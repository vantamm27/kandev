package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Claim states.
const (
	ClaimLaunching = "launching"
	ClaimLaunched  = "launched"
	ClaimFailed    = "failed"
	// ClaimReleased: fleetd stopped the task's session to free its seat
	// while the task waited in REVIEW; the task may be launched again.
	ClaimReleased = "released"
)

// ErrAlreadyClaimed means the task has a launching or launched claim.
var ErrAlreadyClaimed = errors.New("task is already claimed")

// Claim records fleetd's attempt to start one task.
type Claim struct {
	TaskID    string
	SeatID    string
	Attempt   int
	State     string
	SessionID string
	LastError string
	// TaskState is the Kandev task state fleetd last saw for a launched
	// task, TaskStateAt when it first saw that state, and FinishedAt when it
	// first saw the task finished or released it.
	TaskState   string
	TaskStateAt *time.Time
	FinishedAt  *time.Time
	// ReleasedStepID is the workflow step a released task was in when fleetd
	// freed its seat; empty for claims released before it was recorded.
	ReleasedStepID string
	// PausedAt is when fleetd saw the task's session stop at its seat's
	// usage limit; the session waits there for the limit to reset.
	PausedAt  *time.Time
	ClaimedAt time.Time
	UpdatedAt time.Time
}

const claimColumns = `c.task_id, c.seat_id, c.attempt, c.state, c.session_id, c.last_error, c.task_state, c.task_state_at,
	c.finished_at, c.released_step_id, c.paused_at, c.claimed_at, c.updated_at`

type rowScanner interface{ Scan(dest ...any) error }

func scanClaim(row rowScanner, extra ...any) (Claim, error) {
	var c Claim
	var stateAt, finished, paused sql.NullTime
	dest := append([]any{&c.TaskID, &c.SeatID, &c.Attempt, &c.State, &c.SessionID, &c.LastError, &c.TaskState, &stateAt,
		&finished, &c.ReleasedStepID, &paused, &c.ClaimedAt, &c.UpdatedAt}, extra...)
	if err := row.Scan(dest...); err != nil {
		return Claim{}, fmt.Errorf("scan claim: %w", err)
	}
	c.TaskStateAt = nullTime(stateAt)
	c.FinishedAt = nullTime(finished)
	c.PausedAt = nullTime(paused)
	return c, nil
}

func nullTime(t sql.NullTime) *time.Time {
	if !t.Valid {
		return nil
	}
	v := t.Time
	return &v
}

