// Package schedule decides which queued tasks start on which seats. It has
// no I/O: the dispatcher passes in a snapshot and applies the plan.
//
// Rules, in order (plan D6 and D10):
//   - A task only runs on a seat owned by its assignee (seats are not pooled).
//   - The seat must be online with a free slot and carry the task's labels.
//   - Quota gate: a known remaining 5-hour or weekly quota smaller than the
//     task's estimated cost excludes the seat.
//   - Weekly pacing: low and medium tasks wait while the seat's weekly usage
//     is ahead of an even spend across the week; critical and high do not.
//   - Order: priority base plus capped aging, so low can age up to medium and
//     medium up to high, and nothing ages past critical. Ties go to the
//     oldest task. A task that cannot start does not block those behind it.
package schedule

import (
	"sort"
	"time"
)

// Priorities, matching Kandev's task priority values.
const (
	PriorityCritical = "critical"
	PriorityHigh     = "high"
	PriorityMedium   = "medium"
	PriorityLow      = "low"
)

// Seat states the scheduler cares about.
const seatOnline = "online"

// Task is a queued task waiting for a seat.
type Task struct {
	ID          string
	OwnerUserID string
	Priority    string
	QueuedAt    time.Time
	// Labels the seat must carry, for example "xcode".
	RequiredLabels []string
	// SeatID, when set, is the only seat the task may run on: the one that
	// holds its environment.
	SeatID string
}

// Seat is a seat's scheduling-relevant state.
type Seat struct {
	ID           string
	OwnerUserID  string
	State        string
	FreeSlots    int
	Labels       []string
	Quota5hPct   *float64
	QuotaWeekPct *float64
	ResetWeekAt  *time.Time
}

// Class is one priority's ordering and gating parameters.
type Class struct {
	Base float64
	// AgingPerMinute is added per minute queued, up to AgingCap.
	AgingPerMinute float64
	AgingCap       float64
	// Paced classes wait while weekly usage runs ahead of an even spend.
	Paced bool
	// PaceSlackPct is how far ahead of even spend a paced class may run.
	PaceSlackPct float64
}

// Policy configures the scheduler.
type Policy struct {
	Classes map[string]Class
	// EstimatedCost5hPct and EstimatedCostWeekPct are the quota a task is
	// assumed to use when nothing better is known.
	EstimatedCost5hPct   float64
	EstimatedCostWeekPct float64
}

// DefaultPolicy is the plan's starting point; tune it from baseline data.
func DefaultPolicy() Policy {
	return Policy{
		Classes: map[string]Class{
			PriorityCritical: {Base: 10000},
			PriorityHigh:     {Base: 1000, AgingPerMinute: 2, AgingCap: 2000},
			PriorityMedium:   {Base: 300, AgingPerMinute: 1, AgingCap: 700, Paced: true, PaceSlackPct: 10},
			PriorityLow:      {Base: 100, AgingPerMinute: 0.5, AgingCap: 200, Paced: true},
		},
		EstimatedCost5hPct:   10,
		EstimatedCostWeekPct: 2,
	}
}

// Reasons a task did not start this round.
const (
	ReasonNoSeat   = "no_seat"       // its owner has no live seat
	ReasonLabels   = "labels"        // no owner seat has the required labels
	ReasonFull     = "seats_full"    // owner seats are busy or cooling down
	ReasonQuota    = "quota"         // remaining quota below the estimated cost
	ReasonPacing   = "weekly_pacing" // weekly usage ahead of even spend
	ReasonPriority = "unknown_priority"
	// ReasonNoAssignee: Kandev does not record who created a task, so an
	// unassigned task has no owner whose seats could run it.
	ReasonNoAssignee = "no_assignee"
)

// Assignment starts a task on a seat.
type Assignment struct {
	TaskID string
	SeatID string
	Score  float64
}

// Waiting is a task that stays queued and why.
type Waiting struct {
	TaskID string
	Reason string
}

// Plan is one scheduling round's decisions.
type Plan struct {
	Assignments []Assignment
	Waiting     []Waiting
}

// Week is the length of Claude's weekly usage window.
const Week = 7 * 24 * time.Hour

