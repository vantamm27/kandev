package store

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// TaskOutcome is a claim with its seat's owner, for metrics and the admin API.
type TaskOutcome struct {
	Claim
	OwnerUserID string
}

// Outcome classifies the task: launching, running, launch_failed, released,
// or the finished Kandev state in lower case.
func (t TaskOutcome) Outcome() string {
	switch {
	case t.State == ClaimFailed:
		return "launch_failed"
	case t.State == ClaimLaunching:
		return "launching"
	case t.State == ClaimReleased:
		return "released"
	case t.FinishedAt == nil:
		return "running"
	default:
		return strings.ToLower(t.TaskState)
	}
}

// End is when the task stopped running: the claim time for a failed launch,
// the finish time for a finished task, and nothing while it runs.
func (t TaskOutcome) End() (time.Time, bool) {
	switch {
	case t.State == ClaimFailed:
		return t.ClaimedAt, true
	case t.FinishedAt != nil:
		return *t.FinishedAt, true
	default:
		return time.Time{}, false
	}
}

// Duration is how long the task ran: from launch to finish, or to now while
// it runs. A failed launch never ran.
func (t TaskOutcome) Duration(now time.Time) (time.Duration, bool) {
	if t.State == ClaimFailed {
		return 0, false
	}
	end, ok := t.End()
	if !ok {
		end = now
	}
	return end.Sub(t.ClaimedAt), true
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

// RecordTaskState stores the Kandev state of a launched, unfinished task, the
// time that state was first seen and, when finished, the time it was first
// seen finished. An unchanged state is not rewritten. It leaves updated_at
// alone, which tracks launches against heartbeats.
func (s *Store) RecordTaskState(ctx context.Context, taskID, taskState string, finished bool, now time.Time) error {
	var finishedAt any
	if finished {
		finishedAt = now.UTC()
	}
	_, err := s.db.ExecContext(ctx, `UPDATE dispatch_claims SET task_state = ?, finished_at = ?,
			task_state_at = CASE WHEN task_state = ? THEN task_state_at ELSE ? END
		WHERE task_id = ? AND state = ? AND finished_at IS NULL AND (task_state != ? OR ?)`,
		taskState, finishedAt, taskState, now.UTC(), taskID, ClaimLaunched, taskState, finished)
	if err != nil {
		return fmt.Errorf("record task state: %w", err)
	}
	return nil
}
