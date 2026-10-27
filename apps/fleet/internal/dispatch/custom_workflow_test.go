package dispatch

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/kandev/kandev/apps/fleet/internal/kandev"
	"github.com/kandev/kandev/apps/fleet/internal/store"
	"github.com/kandev/kandev/apps/fleet/internal/tasktrack"
)

func (f *fakeTasks) GetTask(_ context.Context, id string) (kandev.Task, error) {
	for _, t := range f.tasks {
		if t.ID == id {
			return t, nil
		}
	}
	return kandev.Task{}, &kandev.APIError{Status: 404, Message: "task not found"}
}

func withTracker(d *Dispatcher, st *store.Store, tasks *fakeTasks, launcher *fakeLauncher) {
	tr := tasktrack.New(st, tasks, launcher, slog.New(slog.NewTextHandler(io.Discard, nil)))
	tr.ReviewGrace = 5 * time.Minute
	d.Tracker = tr
}

func heartbeat(t *testing.T, st *store.Store, seatID, state string, free int, at time.Time) {
	t.Helper()
	if err := st.ApplyHeartbeat(context.Background(), seatID, store.HeartbeatUpdate{State: state, FreeSlots: free, SignedAt: at.Unix()}, at); err != nil {
		t.Fatal(err)
	}
}

func inState(t kandev.Task, state string) kandev.Task {
	t.State = state
	return t
}

// A workflow step waiting for a person leaves the task in REVIEW. Its session
// keeps the seat until another task of the same owner needs it.
func TestReviewSessionKeepsItsSeatUntilAnotherTaskWaits(t *testing.T) {
	st, tasks, launcher, d, clock := newFixture(t, seatFor("a1", "user-a", 1))
	withTracker(d, st, tasks, launcher)
	ctx := context.Background()
	tasks.tasks = []kandev.Task{intake("t-plan", "user-a", "high")}
	if err := d.Round(ctx); err != nil {
		t.Fatal(err)
	}
	heartbeat(t, st, "a1", store.SeatBusy, 0, clock.Add(time.Second))
	planned := withSession(inState(intake("t-plan", "user-a", "high"), "REVIEW"), "sess-t-plan", "WAITING_FOR_INPUT")
	planned.WorkflowStepID = "step-approve"
	tasks.tasks = []kandev.Task{planned}
	*clock = clock.Add(time.Hour)
	if err := d.Round(ctx); err != nil {
		t.Fatal(err)
	}
	if len(launcher.stopped) != 0 {
		t.Fatalf("stopped = %v; a task in review with no one waiting keeps its session", launcher.stopped)
	}

	tasks.tasks = []kandev.Task{planned, intake("t-next", "user-a", "medium")}
	*clock = clock.Add(10 * time.Minute)
	if err := d.Round(ctx); err != nil {
		t.Fatal(err)
	}
	if strings.Join(launcher.stopped, ",") != "sess-t-plan" {
		t.Fatalf("stopped = %v; the review session must free the seat for the waiting task", launcher.stopped)
	}
	claims, _ := st.ListClaims(ctx)
	if claims["t-plan"].State != store.ClaimReleased {
		t.Fatalf("t-plan claim = %+v", claims["t-plan"])
	}
	if len(launcher.calls) != 1 {
		t.Fatalf("the waiting task starts only after the seat reports the free slot: %+v", launcher.calls)
	}
	heartbeat(t, st, "a1", store.SeatOnline, 1, clock.Add(time.Second))
	*clock = clock.Add(time.Minute)
	if err := d.Round(ctx); err != nil {
		t.Fatal(err)
	}
	if len(launcher.calls) != 2 || launcher.calls[1].TaskID != "t-next" {
		t.Fatalf("calls = %+v; the waiting task must start on the freed seat", launcher.calls)
	}
}

