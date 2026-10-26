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

// doneStates are task states that need no seat.
var doneStates = map[string]bool{"REVIEW": true, "COMPLETED": true, "CANCELLED": true}

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

// Scan offers a handoff for every launched task on a blocked seat that is not
// done yet, and withdraws offers whose seat is usable again. It returns the
// number of new offers.
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
		if c.State != store.ClaimLaunched {
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

func (d *Detector) offer(ctx context.Context, c store.Claim, seat store.SeatSummary, now time.Time) (bool, error) {
	task, err := d.tasks.GetTask(ctx, c.TaskID)
	if kandev.IsNotFound(err) || (err == nil && doneStates[task.State]) {
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