// ListClaims returns every claim keyed by task ID.
func (s *Store) ListClaims(ctx context.Context) (map[string]Claim, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+claimColumns+` FROM dispatch_claims c`)
	if err != nil {
		return nil, fmt.Errorf("list claims: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]Claim{}
	for rows.Next() {
		c, err := scanClaim(rows)
		if err != nil {
			return nil, err
		}
		out[c.TaskID] = c
	}
	return out, rows.Err()
}

// BeginClaim claims a task for a seat. A new task gets attempt 1; a failed
// claim is retried with the next attempt number, and a released claim starts
// over at attempt 1. A launching or launched claim is never taken over.
func (s *Store) BeginClaim(ctx context.Context, taskID, seatID string, now time.Time) (int, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO dispatch_claims (task_id, seat_id, attempt, state, claimed_at, updated_at)
		VALUES (?, ?, 1, ?, ?, ?)
		ON CONFLICT(task_id) DO UPDATE SET seat_id = excluded.seat_id,
			attempt = CASE WHEN dispatch_claims.state = ? THEN 1 ELSE dispatch_claims.attempt + 1 END,
			state = excluded.state, session_id = '', last_error = '', task_state = '', task_state_at = NULL,
			finished_at = NULL, released_step_id = '', paused_at = NULL, claimed_at = excluded.claimed_at,
			updated_at = excluded.updated_at
		WHERE dispatch_claims.state IN (?, ?)`,
		taskID, seatID, ClaimLaunching, now.UTC(), now.UTC(), ClaimReleased, ClaimFailed, ClaimReleased)
	if err != nil {
		return 0, fmt.Errorf("begin claim: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return 0, fmt.Errorf("begin claim: %w", err)
	} else if n == 0 {
		return 0, ErrAlreadyClaimed
	}
	var attempt int
	if err := s.db.QueryRowContext(ctx, `SELECT attempt FROM dispatch_claims WHERE task_id = ?`, taskID).Scan(&attempt); err != nil {
		return 0, fmt.Errorf("read claim attempt: %w", err)
	}
	return attempt, nil
}

// FinishClaim records a launch outcome for a launching claim.
func (s *Store) FinishClaim(ctx context.Context, taskID, state, sessionID, lastError string, now time.Time) error {
	res, err := s.db.ExecContext(ctx, `UPDATE dispatch_claims SET state = ?, session_id = ?, last_error = ?, updated_at = ?
		WHERE task_id = ? AND state = ?`, state, sessionID, truncate(lastError, 1000), now.UTC(), taskID, ClaimLaunching)
	if err != nil {
		return fmt.Errorf("finish claim: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return fmt.Errorf("finish claim %s: no launching claim", taskID)
	}
	return nil
}

// FailStaleLaunching fails claims left launching since before cutoff, which
// happens when fleetd stopped mid-launch. They become retryable.
func (s *Store) FailStaleLaunching(ctx context.Context, cutoff, now time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE dispatch_claims SET state = ?, last_error = 'fleetd stopped during launch', updated_at = ?
		WHERE state = ? AND updated_at < ?`, ClaimFailed, now.UTC(), ClaimLaunching, cutoff.UTC())
	if err != nil {
		return 0, fmt.Errorf("fail stale launching claims: %w", err)
	}
	return res.RowsAffected()
}

// UnconfirmedLaunches counts, per seat, launches newer than the seat's last
// heartbeat: sessions the seat's free-slot report does not include yet.
func (s *Store) UnconfirmedLaunches(ctx context.Context) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT c.seat_id, COUNT(*) FROM dispatch_claims c JOIN seats s ON s.id = c.seat_id
		WHERE c.state IN (?, ?) AND (s.last_heartbeat_at IS NULL OR c.updated_at >= s.last_heartbeat_at)
		GROUP BY c.seat_id`, ClaimLaunching, ClaimLaunched)
	if err != nil {
		return nil, fmt.Errorf("count unconfirmed launches: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]int{}
	for rows.Next() {
		var seatID string
		var n int
		if err := rows.Scan(&seatID, &n); err != nil {
			return nil, err
		}
		out[seatID] = n
	}
	return out, rows.Err()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// MoveClaim points a task's claim at the seat and session it now runs on,
// as a launched claim tracked afresh.
func (s *Store) MoveClaim(ctx context.Context, taskID, seatID, sessionID string, now time.Time) error {
	res, err := s.db.ExecContext(ctx, `UPDATE dispatch_claims SET seat_id = ?, session_id = ?, state = ?, last_error = '',
			task_state = '', task_state_at = NULL, finished_at = NULL, paused_at = NULL, updated_at = ?
		WHERE task_id = ?`, seatID, sessionID, ClaimLaunched, now.UTC(), taskID)
	if err != nil {
		return fmt.Errorf("move claim: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return fmt.Errorf("move claim %s: no claim", taskID)
	}
	return nil
}

// ReleaseClaim marks an unfinished launched claim released after fleetd
// stopped its session to free the seat, with the workflow step the task was
// in. It reports whether a claim changed.
func (s *Store) ReleaseClaim(ctx context.Context, taskID, stepID string, now time.Time) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE dispatch_claims SET state = ?, finished_at = ?, released_step_id = ?
		WHERE task_id = ? AND state = ? AND finished_at IS NULL`, ClaimReleased, now.UTC(), stepID, taskID, ClaimLaunched)
	if err != nil {
		return false, fmt.Errorf("release claim: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("release claim: %w", err)
	}
	return n == 1, nil
}