// Schedule plans one round.
func Schedule(tasks []Task, seats []Seat, now time.Time, p Policy) Plan {
	free := make(map[string]int, len(seats))
	for _, s := range seats {
		free[s.ID] = s.FreeSlots
	}
	type scored struct {
		task  Task
		score float64
	}
	queue := make([]scored, 0, len(tasks))
	var plan Plan
	for _, t := range tasks {
		if t.OwnerUserID == "" {
			plan.Waiting = append(plan.Waiting, Waiting{TaskID: t.ID, Reason: ReasonNoAssignee})
			continue
		}
		class, ok := p.Classes[t.Priority]
		if !ok {
			plan.Waiting = append(plan.Waiting, Waiting{TaskID: t.ID, Reason: ReasonPriority})
			continue
		}
		queue = append(queue, scored{task: t, score: Score(class, t.QueuedAt, now)})
	}
	sort.SliceStable(queue, func(i, j int) bool {
		a, b := queue[i], queue[j]
		if a.score != b.score {
			return a.score > b.score
		}
		if !a.task.QueuedAt.Equal(b.task.QueuedAt) {
			return a.task.QueuedAt.Before(b.task.QueuedAt)
		}
		return a.task.ID < b.task.ID
	})
	for _, q := range queue {
		seat, reason := pickSeat(q.task, seats, free, now, p)
		if reason != "" {
			plan.Waiting = append(plan.Waiting, Waiting{TaskID: q.task.ID, Reason: reason})
			continue
		}
		free[seat.ID]--
		plan.Assignments = append(plan.Assignments, Assignment{TaskID: q.task.ID, SeatID: seat.ID, Score: q.score})
	}
	return plan
}

// Score is a task's ordering value at now.
func Score(c Class, queuedAt, now time.Time) float64 {
	minutes := now.Sub(queuedAt).Minutes()
	if minutes < 0 {
		minutes = 0
	}
	aging := minutes * c.AgingPerMinute
	if aging > c.AgingCap {
		aging = c.AgingCap
	}
	return c.Base + aging
}

// pickSeat returns the best seat for t, or the most specific reason none fits.
func pickSeat(t Task, seats []Seat, free map[string]int, now time.Time, p Policy) (Seat, string) {
	class := p.Classes[t.Priority]
	reason := ReasonNoSeat
	var best *Seat
	for i := range seats {
		s := &seats[i]
		if s.OwnerUserID != t.OwnerUserID || (t.SeatID != "" && s.ID != t.SeatID) {
			continue
		}
		r := seatReason(t, *s, free[s.ID], class, now, p)
		if r != "" {
			reason = moreSpecific(reason, r)
			continue
		}
		if best == nil || better(*s, *best, free) {
			best = s
		}
	}
	if best == nil {
		return Seat{}, reason
	}
	return *best, ""
}

func seatReason(t Task, s Seat, freeSlots int, class Class, now time.Time, p Policy) string {
	if !hasLabels(s.Labels, t.RequiredLabels) {
		return ReasonLabels
	}
	if s.State != seatOnline || freeSlots <= 0 {
		return ReasonFull
	}
	if remainingBelow(s.Quota5hPct, p.EstimatedCost5hPct) || remainingBelow(s.QuotaWeekPct, p.EstimatedCostWeekPct) {
		return ReasonQuota
	}
	if class.Paced && aheadOfPace(s, now, class.PaceSlackPct) {
		return ReasonPacing
	}
	return ""
}

// reasonRank orders reasons from least to most specific, so a task reports
// the closest it came to starting.
var reasonRank = map[string]int{ReasonNoSeat: 0, ReasonLabels: 1, ReasonFull: 2, ReasonQuota: 3, ReasonPacing: 4}

func moreSpecific(a, b string) string {
	if reasonRank[b] > reasonRank[a] {
		return b
	}
	return a
}

// better prefers more free slots, then lower weekly usage, then lower 5-hour
// usage, then the lower seat ID for a stable plan.
func better(a, b Seat, free map[string]int) bool {
	if free[a.ID] != free[b.ID] {
		return free[a.ID] > free[b.ID]
	}
	if wa, wb := pctOr(a.QuotaWeekPct), pctOr(b.QuotaWeekPct); wa != wb {
		return wa < wb
	}
	if ha, hb := pctOr(a.Quota5hPct), pctOr(b.Quota5hPct); ha != hb {
		return ha < hb
	}
	return a.ID < b.ID
}

func pctOr(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

func remainingBelow(usedPct *float64, cost float64) bool {
	return usedPct != nil && 100-*usedPct < cost
}

// aheadOfPace reports whether weekly usage exceeds an even spend of the
// window elapsed so far plus slack. Unknown usage or window never blocks.
func aheadOfPace(s Seat, now time.Time, slackPct float64) bool {
	if s.QuotaWeekPct == nil || s.ResetWeekAt == nil {
		return false
	}
	start := s.ResetWeekAt.Add(-Week)
	elapsed := now.Sub(start)
	if elapsed <= 0 || elapsed >= Week {
		return false
	}
	target := 100 * elapsed.Hours() / Week.Hours()
	return *s.QuotaWeekPct > target+slackPct
}

func hasLabels(have, want []string) bool {
	set := make(map[string]bool, len(have))
	for _, l := range have {
		set[l] = true
	}
	for _, l := range want {
		if !set[l] {
			return false
		}
	}
	return true
}
