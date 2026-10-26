// Package nudge asks running agents to checkpoint before their seat runs out
// of quota, so a later handoff resumes from fresh WIP (plan D9).
package nudge

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/kandev/kandev/apps/fleet/internal/kandev"
	"github.com/kandev/kandev/apps/fleet/internal/store"
)

// Messenger sends a message to a task session.
type Messenger interface {
	AddMessage(ctx context.Context, taskID, sessionID, content string) error
}

// Tasks reads one Kandev task.
type Tasks interface {
	GetTask(ctx context.Context, id string) (kandev.Task, error)
}

const (
	// ThresholdPct is the 5-hour or weekly usage that triggers a nudge.
	ThresholdPct = 85
	// Every is the minimum time between nudges for one task, one 5-hour window.
	Every = 5 * time.Hour
)

// Message is what the agent receives for a task.
func Message(taskID string) string {
	return fmt.Sprintf(`Your seat is close to its usage limit. Before continuing, checkpoint your work:
1. Update PROGRESS.md with what is done, what is next, and how to run the tests.
2. Commit, then run: git push --force origin HEAD:refs/agent-wip/%s
Another member can then continue this task if this seat stops.`, taskID)
}

// doneStates are task states that need no checkpoint.
var doneStates = map[string]bool{"REVIEW": true, "COMPLETED": true, "CANCELLED": true}

// Nudger sends checkpoint nudges.
type Nudger struct {
	store     *store.Store
	tasks     Tasks
	messenger Messenger
	log       *slog.Logger
}

// New returns a nudger.
func New(st *store.Store, tasks Tasks, m Messenger, log *slog.Logger) *Nudger {
	return &Nudger{store: st, tasks: tasks, messenger: m, log: log}
}

// NearLimit reports whether a working seat's reported usage reached the threshold.
func NearLimit(seat store.SeatSummary) bool {
	if seat.State != store.SeatOnline && seat.State != store.SeatBusy {
		return false
	}
	return atLeast(seat.Quota5hPct) || atLeast(seat.QuotaWeekPct)
}

func atLeast(p *float64) bool { return p != nil && *p >= ThresholdPct }

// Scan nudges each running task on a seat near its limit at most once per
// Every. It returns the number of nudges sent.
func (n *Nudger) Scan(ctx context.Context, now time.Time) (int, error) {
	seats, err := n.store.ListSeats(ctx)
	if err != nil {
		return 0, err
	}
	near := map[string]bool{}
	for _, s := range seats {
		if NearLimit(s) {
			near[s.ID] = true
		}
	}
	if len(near) == 0 {
		return 0, nil
	}
	claims, err := n.store.ListClaims(ctx)
	if err != nil {
		return 0, err
	}
	sent := 0
	for _, c := range claims {
		if c.State != store.ClaimLaunched || c.SessionID == "" || !near[c.SeatID] {
			continue
		}
		if n.nudge(ctx, c, now) {
			sent++
		}
	}
	return sent, nil
}

func (n *Nudger) nudge(ctx context.Context, c store.Claim, now time.Time) bool {
	task, err := n.tasks.GetTask(ctx, c.TaskID)
	if err != nil || doneStates[task.State] {
		return false
	}
	marked, err := n.store.MarkNudged(ctx, c.TaskID, Every, now)
	if err != nil || !marked {
		return false
	}
	if err := n.messenger.AddMessage(ctx, c.TaskID, c.SessionID, Message(c.TaskID)); err != nil {
		n.log.Warn("checkpoint nudge failed", "task_id", c.TaskID, "session_id", c.SessionID, "error", err)
		if cerr := n.store.ClearNudge(context.WithoutCancel(ctx), c.TaskID); cerr != nil {
			n.log.Error("clearing a failed nudge failed", "task_id", c.TaskID, "error", cerr)
		}
		return false
	}
	n.log.Info("checkpoint nudge sent", "task_id", c.TaskID, "seat_id", c.SeatID, "session_id", c.SessionID)
	return true
}
