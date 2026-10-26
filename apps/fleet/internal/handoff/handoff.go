// Package handoff moves a task blocked by its seat's usage limit to another
// member's seat once that member accepts the offer (plan D7, D8, D9).
package handoff

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/kandev/kandev/apps/fleet/internal/kandev"
	"github.com/kandev/kandev/apps/fleet/internal/store"
)

// Kandev is the part of the Kandev client handoffs need.
type Kandev interface {
	GetTask(ctx context.Context, id string) (kandev.Task, error)
	SetTaskAssignee(ctx context.Context, taskID, userID string) (kandev.Task, error)
}

// Launcher starts Kandev sessions.
type Launcher interface {
	LaunchSession(ctx context.Context, in kandev.LaunchSessionInput) (kandev.LaunchSessionResult, error)
}

// Handoff paths recorded on a done handoff.
const (
	PathResume = "resume" // a new session of the same task on the new seat
)

// Errors for a member's accept request.
var (
	ErrSeatUnavailable = errors.New("that seat cannot take work now (it must be online with a free slot)")
	ErrSameOwner       = errors.New("the task already belongs to that seat's owner")
)

// KandevError wraps a failed Kandev call during a move.
type KandevError struct{ Err error }

func (e *KandevError) Error() string { return "kandev: " + e.Err.Error() }
func (e *KandevError) Unwrap() error { return e.Err }

// Service accepts handoffs.
type Service struct {
	store    *store.Store
	kandev   Kandev
	launcher Launcher
	log      *slog.Logger
	now      func() time.Time
}

// New returns a handoff service.
func New(st *store.Store, k Kandev, l Launcher, log *slog.Logger) *Service {
	return &Service{store: st, kandev: k, launcher: l, log: log, now: time.Now}
}

// CheckpointPrompt is the first message of a session that continues a task
// moved from another seat. It works whether or not the previous seat left a
// WIP ref, so a move behaves like a resume from the last checkpoint.
func CheckpointPrompt(taskID string) string {
	return fmt.Sprintf(`This task was moved to you from another member's seat because that seat hit its usage limit.
Before changing anything:
1. Run: git fetch origin refs/agent-wip/%[1]s && git checkout -B agent-wip/%[1]s FETCH_HEAD
   If that ref does not exist, start from the task's branch as usual.
2. Read PROGRESS.md and any task document named "handoff" if present.
3. Run the project's tests to see the current state.
4. Continue from the recorded next steps. Do not redo finished work.
Keep PROGRESS.md updated as you go.`, taskID)
}

// Accept lets the owner of toSeatID take the handoff: the task is assigned to
// them and started on their seat. On failure the task returns to its
// original assignee and the handoff is marked failed so it can be offered again.
func (s *Service) Accept(ctx context.Context, handoffID, toSeatID string) (store.Handoff, error) {
	h, err := s.store.GetHandoff(ctx, handoffID)
	if err != nil {
		return store.Handoff{}, err
	}
	to, from, err := s.seats(ctx, toSeatID, h.FromSeatID)
	if err != nil {
		return store.Handoff{}, err
	}
	if to.OwnerUserID == from.OwnerUserID {
		return store.Handoff{}, ErrSameOwner
	}
	if err := s.store.AcceptHandoff(ctx, handoffID, toSeatID, s.now()); err != nil {
		return store.Handoff{}, err
	}
	sessionID, moveErr := s.move(ctx, h.TaskID, to, from)
	ctx = context.WithoutCancel(ctx)
	if moveErr != nil {
		if err := s.store.FinishHandoff(ctx, handoffID, store.HandoffFailed, "", s.now()); err != nil {
			s.log.Error("recording a failed handoff failed", "handoff_id", handoffID, "error", err)
		}
		s.log.Warn("handoff failed", "handoff_id", handoffID, "task_id", h.TaskID, "to_seat_id", toSeatID, "error", moveErr)
		return store.Handoff{}, moveErr
	}
	if err := s.store.MoveClaim(ctx, h.TaskID, toSeatID, sessionID, s.now()); err != nil {
		s.log.Error("moving the dispatch claim failed", "task_id", h.TaskID, "error", err)
	}
	if err := s.store.FinishHandoff(ctx, handoffID, store.HandoffDone, PathResume, s.now()); err != nil {
		return store.Handoff{}, err
	}
	s.log.Info("handoff done", "handoff_id", handoffID, "task_id", h.TaskID, "from_seat_id", from.ID,
		"to_seat_id", to.ID, "to_owner_user_id", to.OwnerUserID, "session_id", sessionID, "path", PathResume)
	return s.store.GetHandoff(ctx, handoffID)
}

func (s *Service) seats(ctx context.Context, toID, fromID string) (store.Seat, store.Seat, error) {
	to, err := s.store.GetSeat(ctx, toID)
	if err != nil {
		return store.Seat{}, store.Seat{}, err
	}
	free, err := s.store.FreeSlots(ctx, toID)
	if err != nil {
		return store.Seat{}, store.Seat{}, err
	}
	if to.State != store.SeatOnline || free < 1 {
		return store.Seat{}, store.Seat{}, ErrSeatUnavailable
	}
	from, err := s.store.GetSeat(ctx, fromID)
	if err != nil {
		return store.Seat{}, store.Seat{}, err
	}
	return to, from, nil
}

// move reassigns the task and starts it on the new seat, restoring the
// previous assignee if the launch fails.
func (s *Service) move(ctx context.Context, taskID string, to, from store.Seat) (string, error) {
	task, err := s.kandev.GetTask(ctx, taskID)
	if err != nil {
		return "", &KandevError{Err: err}
	}
	previous := task.AssigneeUserID
	if previous == "" {
		previous = from.OwnerUserID
	}
	if _, err := s.kandev.SetTaskAssignee(ctx, taskID, to.OwnerUserID); err != nil {
		return "", &KandevError{Err: err}
	}
	res, err := s.launcher.LaunchSession(ctx, kandev.LaunchSessionInput{
		TaskID: taskID, Intent: kandev.IntentStart, ExecutorProfileID: to.KandevExecutorProfileID,
		Prompt: CheckpointPrompt(taskID), Priority: task.Priority,
	})
	if err == nil && !res.Success {
		err = errors.New("session.launch returned success=false")
	}
	if err != nil {
		if _, rerr := s.kandev.SetTaskAssignee(context.WithoutCancel(ctx), taskID, previous); rerr != nil {
			s.log.Error("restoring the assignee after a failed handoff failed; fix it in Kandev",
				"task_id", taskID, "assignee_user_id", previous, "error", rerr)
		}
		return "", &KandevError{Err: err}
	}
	return res.SessionID, nil
}
