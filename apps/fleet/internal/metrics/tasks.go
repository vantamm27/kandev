package metrics

import (
	"io"
	"strconv"
	"time"

	"github.com/kandev/kandev/apps/fleet/internal/store"
)

// taskWindow is how long a finished or failed task keeps its per-task series.
// Running tasks always have one.
const taskWindow = 24 * time.Hour

// WriteTasks writes task counts per seat and per-task state and run time.
func WriteTasks(w io.Writer, tasks []store.TaskOutcome, now time.Time) error {
	e := &encoder{w: w}
	type key struct{ seat, owner, outcome string }
	counts := map[key]int{}
	var order []key
	for _, t := range tasks {
		k := key{t.SeatID, t.OwnerUserID, t.Outcome()}
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
		if end, ok := t.End(); !ok || now.Sub(end) < taskWindow {
			recent = append(recent, t)
		}
	}
	e.help("fleet_task_info", "A recent task's seat, Kandev state and outcome (value is always 1).")
	for _, t := range recent {
		e.sample("fleet_task_info", labels{{"task", t.TaskID}, {"seat", t.SeatID}, {"owner", t.OwnerUserID},
			{"state", t.TaskState}, {"outcome", t.Outcome()}, {"attempt", strconv.Itoa(t.Attempt)}}, 1)
	}
	e.help("fleet_task_duration_seconds", "Seconds from launch until the task finished, or until now while it runs.")
	for _, t := range recent {
		if d, ok := t.Duration(now); ok {
			e.sample("fleet_task_duration_seconds", labels{{"task", t.TaskID}, {"seat", t.SeatID}, {"owner", t.OwnerUserID}}, d.Seconds())
		}
	}
	return e.err
}
