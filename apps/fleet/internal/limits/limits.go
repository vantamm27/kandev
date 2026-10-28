// Package limits finds launched tasks stuck behind a seat's usage limit and
// offers them to other members (plan D6 and D7). A short 5-hour cooldown is
// left to Kandev's dynamic profile, which waits for the reset and resumes.
package limits

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/kandev/kandev/apps/fleet/internal/kandev"
	"github.com/kandev/kandev/apps/fleet/internal/store"
)

// Tasks reads one Kandev task.
type Tasks interface {
	GetTask(ctx context.Context, id string) (kandev.Task, error)
}

// DefaultWaitThreshold is the longest 5-hour reset fleetd waits out before
// offering the task to someone else.
const DefaultWaitThreshold = 2 * time.Hour

// Detector offers handoffs for blocked tasks.
type Detector struct {
	store         *store.Store
	tasks         Tasks
	log           *slog.Logger
	waitThreshold time.Duration
}

// New returns a detector.
func New(st *store.Store, tasks Tasks, log *slog.Logger) *Detector {
	return &Detector{store: st, tasks: tasks, log: log, waitThreshold: DefaultWaitThreshold}
}

// Blocked reports whether a seat's limit lasts long enough to move its work.
func Blocked(seat store.SeatSummary, now time.Time, threshold time.Duration) bool {
	switch seat.State {
	case store.SeatCooldownWeek:
		return true
	case store.SeatCooldown5h:
		return seat.Reset5hAt == nil || seat.Reset5hAt.Sub(now) > threshold
	default:
		return false
	}
}

// Scan offers a handoff for every task on a blocked seat that still needs
// it: launched and not done, paused at the limit, or failed mid-run and
// waiting for a retry there. It withdraws offers whose seat is usable again.
// It returns the number of new offers.
func (d *Detector) Scan(ctx context.Context, now time.Time) (int, error) {
	seats, err := d.store.ListSeats(ctx)
	if err != nil {
		return 0, err
	}
	byID := make(map[string]store.SeatSummary, len(seats))
	for _, s := range seats {
		byID[s.ID] = s
	}
	claims, err := d.store.ListClaims(ctx)
	if err != nil {
		return 0, err
	}
	offered := 0
	for _, c := range claims {
		if !holdsSeat(c) {
			continue
		}
		seat, ok := byID[c.SeatID]
		if !ok {
			continue
		}
		if !Blocked(seat, now, d.waitThreshold) {
			if err := d.store.WithdrawHandoffs(ctx, c.TaskID, now); err != nil {
				return offered, err
			}
			continue
		}
		made, err := d.offer(ctx, c, seat, now)
		if err != nil {
			return offered, err
		}
		if made {
			offered++
		}
	}
	return offered, nil
}

// holdsSeat reports whether a claim's task waits on its seat: launched and
// not finished, or failed mid-run (its session ran there) with its
// environment on that seat.
func holdsSeat(c store.Claim) bool {
	switch c.State {
	case store.ClaimLaunched:
		return c.FinishedAt == nil
	case store.ClaimFailed:
		return c.SessionID != ""
	default:
		return false
	}
}

func (d *Detector) offer(ctx context.Context, c store.Claim, seat store.SeatSummary, now time.Time) (bool, error) {
	task, err := d.tasks.GetTask(ctx, c.TaskID)
	if kandev.IsNotFound(err) || (err == nil && !needsWork(task, c)) {
		return false, nil
	}
	if err != nil {
		d.log.Warn("cannot read a blocked task", "task_id", c.TaskID, "error", err)
		return false, nil
	}
	h, err := d.store.OfferHandoff(ctx, c.TaskID, seat.ID, now)
	if errors.Is(err, store.ErrHandoffOpen) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if h.State == store.HandoffExhausted {
		d.log.Warn("task moved too many times; a person needs to decide", "task_id", c.TaskID, "handoffs", h.Attempt-1)
		return false, nil
	}
	d.log.Info("handoff offered", "handoff_id", h.ID, "task_id", c.TaskID, "from_seat_id", seat.ID,
		"owner_user_id", seat.OwnerUserID, "seat_state", seat.State, "attempt", h.Attempt)
	return true, nil
}

// needsWork reports whether a blocked task has work left. A task in REVIEW
// is done with its turn, unless it stopped at the usage limit or its session
// failed.
func needsWork(t kandev.Task, c store.Claim) bool {
	switch {
	case t.State == kandev.TaskStateCompleted || t.State == kandev.TaskStateCancelled:
		return false
	case c.PausedAt != nil || c.State == store.ClaimFailed:
		return true
	default:
		return !kandev.TaskDone(t.State)
	}
}
