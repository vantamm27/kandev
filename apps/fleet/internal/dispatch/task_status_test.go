package dispatch

import (
	"context"
	"testing"
	"time"

	"github.com/kandev/kandev/apps/fleet/internal/kandev"
	"github.com/kandev/kandev/apps/fleet/internal/store"
)

func launched(t *testing.T, st *store.Store, taskID, seatID string) {
	t.Helper()
	ctx := context.Background()
	if _, err := st.BeginClaim(ctx, taskID, seatID, t0); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishClaim(ctx, taskID, store.ClaimLaunched, "sess-"+taskID, "", t0); err != nil {
		t.Fatal(err)
	}
}

func coolDown(t *testing.T, st *store.Store, seatID string, resetAt time.Time) {
	t.Helper()
	if err := st.ApplyHeartbeat(context.Background(), seatID, store.HeartbeatUpdate{
		State: store.SeatCooldown5h, Reset5hAt: &resetAt, SignedAt: 2,
	}, t0); err != nil {
		t.Fatal(err)
	}
}

// A session that failed mid-run is retried after the backoff, on the seat
// that holds its environment, even when the owner has an emptier seat.
func TestSessionFailedMidRunIsRetriedOnItsSeat(t *testing.T) {
	st, tasks, launcher, d, clock := newFixture(t, seatFor("a1", "user-a", 1), seatFor("a2", "user-a", 3))
	ctx := context.Background()
	launched(t, st, "t-1", "a1")
	if _, err := st.FailRunningClaim(ctx, "t-1", "FAILED", "the session failed mid-run", *clock); err != nil {
		t.Fatal(err)
	}
	failed := withSession(inState(intake("t-1", "user-a", "high"), "FAILED"), "sess-t-1", "FAILED")
	failed.WorkflowStepID = "step-implement"
	tasks.tasks = []kandev.Task{failed}
	if err := d.Round(ctx); err != nil {
		t.Fatal(err)
	}
	if len(launcher.calls) != 0 {
		t.Fatalf("calls = %+v; the retry waits for its backoff", launcher.calls)
	}
	if st := d.LastRound().Tasks["t-1"]; st.Status != StatusRetrying || st.Attempt != 1 || st.MaxAttempts != 2 || st.RetryAt == nil {
		t.Fatalf("status = %+v", st)
	}
	*clock = clock.Add(2 * time.Minute)
	if err := d.Round(ctx); err != nil {
		t.Fatal(err)
	}
	if len(launcher.calls) != 1 || launcher.calls[0].ExecutorProfileID != "prof-a1" || launcher.calls[0].WorkflowStepID != "step-implement" {
		t.Fatalf("calls = %+v; want a retry on seat a1 in the task's current step", launcher.calls)
	}
}

// A session that failed and left the task in REVIEW is retried too.
func TestSessionFailedInReviewIsRetried(t *testing.T) {
	st, tasks, launcher, d, clock := newFixture(t, seatFor("a1", "user-a", 1))
	ctx := context.Background()
	launched(t, st, "t-1", "a1")
	if _, err := st.FailRunningClaim(ctx, "t-1", "REVIEW", "the session failed mid-run", t0); err != nil {
		t.Fatal(err)
	}
	tasks.tasks = []kandev.Task{withSession(inState(intake("t-1", "user-a", "high"), "REVIEW"), "sess-t-1", "FAILED")}
	*clock = clock.Add(5 * time.Minute)
	if err := d.Round(ctx); err != nil {
		t.Fatal(err)
	}
	if len(launcher.calls) != 1 {
		t.Fatalf("calls = %+v", launcher.calls)
	}
}

