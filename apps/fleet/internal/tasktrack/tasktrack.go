// Package tasktrack follows the tasks fleetd launched until Kandev reports
// them ended, so /metrics can show each task's state and run time. It frees
// the seat of a task waiting in REVIEW when another task needs it, asks a
// session stopped by its seat's usage limit to continue once the limit
// resets, and hands a session that failed mid-run back to the dispatcher.
package tasktrack

import (
	"context"
	"log/slog"
	"regexp"
	"sort"
	"time"

	"github.com/kandev/kandev/apps/fleet/internal/kandev"
	"github.com/kandev/kandev/apps/fleet/internal/store"
)

// Tasks reads Kandev tasks and session messages.
type Tasks interface {
	GetTask(ctx context.Context, id string) (kandev.Task, error)
	ListRecentMessages(ctx context.Context, sessionID string, limit int) ([]kandev.SessionMessage, error)
}

// StateDeleted is recorded for a launched task Kandev no longer returns.
const StateDeleted = "DELETED"

// Sessions stops Kandev sessions and sends them messages.
type Sessions interface {
	StopSession(ctx context.Context, sessionID, reason string) error
	AddMessage(ctx context.Context, taskID, sessionID, content string) error
}

// Reasons recorded on sessions fleetd stops.
const (
	stopReason    = "fleetd: task finished, freeing the seat"
	releaseReason = "fleetd: freeing the seat for a queued task while this task waits in review"
	failedReason  = "fleetd: the session failed; the task will be retried"
)

// Errors recorded on claims whose session failed mid-run.
const (
	errLimitFailed   = "the session stopped at the seat's usage limit"
	errSessionFailed = "the session failed mid-run"
)

// ResumePrompt is what fleetd sends a session paused at its seat's usage
// limit once the limit resets.
const ResumePrompt = "The usage limit of this seat has reset. Continue the task from where you stopped. " +
	"Do not redo finished work."

// limitPattern matches what Claude Code reports when a subscription reaches
// its 5-hour or weekly limit.
var limitPattern = regexp.MustCompile(`(?i)(?:you['’]ve|you have) hit your (?:session|weekly|usage|5-hour) limit|` +
	`\b(?:usage|session|weekly|5-hour|opus) limit reached`)

// recentMessages is how many of a stopped session's newest messages are
// searched for a usage-limit report.
const recentMessages = 5

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
// same session in a later step. FAILED is not ended either: fleetd retries
// a session that failed mid-run.
func ended(state string) bool {
	switch state {
	case kandev.TaskStateCompleted, kandev.TaskStateCancelled, StateDeleted:
		return true
	default:
		return false
	}
}

// Track records the state of every launched claim not yet seen ended.
// listed holds the tasks the dispatch round already read from Kandev; a
// claim missing from it is read on its own. A task that cannot be read is
// retried on the next round.
//
// When a task changes state, Track also decides what its stop means: a task
// in REVIEW whose session reported the seat's usage limit is paused until
// the limit resets; a FAILED task, or one in REVIEW whose session failed, is
// handed back to the dispatcher for a retry on its seat; a task that moves
// on by itself is no longer paused.
func (t *Tracker) Track(ctx context.Context, claims map[string]store.Claim, listed map[string]kandev.Task, now time.Time) error {
	for id, c := range claims {
		if c.State != store.ClaimLaunched || c.FinishedAt != nil {
			continue
		}
		task, err := t.task(ctx, id, listed)
		if err != nil {
			t.log.Warn("task state unavailable", "task_id", id, "error", err)
			continue
		}
		state := task.State
		finished := ended(state)
		if state == c.TaskState && !finished {
			continue
		}
		if state == kandev.TaskStateFailed || (state == kandev.TaskStateReview && sessionFailed(task, c)) {
			if err := t.fail(ctx, c, state, now); err != nil {
				return err
			}
			continue
		}
		if err := t.store.RecordTaskState(ctx, id, state, finished, now); err != nil {
			return err
		}
		switch {
		case finished:
			t.log.Info("task finished", "task_id", id, "state", state)
			t.stopSession(ctx, c, state)
		case state == kandev.TaskStateReview:
			if err := t.pauseAtLimit(ctx, c, now); err != nil {
				return err
			}
		case c.PausedAt != nil:
			if err := t.store.UnpauseClaim(ctx, id); err != nil {
				return err
			}
		}
	}
	return nil
}

// sessionFailed reports whether the task's primary session is the claim's
// and failed.
func sessionFailed(task kandev.Task, c store.Claim) bool {
	return task.PrimarySessionID != nil && *task.PrimarySessionID == c.SessionID &&
		task.PrimarySessionIs(kandev.SessionStateFailed)
}

