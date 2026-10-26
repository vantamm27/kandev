package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Handoff states.
const (
	HandoffOffered   = "offered"   // waiting for a member to take the task
	HandoffAccepted  = "accepted"  // a member took it; fleetd is moving it
	HandoffDone      = "done"      // the task runs on the new seat
	HandoffFailed    = "failed"    // moving the task failed; offer again later
	HandoffExhausted = "exhausted" // too many handoffs; a person decides
	HandoffWithdrawn = "withdrawn" // the original seat recovered first
)

// MaxHandoffs is how many times one task may move to another member.
const MaxHandoffs = 3

// ErrHandoffOpen means the task already has an open handoff.
var ErrHandoffOpen = errors.New("task already has an open handoff")

// ErrHandoffNotFound means no handoff has the requested ID.
var ErrHandoffNotFound = errors.New("handoff not found")

// Handoff is one offer to move a blocked task to another member's seat.
type Handoff struct {
	ID         string    `json:"id"`
	TaskID     string    `json:"task_id"`
	FromSeatID string    `json:"from_seat_id"`
	ToSeatID   string    `json:"to_seat_id,omitempty"`
	State      string    `json:"state"`
	Path       string    `json:"path,omitempty"`
	Attempt    int       `json:"attempt"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// openHandoffStates are the states that block a new offer for the same task.
var openHandoffStates = []any{HandoffOffered, HandoffAccepted, HandoffExhausted}

// OfferHandoff records an offer for a blocked task. The attempt number counts
// earlier handoffs; past MaxHandoffs the record is created exhausted instead.
func (s *Store) OfferHandoff(ctx context.Context, taskID, fromSeatID string, now time.Time) (Handoff, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Handoff{}, fmt.Errorf("begin offer handoff: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var open, previous int
	args := append([]any{taskID}, openHandoffStates...)
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM handoffs WHERE task_id = ? AND state IN (?, ?, ?)`, args...).Scan(&open); err != nil {
		return Handoff{}, fmt.Errorf("count open handoffs: %w", err)
	}
	if open > 0 {
		return Handoff{}, ErrHandoffOpen
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM handoffs WHERE task_id = ? AND state = ?`, taskID, HandoffDone).Scan(&previous); err != nil {
		return Handoff{}, fmt.Errorf("count handoffs: %w", err)
	}
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return Handoff{}, err
	}
	h := Handoff{ID: "hoff_" + hex.EncodeToString(b), TaskID: taskID, FromSeatID: fromSeatID, State: HandoffOffered,
		Attempt: previous + 1, CreatedAt: now.UTC(), UpdatedAt: now.UTC()}
	if h.Attempt > MaxHandoffs {
		h.State = HandoffExhausted
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO handoffs (id, task_id, from_seat_id, state, attempt, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, h.ID, h.TaskID, h.FromSeatID, h.State, h.Attempt, h.CreatedAt, h.UpdatedAt); err != nil {
		return Handoff{}, fmt.Errorf("insert handoff: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Handoff{}, fmt.Errorf("commit offer handoff: %w", err)
	}
	return h, nil
}

// ListHandoffs returns handoffs in the given states (all when none), newest first.
func (s *Store) ListHandoffs(ctx context.Context, states ...string) ([]Handoff, error) {
	query := `SELECT id, task_id, from_seat_id, COALESCE(to_seat_id, ''), state, path, attempt, created_at, updated_at FROM handoffs`
	args := make([]any, 0, len(states))
	if len(states) > 0 {
		query += ` WHERE state IN (?` + strings.Repeat(",?", len(states)-1) + `)`
		for _, st := range states {
			args = append(args, st)
		}
	}
	query += ` ORDER BY created_at DESC, id`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list handoffs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []Handoff{}
	for rows.Next() {
		var h Handoff
		if err := rows.Scan(&h.ID, &h.TaskID, &h.FromSeatID, &h.ToSeatID, &h.State, &h.Path, &h.Attempt, &h.CreatedAt, &h.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan handoff: %w", err)
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// WithdrawHandoffs withdraws open offers for tasks whose seat recovered.
func (s *Store) WithdrawHandoffs(ctx context.Context, taskID string, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE handoffs SET state = ?, updated_at = ? WHERE task_id = ? AND state = ?`,
		HandoffWithdrawn, now.UTC(), taskID, HandoffOffered)
	if err != nil {
		return fmt.Errorf("withdraw handoffs: %w", err)
	}
	return nil
}
