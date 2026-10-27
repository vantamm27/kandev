package metrics

import (
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/kandev/kandev/apps/fleet/internal/store"
)

// taskWindow is how long a finished or failed task keeps its per-task series.
// Running tasks always have one.
const taskWindow = 24 * time.Hour

// taskOutcome classifies a claimed task for fleet_tasks: launching, running,
// launch_failed, or the finished Kandev state in lower case.
func taskOutcome(t store.TaskOutcome) string {
	switch {
	case t.State == store.ClaimFailed:
		return "launch_failed"
	case t.State == store.ClaimLaunching:
		return "launching"
	case t.FinishedAt == nil:
		return "running"
	default:
		return strings.ToLower(t.TaskState)
	}
}

// taskEnd is when a task stopped running: the claim time for a failed launch,
// the finish time for a finished task, and nothing while it runs.
func taskEnd(t store.TaskOutcome) (time.Time, bool) {
	switch {
	case t.State == store.ClaimFailed:
		return t.ClaimedAt, true
	case t.FinishedAt != nil:
		return *t.FinishedAt, true
	default:
		return time.Time{}, false
	}
}

// WriteTasks writes task counts per seat and per-task state and run time.
func WriteTasks(w io.Writer, tasks []store.TaskOutcome, now time.Time) error {
	e := &encoder{w: w}
	type key struct{ seat, owner, outcome string }
	counts := map[key]int{}
	var order []key
	for _, t := range tasks {
		k := key{t.SeatID, t.OwnerUserID, taskOutcome(t)}
		if counts[k] == 0 {
			order = append(order, k)
		}
		counts[k]++
	}
	e.help("fleet_tasks", "Tasks fleetd launched, by seat and outcome, since the fleet database was created.")
	for _, k := range order {
		e.sample("fleet_tasks", labels{{"seat", k.seat}, {"owner", k.owner}, {"outcome", k.outcome}}, float64(counts[k]))
	}
	var recent []store.TaskOutcome
	for _, t := range tasks {
		if end, ok := taskEnd(t); !ok || now.Sub(end) < taskWindow {
			recent = append(recent, t)
		}
	}
	e.help("fleet_task_info", "A recent task's seat, Kandev state and outcome (value is always 1).")
	for _, t := range recent {
		e.sample("fleet_task_info", labels{{"task", t.TaskID}, {"seat", t.SeatID}, {"owner", t.OwnerUserID},
			{"state", t.TaskState}, {"outcome", taskOutcome(t)}, {"attempt", strconv.Itoa(t.Attempt)}}, 1)
	}
	e.help("fleet_task_duration_seconds", "Seconds from launch until the task finished, or until now while it runs.")
	for _, t := range recent {
		if t.State == store.ClaimFailed {
			continue
		}
		end, ok := taskEnd(t)
		if !ok {
			end = now
		}
		e.sample("fleet_task_duration_seconds", labels{{"task", t.TaskID}, {"seat", t.SeatID}, {"owner", t.OwnerUserID}},
			end.Sub(t.ClaimedAt).Seconds())
	}
	return e.err
}