// AdoptSession tracks a session Kandev started for a released task on its
// own, as if fleetd had launched it on the claim's seat.
func (s *Store) AdoptSession(ctx context.Context, taskID, sessionID string, now time.Time) error {
	res, err := s.db.ExecContext(ctx, `UPDATE dispatch_claims SET state = ?, session_id = ?, task_state = '',
		task_state_at = NULL, finished_at = NULL, nudged_at = NULL, released_step_id = '', paused_at = NULL, updated_at = ?
		WHERE task_id = ? AND state = ?`, ClaimLaunched, sessionID, now.UTC(), taskID, ClaimReleased)
	if err != nil {
		return fmt.Errorf("adopt session: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return fmt.Errorf("adopt session for %s: no released claim", taskID)
	}
	return nil
}

// FailRunningClaim marks an unfinished launched claim failed after its
// session failed mid-run, with the task state fleetd saw. It keeps the claim's
// seat and session, so the dispatcher retries the task on the seat that holds
// its environment, and starts the retry backoff now.
func (s *Store) FailRunningClaim(ctx context.Context, taskID, taskState, lastError string, now time.Time) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE dispatch_claims SET state = ?, last_error = ?, finished_at = ?, paused_at = NULL,
			task_state_at = CASE WHEN task_state = ? THEN task_state_at ELSE ? END, task_state = ?, updated_at = ?
		WHERE task_id = ? AND state = ? AND finished_at IS NULL`,
		ClaimFailed, truncate(lastError, 1000), now.UTC(), taskState, now.UTC(), taskState, now.UTC(), taskID, ClaimLaunched)
	if err != nil {
		return false, fmt.Errorf("fail running claim: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("fail running claim: %w", err)
	}
	return n == 1, nil
}

// PauseClaim records that an unfinished launched task stopped at its seat's
// usage limit. It reports whether the claim was not paused already.
func (s *Store) PauseClaim(ctx context.Context, taskID string, now time.Time) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE dispatch_claims SET paused_at = ?
		WHERE task_id = ? AND state = ? AND finished_at IS NULL AND paused_at IS NULL`, now.UTC(), taskID, ClaimLaunched)
	if err != nil {
		return false, fmt.Errorf("pause claim: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("pause claim: %w", err)
	}
	return n == 1, nil
}

// UnpauseClaim forgets a pause, used when the task went on without fleetd.
func (s *Store) UnpauseClaim(ctx context.Context, taskID string) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE dispatch_claims SET paused_at = NULL WHERE task_id = ?`, taskID); err != nil {
		return fmt.Errorf("unpause claim: %w", err)
	}
	return nil
}

// ResumeClaim clears a pause after fleetd asked the session to continue and
// records the task as running from now, so the next REVIEW the tracker sees
// is the resumed turn's.
func (s *Store) ResumeClaim(ctx context.Context, taskID, runningState string, now time.Time) error {
	res, err := s.db.ExecContext(ctx, `UPDATE dispatch_claims SET paused_at = NULL, task_state = ?, task_state_at = ?
		WHERE task_id = ? AND state = ? AND finished_at IS NULL AND paused_at IS NOT NULL`,
		runningState, now.UTC(), taskID, ClaimLaunched)
	if err != nil {
		return fmt.Errorf("resume claim: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return fmt.Errorf("resume claim %s: no paused claim", taskID)
	}
	return nil
}

// MarkNudged records that the task's session was asked to checkpoint, unless
// it was asked within `every`. It reports whether this call recorded a nudge.
func (s *Store) MarkNudged(ctx context.Context, taskID string, every time.Duration, now time.Time) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE dispatch_claims SET nudged_at = ?
		WHERE task_id = ? AND state = ? AND (nudged_at IS NULL OR nudged_at < ?)`,
		now.UTC(), taskID, ClaimLaunched, now.Add(-every).UTC())
	if err != nil {
		return false, fmt.Errorf("mark nudged: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("mark nudged: %w", err)
	}
	return n == 1, nil
}

// ClearNudge forgets a nudge, used when sending it failed.
func (s *Store) ClearNudge(ctx context.Context, taskID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE dispatch_claims SET nudged_at = NULL WHERE task_id = ?`, taskID)
	if err != nil {
		return fmt.Errorf("clear nudge: %w", err)
	}
	return nil
}
