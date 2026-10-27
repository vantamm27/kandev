package store

import (
	"context"
	"fmt"
	"time"
)

// TaskOutcome is a claim with its seat's owner, for metrics.
type TaskOutcome struct {
	Claim
	OwnerUserID string
}

// ListTaskOutcomes returns every claim with its seat's owner.
func (s *Store) ListTaskOutcomes(ctx context.Context) ([]TaskOutcome, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+claimColumns+`, COALESCE(s.owner_user_id, '')
		FROM dispatch_claims c LEFT JOIN seats s ON s.id = c.seat_id ORDER BY c.claimed_at, c.task_id`)
	if err != nil {
		return nil, fmt.Errorf("list task outcomes: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []TaskOutcome
	for rows.Next() {
		var o TaskOutcome
		if o.Claim, err = scanClaim(rows, &o.OwnerUserID); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// RecordTaskState stores the Kandev state of a launched, unfinished task and,
// when finished, the time it was first seen finished. An unchanged state is
// not rewritten. It leaves updated_at alone, which tracks launches against
// heartbeats.
func (s *Store) RecordTaskState(ctx context.Context, taskID, taskState string, finished bool, now time.Time) error {
	var finishedAt any
	if finished {
		finishedAt = now.UTC()
	}
	_, err := s.db.ExecContext(ctx, `UPDATE dispatch_claims SET task_state = ?, finished_at = ?
		WHERE task_id = ? AND state = ? AND finished_at IS NULL AND (task_state != ? OR ?)`,
		taskState, finishedAt, taskID, ClaimLaunched, taskState, finished)
	if err != nil {
		return fmt.Errorf("record task state: %w", err)
	}
	return nil
}
