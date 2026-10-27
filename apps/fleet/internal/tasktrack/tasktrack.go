// Package tasktrack follows the tasks fleetd launched until Kandev reports
// them finished, so /metrics can show each task's state and run time.
package tasktrack

import (
	"context"
	"log/slog"
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

// Tracker records the Kandev state of unfinished launched tasks.
type Tracker struct {
	store *store.Store
	tasks Tasks
	log   *slog.Logger
}

// New returns a tracker.
func New(st *store.Store, tasks Tasks, log *slog.Logger) *Tracker {
	return &Tracker{store: st, tasks: tasks, log: log}
}

// Track records the state of every launched claim not yet seen finished.
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
		// A failed task also ends the seat's work, unlike for handoffs and
		// nudges, which leave it to a person.
		finished := kandev.TaskDone(state) || state == kandev.TaskStateFailed || state == StateDeleted
		if state == c.TaskState && !finished {
			continue
		}
		if err := t.store.RecordTaskState(ctx, id, state, finished, now); err != nil {
			return err
		}
		if finished {
			t.log.Info("task finished", "task_id", id, "state", state)
		}
	}
	return nil
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
