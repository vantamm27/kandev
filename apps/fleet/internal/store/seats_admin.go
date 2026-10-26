package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrSeatStateConflict means the seat is not in a state the operation allows.
var ErrSeatStateConflict = errors.New("seat is not in a state that allows this operation")

// SeatSummary is a seat as admins see it: no secret.
type SeatSummary struct {
	ID                      string     `json:"id"`
	OwnerUserID             string     `json:"owner_user_id"`
	SSHHost                 string     `json:"ssh_host"`
	SSHPort                 int        `json:"ssh_port"`
	SSHUser                 string     `json:"ssh_user"`
	Labels                  []string   `json:"labels"`
	Capacity                int        `json:"capacity"`
	FreeSlots               int        `json:"free_slots"`
	State                   string     `json:"state"`
	LimitReason             string     `json:"limit_reason"`
	Quota5hPct              *float64   `json:"quota_5h_pct,omitempty"`
	QuotaWeekPct            *float64   `json:"quota_week_pct,omitempty"`
	Reset5hAt               *time.Time `json:"reset_5h_at,omitempty"`
	ResetWeekAt             *time.Time `json:"reset_week_at,omitempty"`
	AgentVersion            string     `json:"agent_version"`
	KandevExecutorID        string     `json:"kandev_executor_id"`
	KandevExecutorProfileID string     `json:"kandev_executor_profile_id"`
	LastHeartbeatAt         *time.Time `json:"last_heartbeat_at,omitempty"`
}

// ListSeats returns every seat, newest first.
func (s *Store) ListSeats(ctx context.Context) ([]SeatSummary, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, owner_user_id, ssh_host, ssh_port, ssh_user, labels,
		capacity, free_slots, state, limit_reason, quota_5h_pct, quota_week_pct, reset_5h_at, reset_week_at,
		agent_version, kandev_executor_id, kandev_executor_profile_id, last_heartbeat_at
		FROM seats ORDER BY created_at DESC, id`)
	if err != nil {
		return nil, fmt.Errorf("list seats: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []SeatSummary{}
	for rows.Next() {
		var seat SeatSummary
		var labels string
		if err := rows.Scan(&seat.ID, &seat.OwnerUserID, &seat.SSHHost, &seat.SSHPort, &seat.SSHUser, &labels,
			&seat.Capacity, &seat.FreeSlots, &seat.State, &seat.LimitReason, &seat.Quota5hPct, &seat.QuotaWeekPct,
			&seat.Reset5hAt, &seat.ResetWeekAt, &seat.AgentVersion, &seat.KandevExecutorID,
			&seat.KandevExecutorProfileID, &seat.LastHeartbeatAt); err != nil {
			return nil, fmt.Errorf("scan seat: %w", err)
		}
		if err := decodeLabels(labels, &seat.Labels); err != nil {
			return nil, err
		}
		out = append(out, seat)
	}
	return out, rows.Err()
}

// SetSeatState moves a seat to `to` only from one of `from`.
func (s *Store) SetSeatState(ctx context.Context, id string, from []string, to string, now time.Time) error {
	if len(from) == 0 {
		return errors.New("SetSeatState needs at least one allowed source state")
	}
	query := `UPDATE seats SET state = ?, updated_at = ? WHERE id = ? AND state IN (?` + strings.Repeat(",?", len(from)-1) + `)`
	args := []any{to, now.UTC(), id}
	for _, state := range from {
		args = append(args, state)
	}
	res, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("set seat state: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("set seat state: %w", err)
	} else if n == 1 {
		return nil
	}
	if _, err := s.GetSeat(ctx, id); err != nil {
		return err
	}
	return ErrSeatStateConflict
}

// RotateSeatSecret replaces a live seat's HMAC secret.
func (s *Store) RotateSeatSecret(ctx context.Context, id, secret string, now time.Time) error {
	res, err := s.db.ExecContext(ctx, `UPDATE seats SET hmac_secret = ?, last_signed_at = 0, updated_at = ?
		WHERE id = ? AND state != ?`, secret, now.UTC(), id, SeatRetired)
	if err != nil {
		return fmt.Errorf("rotate seat secret: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("rotate seat secret: %w", err)
	} else if n == 1 {
		return nil
	}
	if _, err := s.GetSeat(ctx, id); err != nil {
		return err
	}
	return ErrSeatStateConflict
}