// fail hands a task whose session failed mid-run back to the dispatcher,
// which retries it on the claim's seat within its attempt budget.
func (t *Tracker) fail(ctx context.Context, c store.Claim, state string, now time.Time) error {
	reason := errSessionFailed
	if t.hitLimit(ctx, c) {
		reason = errLimitFailed
	}
	changed, err := t.store.FailRunningClaim(ctx, c.TaskID, state, reason, now)
	if err != nil || !changed {
		return err
	}
	t.log.Warn("task session failed; it will be retried on its seat", "task_id", c.TaskID, "seat_id", c.SeatID,
		"session_id", c.SessionID, "state", state, "reason", reason, "attempt", c.Attempt)
	// The agentctl of a failed session can stay up and hold a slot.
	if err := t.sessions.StopSession(ctx, c.SessionID, failedReason); err != nil {
		t.log.Debug("could not stop a failed session", "task_id", c.TaskID, "session_id", c.SessionID, "error", err)
	}
	return nil
}

// pauseAtLimit pauses a task that stopped in REVIEW because its seat hit
// its usage limit, so Resume continues it after the reset.
func (t *Tracker) pauseAtLimit(ctx context.Context, c store.Claim, now time.Time) error {
	if c.PausedAt != nil || !t.hitLimit(ctx, c) {
		return nil
	}
	paused, err := t.store.PauseClaim(ctx, c.TaskID, now)
	if err != nil || !paused {
		return err
	}
	t.log.Info("task paused at its seat's usage limit", "task_id", c.TaskID, "seat_id", c.SeatID, "session_id", c.SessionID)
	return nil
}

// hitLimit reports whether the claim's session answered its last prompt
// with a usage-limit report: an agent message newer than the newest user
// message. A session that cannot be read counts as not limited.
func (t *Tracker) hitLimit(ctx context.Context, c store.Claim) bool {
	if c.SessionID == "" {
		return false
	}
	msgs, err := t.tasks.ListRecentMessages(ctx, c.SessionID, recentMessages)
	if err != nil {
		t.log.Warn("cannot read a stopped session's messages", "task_id", c.TaskID, "session_id", c.SessionID, "error", err)
		return false
	}
	for _, m := range msgs {
		if m.AuthorType == kandev.MessageAuthorUser {
			return false
		}
		if limitPattern.MatchString(m.Content) {
			return true
		}
	}
	return false
}

// Resume asks every paused session whose seat takes work again (its usage
// limit reset) to continue. A failed message is retried on the next round.
func (t *Tracker) Resume(ctx context.Context, now time.Time) error {
	claims, err := t.store.ListClaims(ctx)
	if err != nil {
		return err
	}
	var seats map[string]string
	for _, c := range claims {
		if c.State != store.ClaimLaunched || c.FinishedAt != nil || c.PausedAt == nil {
			continue
		}
		if seats == nil {
			if seats, err = t.seatStates(ctx); err != nil {
				return err
			}
		}
		if s := seats[c.SeatID]; s != store.SeatOnline && s != store.SeatBusy {
			continue
		}
		if err := t.sessions.AddMessage(ctx, c.TaskID, c.SessionID, ResumePrompt); err != nil {
			t.log.Warn("could not resume a paused session", "task_id", c.TaskID, "session_id", c.SessionID, "error", err)
			continue
		}
		if err := t.store.ResumeClaim(ctx, c.TaskID, kandev.TaskStateInProgress, now); err != nil {
			return err
		}
		t.log.Info("resumed a task after its seat's usage limit reset", "task_id", c.TaskID, "seat_id", c.SeatID,
			"session_id", c.SessionID)
	}
	return nil
}

func (t *Tracker) seatStates(ctx context.Context) (map[string]string, error) {
	seats, err := t.store.ListSeats(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(seats))
	for _, s := range seats {
		out[s.ID] = s.State
	}
	return out, nil
}

// Release frees seats for queued tasks: for each owner in demand (the number
// of their tasks waiting because their seats are full) it stops that many
// sessions of tasks in REVIEW for at least ReviewGrace, oldest first, on the
// owner's seats in seatOwners (seat ID to owner, only seats that can take
// work). It records each released task's workflow step from listed, so the
// dispatcher can launch it again when its workflow continues.
func (t *Tracker) Release(ctx context.Context, demand map[string]int, seatOwners map[string]string,
	listed map[string]kandev.Task, now time.Time) error {
	if len(demand) == 0 {
		return nil
	}
	claims, err := t.store.ListClaims(ctx)
	if err != nil {
		return err
	}
	var idle []store.Claim
	for _, c := range claims {
		if c.State != store.ClaimLaunched || c.FinishedAt != nil || c.SessionID == "" || c.PausedAt != nil ||
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
		if _, err := t.store.ReleaseClaim(ctx, c.TaskID, listed[c.TaskID].WorkflowStepID, now); err != nil {
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

func (t *Tracker) task(ctx context.Context, id string, listed map[string]kandev.Task) (kandev.Task, error) {
	if task, ok := listed[id]; ok {
		return task, nil
	}
	task, err := t.tasks.GetTask(ctx, id)
	if kandev.IsNotFound(err) {
		return kandev.Task{ID: id, State: StateDeleted}, nil
	}
	return task, err
}
