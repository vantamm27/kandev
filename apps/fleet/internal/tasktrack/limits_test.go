package tasktrack

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/kandev/kandev/apps/fleet/internal/kandev"
	"github.com/kandev/kandev/apps/fleet/internal/store"
)

func newTracker(st *store.Store, sessions *fakeSessions) *Tracker {
	return New(st, fakeTasks{}, sessions, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func withMessages(t *testing.T, m map[string][]kandev.SessionMessage) {
	t.Helper()
	messagesOf = m
	t.Cleanup(func() { messagesOf = nil })
}

func agentSaid(text string) []kandev.SessionMessage {
	return []kandev.SessionMessage{{AuthorType: "agent", Type: "message", Content: text}}
}

func primary(task kandev.Task, sessionID, state string) kandev.Task {
	task.PrimarySessionID, task.PrimarySessionState = &sessionID, &state
	return task
}

func track(t *testing.T, tr *Tracker, st *store.Store, listed map[string]kandev.Task, at time.Time) map[string]store.Claim {
	t.Helper()
	ctx := context.Background()
	claims, err := st.ListClaims(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := tr.Track(ctx, claims, listed, at); err != nil {
		t.Fatalf("track: %v", err)
	}
	claims, _ = st.ListClaims(ctx)
	return claims
}

func setSeatState(t *testing.T, st *store.Store, state string, signed int64) {
	t.Helper()
	if err := st.ApplyHeartbeat(context.Background(), "seat_1", store.HeartbeatUpdate{State: state, FreeSlots: 0, SignedAt: signed}, now); err != nil {
		t.Fatal(err)
	}
}

func TestLimitReportPausesTheTaskAndResetResumesIt(t *testing.T) {
	st := launchedStore(t, "task-limit", "task-done")
	withMessages(t, map[string][]kandev.SessionMessage{
		"session-task-limit": agentSaid("You've hit your session limit · resets 3pm (UTC)"),
		"session-task-done":  agentSaid("All tests pass. The usage limit section of the README is updated."),
	})
	sessions := &fakeSessions{}
	tr := newTracker(st, sessions)
	review := map[string]kandev.Task{
		"task-limit": primary(kandev.Task{ID: "task-limit", State: "REVIEW"}, "session-task-limit", "WAITING_FOR_INPUT"),
		"task-done":  primary(kandev.Task{ID: "task-done", State: "REVIEW"}, "session-task-done", "WAITING_FOR_INPUT"),
	}
	claims := track(t, tr, st, review, now.Add(time.Minute))
	if claims["task-limit"].PausedAt == nil || claims["task-done"].PausedAt != nil {
		t.Fatalf("only the session that reported the limit pauses: %+v / %+v", claims["task-limit"], claims["task-done"])
	}

	setSeatState(t, st, store.SeatCooldown5h, 2)
	if err := tr.Resume(context.Background(), now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if len(sessions.sent) != 0 {
		t.Fatalf("sent = %v; nothing is resumed while the seat cools down", sessions.sent)
	}
	if err := tr.Release(context.Background(), map[string]int{"user-a": 2}, map[string]string{"seat_1": "user-a"}, review, now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if strings.Join(sessions.stopped, ",") != "session-task-done" {
		t.Fatalf("stopped = %v; a paused session keeps its seat for the resume", sessions.stopped)
	}

	setSeatState(t, st, store.SeatOnline, 3)
	resumedAt := now.Add(5 * time.Hour)
	if err := tr.Resume(context.Background(), resumedAt); err != nil {
		t.Fatal(err)
	}
	if len(sessions.sent) != 1 || sessions.sent[0] != "task-limit/session-task-limit: "+ResumePrompt {
		t.Fatalf("sent = %v; the paused session must be asked to continue", sessions.sent)
	}
	claims, _ = st.ListClaims(context.Background())
	if c := claims["task-limit"]; c.PausedAt != nil || c.TaskState != "IN_PROGRESS" || !c.TaskStateAt.Equal(resumedAt) {
		t.Fatalf("resumed claim = %+v", c)
	}
	if err := tr.Resume(context.Background(), resumedAt.Add(time.Minute)); err != nil || len(sessions.sent) != 1 {
		t.Fatalf("a resumed task is asked once: %v, %v", sessions.sent, err)
	}
}

func TestAPausedTaskThatGoesOnIsNoLongerPaused(t *testing.T) {
	st := launchedStore(t, "task-1")
	withMessages(t, map[string][]kandev.SessionMessage{"session-task-1": agentSaid("Claude usage limit reached. Your limit will reset at 3pm.")})
	tr := newTracker(st, &fakeSessions{})
	track(t, tr, st, map[string]kandev.Task{"task-1": {ID: "task-1", State: "REVIEW"}}, now.Add(time.Minute))
	claims := track(t, tr, st, map[string]kandev.Task{"task-1": {ID: "task-1", State: "IN_PROGRESS"}}, now.Add(time.Hour))
	if claims["task-1"].PausedAt != nil {
		t.Fatalf("a person continued the task; it is not paused any more: %+v", claims["task-1"])
	}
}

func TestAUserMessageAboutLimitsDoesNotPause(t *testing.T) {
	st := launchedStore(t, "task-1")
	withMessages(t, map[string][]kandev.SessionMessage{"session-task-1": {
		{AuthorType: "user", Content: "What happens when you've hit your session limit?"},
	}})
	claims := track(t, newTracker(st, &fakeSessions{}), st, map[string]kandev.Task{"task-1": {ID: "task-1", State: "REVIEW"}}, now.Add(time.Minute))
	if claims["task-1"].PausedAt != nil {
		t.Fatal("only the agent's own report counts")
	}
}

func TestSessionFailedMidRunIsHandedBackForARetry(t *testing.T) {
	st := launchedStore(t, "task-failed", "task-limit-failed", "task-review-failed")
	withMessages(t, map[string][]kandev.SessionMessage{"session-task-limit-failed": {
		{AuthorType: "agent", Type: "error", Content: "Error: You've hit your weekly limit · resets Oct 30"},
	}})
	sessions := &fakeSessions{}
	claims := track(t, newTracker(st, sessions), st, map[string]kandev.Task{
		"task-failed":        {ID: "task-failed", State: "FAILED"},
		"task-limit-failed":  {ID: "task-limit-failed", State: "FAILED"},
		"task-review-failed": primary(kandev.Task{ID: "task-review-failed", State: "REVIEW"}, "session-task-review-failed", "FAILED"),
	}, now.Add(time.Minute))
	for id, wantErr := range map[string]string{
		"task-failed": errSessionFailed, "task-limit-failed": errLimitFailed, "task-review-failed": errSessionFailed,
	} {
		c := claims[id]
		if c.State != store.ClaimFailed || c.SeatID != "seat_1" || c.SessionID != "session-"+id || c.LastError != wantErr {
			t.Fatalf("%s claim = %+v; want failed on its seat with %q", id, c, wantErr)
		}
	}
	if len(sessions.stopped) != 3 {
		t.Fatalf("stopped = %v; a failed session's agentctl is stopped", sessions.stopped)
	}
}

// After fleetd asks a paused session to continue, the old limit report is
// older than that request and must not pause the task again.
func TestAnOlderLimitReportDoesNotPauseAgain(t *testing.T) {
	st := launchedStore(t, "task-1")
	withMessages(t, map[string][]kandev.SessionMessage{"session-task-1": {
		{AuthorType: "user", Content: ResumePrompt},
		{AuthorType: "agent", Content: "You've hit your session limit · resets 3pm"},
	}})
	claims := track(t, newTracker(st, &fakeSessions{}), st, map[string]kandev.Task{"task-1": {ID: "task-1", State: "REVIEW"}}, now.Add(time.Minute))
	if claims["task-1"].PausedAt != nil {
		t.Fatal("only a limit report newer than the last prompt pauses the task")
	}
}
