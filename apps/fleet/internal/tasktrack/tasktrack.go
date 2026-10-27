// Package tasktrack follows the tasks fleetd launched until Kandev reports
// them ended, so /metrics can show each task's state and run time, and frees
// the seat of a task waiting in REVIEW when another task needs it.
package tasktrack

import (
	"context"
	"log/slog"
	"sort"
	"time"

	"github.com/kandev/kandev/apps/fleet/internal/kandev"
	"github.com/kandev/kandev/apps/fleet/internal/store"
)

// Tasks reads one Kandev task.
type Tasks interface {
	GetTask(ctx context.Context, id string) (kandev.Task, error)
}

// StateDeleted is recorded for a launched task Kandev no longer returns.
const StateDeleted = "DELETED"

// Sessions stops Kandev sessions.
type Sessions interface {
	StopSession(ctx context.Context, sessionID, reason string) error
}

// Reasons recorded on sessions fleetd stops.
const (
	stopReason    = "fleetd: task finished, freeing the seat"
	releaseReason = "fleetd: freeing the seat for a queued task while this task waits in review"
)

// DefaultReviewGrace is how long a task must stay in REVIEW before its seat
// can be freed. A workflow step's on_turn_complete move passes through REVIEW
// for a moment; the grace keeps such a task's session.
const DefaultReviewGrace = 5 * time.Minute

// Tracker records the Kandev state of unfinished launched tasks and stops
// the session of an ended one, because a session waiting for input keeps
// its agentctl running and its seat busy.
type Tracker struct {
	store    *store.Store
	tasks    Tasks
	sessions Sessions
	log      *slog.Logger
	// ReviewGrace is the minimum time in REVIEW before Release frees a seat.
	ReviewGrace time.Duration
}

// New returns a tracker.
func New(st *store.Store, tasks Tasks, sessions Sessions, log *slog.Logger) *Tracker {
	return &Tracker{store: st, tasks: tasks, sessions: sessions, log: log, ReviewGrace: DefaultReviewGrace}
}

// ended reports whether a task needs its seat no more. REVIEW is not ended:
// Kandev sets it after every agent turn, and a workflow can continue the
// same session in a later step.
func ended(state string) bool {
	switch state {
	case kandev.TaskStateCompleted, kandev.TaskStateCancelled, kandev.TaskStateFailed, StateDeleted:
		return true
	default:
		return false
	}
}

// Track records the state of every launched claim not yet seen ended.
// listed holds the tasks the dispatch round already read from Kandev; a
// claim missing from it is read on its own. A task that cannot be read is
// retried on the next round.
func (t *Tracker) Track(ctx context.Context, claims map[string]store.Claim, listed map[string]kandev.Task, now time.Time) error {
	for id, c := range claims {
		if c.State != store.ClaimLaunched || c.FinishedAt != nil {
			continue
		}
		state, err := t.state(ctx, id, listed)
		if err != nil {
			t.log.Warn("task state unavailable", "task_id", id, "error", err)
			continue
		}
		finished := ended(state)
		if state == c.TaskState && !finished {
			continue
		}
		if err := t.store.RecordTaskState(ctx, id, state, finished, now); err != nil {
			return err
		}
		if finished {
			t.log.Info("task finished", "task_id", id, "state", state)
			t.stopSession(ctx, c, state)
		}
	}
	return nil
}

// Release frees seats for queued tasks: for each owner in demand (the number
// of their tasks waiting because their seats are full) it stops that many
// sessions of tasks in REVIEW for at least ReviewGrace, oldest first, on the
// owner's seats in seatOwners (seat ID to owner, only seats that can take
// work). A released task can be launched again when its workflow continues.
func (t *Tracker) Release(ctx context.Context, demand map[string]int, seatOwners map[string]string, now time.Time) error {
	if len(demand) == 0 {
		return nil
	}
	claims, err := t.store.ListClaims(ctx)
	if err != nil {
		return err
	}
	var idle []store.Claim
	for _, c := range claims {
		if c.State != store.ClaimLaunched || c.FinishedAt != nil || c.SessionID == "" ||
			c.TaskState != kandev.TaskStateReview || c.TaskStateAt == nil || now.Sub(*c.TaskStateAt) < t.ReviewGrace {
			continue
		}
		if demand[seatOwners[c.SeatID]] > 0 {
			idle = append(idle, c)
		}
	}
	sort.Slice(idle, func(i, j int) bool {
		if !idle[i].TaskStateAt.Equal(*idle[j].TaskStateAt) {
			return idle[i].TaskStateAt.Before(*idle[j].TaskStateAt)
		}
		return idle[i].TaskID < idle[j].TaskID
	})
	for _, c := range idle {
		owner := seatOwners[c.SeatID]
		if demand[owner] <= 0 {
			continue
		}
		if err := t.sessions.StopSession(ctx, c.SessionID, releaseReason); err != nil {
			t.log.Warn("could not stop a review session to free its seat", "task_id", c.TaskID, "session_id", c.SessionID, "error", err)
			continue
		}
		if _, err := t.store.ReleaseClaim(ctx, c.TaskID, now); err != nil {
			return err
		}
		demand[owner]--
		t.log.Info("freed a seat held by a task in review", "task_id", c.TaskID, "seat_id", c.SeatID, "session_id", c.SessionID)
	}
	return nil
}

// stopSession ends the finished task's session. A failed stop is logged; the
// seat stays busy until someone stops the session.
func (t *Tracker) stopSession(ctx context.Context, c store.Claim, state string) {
	if c.SessionID == "" || state == StateDeleted {
		return
	}
	if err := t.sessions.StopSession(ctx, c.SessionID, stopReason); err != nil {
		t.log.Warn("could not stop a finished task's session", "task_id", c.TaskID, "session_id", c.SessionID, "error", err)
	}
}

func (t *Tracker) state(ctx context.Context, id string, listed map[string]kandev.Task) (string, error) {
	if task, ok := listed[id]; ok {
		return task.State, nil
	}
	task, err := t.tasks.GetTask(ctx, id)
	if kandev.IsNotFound(err) {
		return StateDeleted, nil
	}
	return task.State, err
}