func releasedFixture(t *testing.T) (*store.Store, *fakeTasks, *fakeLauncher, *Dispatcher, *time.Time) {
	t.Helper()
	st, tasks, launcher, d, clock := newFixture(t, seatFor("a1", "user-a", 1), seatFor("a2", "user-a", 3))
	ctx := context.Background()
	if _, err := st.BeginClaim(ctx, "t-plan", "a1", t0); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishClaim(ctx, "t-plan", store.ClaimLaunched, "sess-plan", "", t0); err != nil {
		t.Fatal(err)
	}
	if ok, err := st.ReleaseClaim(ctx, "t-plan", "step-approve", t0); err != nil || !ok {
		t.Fatalf("release: %v, %v", ok, err)
	}
	return st, tasks, launcher, d, clock
}

// A person approved the plan and moved the task to an agent step: the
// released task runs again on the seat that holds its environment.
func TestReleasedTaskContinuesOnItsSeat(t *testing.T) {
	st, tasks, launcher, d, _ := releasedFixture(t)
	implement := withSession(inState(intake("t-plan", "user-a", "high"), "IN_PROGRESS"), "sess-plan", "CANCELLED")
	implement.WorkflowStepID = "step-implement"
	tasks.tasks = []kandev.Task{implement}
	if err := d.Round(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(launcher.calls) != 1 || launcher.calls[0].TaskID != "t-plan" || launcher.calls[0].ExecutorProfileID != "prof-a1" {
		t.Fatalf("calls = %+v; want a relaunch on seat a1, not the emptier a2", launcher.calls)
	}
	if len(launcher.primary) != 1 || launcher.primary[0] != "sess-t-plan" {
		t.Fatalf("primary = %v; the new session must replace the stopped one", launcher.primary)
	}
	if len(tasks.resets) != 0 {
		t.Fatalf("resets = %v; the environment on the seat must be kept", tasks.resets)
	}
	claims, _ := st.ListClaims(context.Background())
	if c := claims["t-plan"]; c.State != store.ClaimLaunched || c.SeatID != "a1" || c.Attempt != 1 {
		t.Fatalf("claim = %+v", c)
	}
}

func TestReleasedTaskStillInReviewWaits(t *testing.T) {
	_, tasks, launcher, d, _ := releasedFixture(t)
	tasks.steps = map[string]bool{"step-approve": false, "step-done": false}
	review := withSession(inState(intake("t-plan", "user-a", "high"), "REVIEW"), "sess-plan", "CANCELLED")
	for _, step := range []string{"step-approve", "step-done"} {
		review.WorkflowStepID = step
		tasks.tasks = []kandev.Task{review}
		if err := d.Round(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if len(launcher.calls) != 0 {
		t.Fatalf("calls = %+v; a released task in review, in its step or a step without an agent, needs no seat", launcher.calls)
	}
}

// Kandev starts the agent of a task that enters an auto_start_agent step with
// no live session itself, on the task's own executor (Kandev's host), and the
// task stays in REVIEW. Entering such a step is what makes a released task
// fleetd's to run again.
func TestReleasedTaskMovedToAnAgentStepRunsAgain(t *testing.T) {
	_, tasks, launcher, d, _ := releasedFixture(t)
	tasks.steps = map[string]bool{"step-implement": true}
	moved := withSession(inState(intake("t-plan", "user-a", "high"), "REVIEW"), "sess-plan", "CANCELLED")
	moved.WorkflowStepID = "step-implement"
	tasks.tasks = []kandev.Task{moved}
	if err := d.Round(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(launcher.calls) != 1 || launcher.calls[0].ExecutorProfileID != "prof-a1" {
		t.Fatalf("calls = %+v; want a relaunch on seat a1", launcher.calls)
	}
}

// Kandev itself started a session for the released task (for example a
// person replied in the task): fleetd tracks it instead of launching another.
func TestReleasedTaskAdoptsASessionKandevStarted(t *testing.T) {
	st, tasks, launcher, d, _ := releasedFixture(t)
	running := withSession(inState(intake("t-plan", "user-a", "high"), "IN_PROGRESS"), "sess-kandev", "RUNNING")
	tasks.tasks = []kandev.Task{running}
	if err := d.Round(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(launcher.calls) != 0 {
		t.Fatalf("calls = %+v; a task with a live session must not be launched again", launcher.calls)
	}
	claims, _ := st.ListClaims(context.Background())
	if c := claims["t-plan"]; c.State != store.ClaimLaunched || c.SessionID != "sess-kandev" || c.FinishedAt != nil {
		t.Fatalf("claim = %+v; want the Kandev session adopted", c)
	}
}

func TestSeveralIntakeSteps(t *testing.T) {
	_, tasks, launcher, d, _ := newFixture(t, seatFor("a1", "user-a", 3))
	d.cfg.IntakeStepIDs = []string{"step-intake", "step-other-workflow"}
	other := intake("t-other", "user-a", "high")
	other.WorkflowStepID = "step-other-workflow"
	elsewhere := intake("t-elsewhere", "user-a", "high")
	elsewhere.WorkflowStepID = "step-review"
	tasks.tasks = []kandev.Task{intake("t-1", "user-a", "high"), other, elsewhere}
	if err := d.Round(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, c := range launcher.calls {
		got[c.TaskID] = true
	}
	if len(got) != 2 || !got["t-1"] || !got["t-other"] {
		t.Fatalf("launched = %v; want the tasks of both intake steps", got)
	}
}

// Kandev's own start of a released task (on its host, which has no
// subscription login) leaves a session there that built the task environment
// on that host. fleetd stops it and resets the environment, or the seat launch
// fails with "workspace reuse is unsafe".
func TestRelaunchClearsASessionKandevStartedElsewhere(t *testing.T) {
	_, tasks, launcher, d, _ := releasedFixture(t)
	tasks.steps = map[string]bool{"step-implement": true}
	moved := withSession(inState(intake("t-plan", "user-a", "high"), "REVIEW"), "sess-plan", "CANCELLED")
	moved.WorkflowStepID = "step-implement"
	tasks.tasks = []kandev.Task{moved}
	tasks.sessions = map[string][]kandev.TaskSession{"t-plan": {
		{ID: "sess-plan", State: "CANCELLED", ExecutorID: "exec-a1"},
		{ID: "sess-host", State: "WAITING_FOR_INPUT"},
	}}
	if err := d.Round(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Join(launcher.stopped, ",") != "sess-host" || strings.Join(tasks.resets, ",") != "t-plan" {
		t.Fatalf("stopped = %v, resets = %v; want the host session stopped and the environment reset", launcher.stopped, tasks.resets)
	}
	if len(launcher.calls) != 1 || launcher.calls[0].ExecutorProfileID != "prof-a1" {
		t.Fatalf("calls = %+v", launcher.calls)
	}
}

// A relaunch that failed leaves the stopped session primary; fleetd retries
// its own failed launch wherever the task's workflow now is.
func TestFailedRelaunchIsRetried(t *testing.T) {
	_, tasks, launcher, d, clock := releasedFixture(t)
	tasks.steps = map[string]bool{"step-implement": true}
	moved := withSession(inState(intake("t-plan", "user-a", "high"), "REVIEW"), "sess-plan", "CANCELLED")
	moved.WorkflowStepID = "step-implement"
	tasks.tasks = []kandev.Task{moved}
	launcher.err = errors.New("kandev: session.launch: workspace reuse is unsafe")
	if err := d.Round(context.Background()); err != nil {
		t.Fatal(err)
	}
	launcher.err = nil
	failed := inState(moved, "FAILED")
	tasks.tasks = []kandev.Task{failed}
	*clock = clock.Add(2 * time.Minute)
	if err := d.Round(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(launcher.calls) != 2 {
		t.Fatalf("calls = %d; the failed relaunch must be retried", len(launcher.calls))
	}
}