func TestTaskStatuses(t *testing.T) {
	st, tasks, _, d, clock := newFixture(t, seatFor("a1", "user-a", 1), seatFor("b1", "user-b", 1))
	ctx := context.Background()
	resetAt := t0.Add(3 * time.Hour)
	launched(t, st, "t-paused", "a1")
	if _, err := st.PauseClaim(ctx, "t-paused", t0); err != nil {
		t.Fatal(err)
	}
	launched(t, st, "t-review", "b1")
	if err := st.RecordTaskState(ctx, "t-review", "REVIEW", false, t0); err != nil {
		t.Fatal(err)
	}
	launched(t, st, "t-out", "b1")
	for i := 0; i < 2; i++ {
		if i > 0 {
			if _, err := st.BeginClaim(ctx, "t-out", "b1", t0); err != nil {
				t.Fatal(err)
			}
			if err := st.FinishClaim(ctx, "t-out", store.ClaimLaunched, "sess-t-out", "", t0); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := st.FailRunningClaim(ctx, "t-out", "FAILED", "the session failed mid-run", t0); err != nil {
			t.Fatal(err)
		}
	}
	launched(t, st, "t-done", "b1")
	if err := st.RecordTaskState(ctx, "t-done", "COMPLETED", true, t0); err != nil {
		t.Fatal(err)
	}
	coolDown(t, st, "a1", resetAt)
	tasks.tasks = []kandev.Task{
		withSession(inState(intake("t-paused", "user-a", "high"), "REVIEW"), "sess-t-paused", "WAITING_FOR_INPUT"),
		withSession(inState(intake("t-review", "user-b", "high"), "REVIEW"), "sess-t-review", "WAITING_FOR_INPUT"),
		withSession(inState(intake("t-out", "user-b", "high"), "FAILED"), "sess-t-out", "FAILED"),
		inState(intake("t-done", "user-b", "high"), "COMPLETED"),
		intake("t-queued", "user-a", "medium"),
	}
	*clock = clock.Add(time.Hour)
	if err := d.Round(ctx); err != nil {
		t.Fatal(err)
	}
	got := d.LastRound().Tasks
	if s := got["t-paused"]; s.Status != StatusPaused || s.SeatID != "a1" || s.ResetAt == nil || !s.ResetAt.Equal(resetAt) {
		t.Errorf("t-paused = %+v", s)
	}
	if s := got["t-review"]; s.Status != StatusReview || s.SeatID != "b1" {
		t.Errorf("t-review = %+v", s)
	}
	if s := got["t-out"]; s.Status != StatusExhausted || s.Attempt != 2 || s.LastError == "" {
		t.Errorf("t-out = %+v", s)
	}
	if s := got["t-queued"]; s.Status != StatusWaiting || s.Reason != "quota_cooldown" || s.ResetAt == nil || !s.ResetAt.Equal(resetAt) {
		t.Errorf("t-queued = %+v", s)
	}
	if _, ok := got["t-done"]; ok {
		t.Errorf("a finished task has no status: %+v", got["t-done"])
	}
}

type assignTasks struct {
	task     kandev.Task
	assigned string
}

func (a *assignTasks) GetTask(_ context.Context, id string) (kandev.Task, error) {
	if id != a.task.ID {
		return kandev.Task{}, &kandev.APIError{Status: 404, Message: "task not found"}
	}
	return a.task, nil
}

func (a *assignTasks) SetTaskAssignee(_ context.Context, _, userID string) (kandev.Task, error) {
	a.assigned = userID
	return a.task, nil
}

func TestAssignCreator(t *testing.T) {
	for name, tc := range map[string]struct {
		task kandev.Task
		want string
	}{
		"new fleet task":   {kandev.Task{ID: "t", WorkspaceID: "ws-1"}, AssignDone},
		"already assigned": {kandev.Task{ID: "t", WorkspaceID: "ws-1", AssigneeUserID: "user-b"}, AssignHasOwner},
		"other workspace":  {kandev.Task{ID: "t", WorkspaceID: "ws-2"}, AssignOtherSpace},
	} {
		t.Run(name, func(t *testing.T) {
			tasks := &assignTasks{task: tc.task}
			got, err := Assigner{WorkspaceID: "ws-1", Tasks: tasks}.AssignCreator(context.Background(), "t", "user-a")
			if err != nil || got != tc.want {
				t.Fatalf("result = %q, %v; want %q", got, err, tc.want)
			}
			if (tasks.assigned == "user-a") != (tc.want == AssignDone) {
				t.Fatalf("assigned = %q", tasks.assigned)
			}
		})
	}
	if _, err := (Assigner{WorkspaceID: "ws-1", Tasks: &assignTasks{}}).AssignCreator(context.Background(), "missing", "user-a"); !kandev.IsNotFound(err) {
		t.Fatalf("missing task: %v", err)
	}
}
