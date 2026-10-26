package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/mattn/go-sqlite3"
)

// Seat states. A seat is one subscription account on one node user.
const (
	SeatRegistering  = "registering"
	SeatOnline       = "online"
	SeatBusy         = "busy"
	SeatCooldown5h   = "cooldown_5h"
	SeatCooldownWeek = "cooldown_week"
	SeatDraining     = "draining"
	SeatOffline      = "offline"
	SeatRetired      = "retired"
)

// ErrSeatExists means a live seat already uses the same SSH host, port, and user.
var ErrSeatExists = errors.New("a seat already uses this ssh host, port, and user")

// ErrSeatNotFound means no seat has the requested ID.
var ErrSeatNotFound = errors.New("seat not found")

// Seat is a registered seat.
type Seat struct {
	ID                      string
	OwnerUserID             string
	SSHHost                 string
	SSHPort                 int
	SSHUser                 string
	HostKeyFingerprint      string
	Labels                  []string
	Capacity                int
	KandevExecutorID        string
	KandevExecutorProfileID string
	HMACSecret              string
	State                   string
	AgentVersion            string
	CreatedAt               time.Time
	UpdatedAt               time.Time
}

// RegisterSeat consumes the registration token and inserts the seat in one
// transaction, so a token registers at most one seat.
func (s *Store) RegisterSeat(ctx context.Context, rawToken string, seat Seat, now time.Time) error {
	labels, err := json.Marshal(nonNilLabels(seat.Labels))
	if err != nil {
		return fmt.Errorf("encode labels: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin register seat: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx, `UPDATE registration_tokens SET used_at = ?, used_by_seat_id = ?
		WHERE token_hash = ? AND used_at IS NULL AND expires_at > ?`,
		now.UTC(), seat.ID, HashToken(rawToken), now.UTC())
	if err != nil {
		return fmt.Errorf("consume registration token: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return ErrTokenInvalid
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO seats (id, owner_user_id, ssh_host, ssh_port, ssh_user,
		host_key_fingerprint, labels, capacity, kandev_executor_id, kandev_executor_profile_id,
		hmac_secret, state, agent_version, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		seat.ID, seat.OwnerUserID, seat.SSHHost, seat.SSHPort, seat.SSHUser, seat.HostKeyFingerprint,
		string(labels), seat.Capacity, seat.KandevExecutorID, seat.KandevExecutorProfileID,
		seat.HMACSecret, seat.State, seat.AgentVersion, now.UTC(), now.UTC())
	if err != nil {
		if isUniqueViolation(err) {
			return ErrSeatExists
		}
		return fmt.Errorf("insert seat: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit register seat: %w", err)
	}
	return nil
}

// SeatEndpointTaken reports whether a live (not retired) seat uses the
// endpoint, so registration can fail before creating Kandev resources.
func (s *Store) SeatEndpointTaken(ctx context.Context, host string, port int, user string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM seats
		WHERE ssh_host = ? AND ssh_port = ? AND ssh_user = ? AND state != ?`, host, port, user, SeatRetired).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("check seat endpoint: %w", err)
	}
	return n > 0, nil
}

// GetSeat reads one seat.
func (s *Store) GetSeat(ctx context.Context, id string) (Seat, error) {
	var seat Seat
	var labels string
	err := s.db.QueryRowContext(ctx, `SELECT id, owner_user_id, ssh_host, ssh_port, ssh_user,
		host_key_fingerprint, labels, capacity, kandev_executor_id, kandev_executor_profile_id,
		hmac_secret, state, agent_version, created_at, updated_at FROM seats WHERE id = ?`, id).
		Scan(&seat.ID, &seat.OwnerUserID, &seat.SSHHost, &seat.SSHPort, &seat.SSHUser,
			&seat.HostKeyFingerprint, &labels, &seat.Capacity, &seat.KandevExecutorID,
			&seat.KandevExecutorProfileID, &seat.HMACSecret, &seat.State, &seat.AgentVersion,
			&seat.CreatedAt, &seat.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Seat{}, ErrSeatNotFound
	}
	if err != nil {
		return Seat{}, fmt.Errorf("read seat: %w", err)
	}
	if err := decodeLabels(labels, &seat.Labels); err != nil {
		return Seat{}, err
	}
	return seat, nil
}

// FreeSlots reads the seat's free slots from its last heartbeat.
func (s *Store) FreeSlots(ctx context.Context, id string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT free_slots FROM seats WHERE id = ?`, id).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrSeatNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("read free slots: %w", err)
	}
	return n, nil
}

func decodeLabels(raw string, out *[]string) error {
	if err := json.Unmarshal([]byte(raw), out); err != nil {
		return fmt.Errorf("decode seat labels: %w", err)
	}
	return nil
}

func isUniqueViolation(err error) bool {
	var sqliteErr sqlite3.Error
	return errors.As(err, &sqliteErr) && sqliteErr.ExtendedCode == sqlite3.ErrConstraintUnique
}
