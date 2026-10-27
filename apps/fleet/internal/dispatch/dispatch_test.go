package dispatch

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/kandev/kandev/apps/fleet/internal/kandev"
	"github.com/kandev/kandev/apps/fleet/internal/store"
)

var t0 = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

type fakeTasks struct {
	tasks    []kandev.Task
	resets   []string
	resetErr error
	// steps maps a workflow step ID to whether it starts the agent on enter.
	steps     map[string]bool
	stepReads int
}

func (f *fakeTasks) GetWorkflowStep(_ context.Context, id string) (kandev.WorkflowStep, error) {
	f.stepReads++
	starts, ok := f.steps[id]
	if !ok {
		return kandev.WorkflowStep{}, &kandev.APIError{Status: 404, Message: "Step not found"}
	}
	step := kandev.WorkflowStep{ID: id}
	if starts {
		step.Events.OnEnter = []kandev.StepAction{{Type: kandev.OnEnterAutoStartAgent}}
	}
	return step, nil
}

func (f *fakeTasks) ResetTaskEnvironment(_ context.Context, taskID string) error {
	f.resets = append(f.resets, taskID)
	return f.resetErr
}

func (f *fakeTasks) ListWorkspaceTasks(_ context.Context, ws string, opts kandev.ListTasksOptions) (kandev.TaskPage, error) {
	if ws != "ws-1" {
		return kandev.TaskPage{}, errors.New("wrong workspace")
	}
	start := (opts.Page - 1) * opts.PageSize
	if start >= len(f.tasks) {
		return kandev.TaskPage{Total: len(f.tasks)}, nil
	}
	end := start + opts.PageSize
	if end > len(f.tasks) {
		end = len(f.tasks)
	}
	return kandev.TaskPage{Tasks: f.tasks[start:end], Total: len(f.tasks)}, nil
}

type fakeLauncher struct {
	mu      sync.Mutex
	calls   []kandev.LaunchSessionInput
	err     error
	primary []string
	stopped []string
}

func (f *fakeLauncher) StopSession(_ context.Context, sessionID, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped = append(f.stopped, sessionID)
	return nil
}

func (f *fakeLauncher) SetPrimarySession(_ context.Context, sessionID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.primary = append(f.primary, sessionID)
	return nil
}

func (f *fakeLauncher) LaunchSession(_ context.Context, in kandev.LaunchSessionInput) (kandev.LaunchSessionResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, in)
	if f.err != nil {
		return kandev.LaunchSessionResult{}, f.err
	}
	return kandev.LaunchSessionResult{Success: true, TaskID: in.TaskID, SessionID: "sess-" + in.TaskID, State: "STARTING"}, nil
}

func newFixture(t *testing.T, seats ...store.Seat) (*store.Store, *fakeTasks, *fakeLauncher, *Dispatcher, *time.Time) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "fleet.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	for _, seat := range seats {
		if err := st.CreateToken(context.Background(), "tok-"+seat.ID, store.RegistrationToken{OwnerUserID: seat.OwnerUserID, ExpiresAt: t0.Add(time.Hour)}, t0); err != nil {
			t.Fatal(err)
		}
		if err := st.RegisterSeat(context.Background(), "tok-"+seat.ID, seat, t0); err != nil {
			t.Fatal(err)
		}
		if err := st.ApplyHeartbeat(context.Background(), seat.ID, store.HeartbeatUpdate{State: seat.State, FreeSlots: seat.Capacity, SignedAt: 1}, t0); err != nil {
			t.Fatal(err)
		}
	}
	tasks := &fakeTasks{}
	launcher := &fakeLauncher{}
	d := New(Config{WorkspaceID: "ws-1", IntakeStepIDs: []string{"step-intake"}, AgentProfileID: "agent-prof", MaxAttempts: 2, RetryBackoff: time.Minute}, st, tasks, launcher,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	clock := t0.Add(time.Minute)
	d.now = func() time.Time { return clock }
	return st, tasks, launcher, d, &clock
}

func seatFor(id, owner string, capacity int) store.Seat {
	return store.Seat{ID: id, OwnerUserID: owner, SSHHost: "mini-" + id, SSHPort: 22, SSHUser: "seat", HostKeyFingerprint: "SHA256:x",
		Capacity: capacity, KandevExecutorProfileID: "prof-" + id, HMACSecret: "s", State: store.SeatOnline}
}

func intake(id, owner, priority string) kandev.Task {
	return kandev.Task{ID: id, Title: "title " + id, WorkflowStepID: "step-intake", AssigneeUserID: owner, Priority: priority, CreatedAt: t0}
}

func TestRoundLaunchesEligibleTasksOnOwnerSeat(t *testing.T) {
	st, tasks, launcher, d, _ := newFixture(t, seatFor("a1", "user-a", 2), seatFor("b1", "user-b", 1))
	started := "sess-old"
	tasks.tasks = []kandev.Task{
		intake("t-a", "user-a", "high"),
		intake("t-b", "user-b", ""),
		{ID: "t-elsewhere", WorkflowStepID: "step-review", AssigneeUserID: "user-a", CreatedAt: t0},
		{ID: "t-unassigned", WorkflowStepID: "step-intake", CreatedAt: t0},
		{ID: "t-started", WorkflowStepID: "step-intake", AssigneeUserID: "user-a", PrimarySessionID: &started, CreatedAt: t0},
	}
	if err := d.Round(context.Background()); err != nil {
		t.Fatalf("Round: %v", err)
	}
	got := map[string]string{}
	for _, c := range launcher.calls {
		got[c.TaskID] = c.ExecutorProfileID
		if c.Intent != kandev.IntentStart || c.AgentProfileID != "agent-prof" {
			t.Fatalf("launch = %+v; want intent start with the configured agent profile", c)
		}
	}
	if len(got) != 2 || got["t-a"] != "prof-a1" || got["t-b"] != "prof-b1" {
		t.Fatalf("launches = %v", got)
	}
	if launcher.calls[0].TaskID != "t-a" || launcher.calls[0].Priority != "high" {
		t.Fatalf("higher priority must launch first: %+v", launcher.calls)
	}
	claims, _ := st.ListClaims(context.Background())
	if claims["t-a"].State != store.ClaimLaunched || claims["t-a"].SessionID != "sess-t-a" {
		t.Fatalf("claim = %+v", claims["t-a"])
	}
	stats := d.LastRound()
	if stats.Launched != 2 || stats.Queued["high"] != 1 || stats.Queued["medium"] != 1 {
		t.Fatalf("stats = %+v", stats)
	}

	if err := d.Round(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(launcher.calls) != 2 {
		t.Fatalf("a launched task must not launch again: %d calls", len(launcher.calls))
	}
}

func TestUnconfirmedLaunchesHoldTheSlot(t *testing.T) {
	_, tasks, launcher, d, _ := newFixture(t, seatFor("a1", "user-a", 1))
	tasks.tasks = []kandev.Task{intake("t-1", "user-a", "high"), intake("t-2", "user-a", "high")}
	if err := d.Round(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := d.Round(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(launcher.calls) != 1 {
		t.Fatalf("calls = %d; the seat's only slot is taken until its heartbeat catches up", len(launcher.calls))
	}
	last := d.LastRound()
	if last.Waiting["high"]["seats_full"] != 1 {
		t.Fatalf("waiting = %+v", last.Waiting)
	}
	if len(last.WaitingTasks) != 1 || last.WaitingTasks[0].TaskID != "t-2" || last.WaitingTasks[0].Reason != "seats_full" ||
		last.WaitingTasks[0].AssigneeUserID != "user-a" || last.WaitingTasks[0].Title != "title t-2" || last.Titles["t-1"] != "title t-1" {
		t.Fatalf("waiting tasks = %+v, titles = %v", last.WaitingTasks, last.Titles)
	}
}

func TestFailedLaunchRetriesWithBackoffThenStops(t *testing.T) {
	st, tasks, launcher, d, clock := newFixture(t, seatFor("a1", "user-a", 1))
	tasks.tasks = []kandev.Task{intake("t-1", "user-a", "critical")}
	launcher.err = errors.New("kandev: websocket closed")
	if err := d.Round(context.Background()); err != nil {
		t.Fatal(err)
	}
	if c, _ := st.ListClaims(context.Background()); c["t-1"].State != store.ClaimFailed || c["t-1"].LastError == "" {
		t.Fatalf("claim after failure = %+v", c["t-1"])
	}
	*clock = clock.Add(30 * time.Second)
	if err := d.Round(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(launcher.calls) != 1 {
		t.Fatal("a failed task waits out its backoff")
	}
	*clock = clock.Add(2 * time.Minute)
	if err := d.Round(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(launcher.calls) != 2 {
		t.Fatalf("calls = %d; retry after the backoff", len(launcher.calls))
	}
	*clock = clock.Add(time.Hour)
	if err := d.Round(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(launcher.calls) != 2 || d.LastRound().Exhausted != 1 {
		t.Fatalf("calls = %d, exhausted = %d; MaxAttempts=2 stops retries", len(launcher.calls), d.LastRound().Exhausted)
	}
	if launched, failed := d.Totals(); launched != 0 || failed != 2 {
		t.Fatalf("totals = %d launched, %d failed", launched, failed)
	}
}

func TestRoundRecoversClaimsLeftLaunching(t *testing.T) {
	st, tasks, launcher, d, clock := newFixture(t, seatFor("a1", "user-a", 1))
	tasks.tasks = []kandev.Task{intake("t-1", "user-a", "high")}
	if _, err := st.BeginClaim(context.Background(), "t-1", "a1", t0); err != nil {
		t.Fatal(err)
	}
	*clock = t0.Add(10 * time.Minute)
	if err := d.Round(context.Background()); err != nil {
		t.Fatal(err)
	}
	*clock = clock.Add(5 * time.Minute)
	if err := d.Round(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(launcher.calls) != 1 {
		t.Fatalf("a claim stranded by a crash must be retried, calls = %d", len(launcher.calls))
	}
}

func TestPagination(t *testing.T) {
	_, tasks, launcher, d, _ := newFixture(t, seatFor("a1", "user-a", 8))
	for i := 0; i < 230; i++ {
		tasks.tasks = append(tasks.tasks, kandev.Task{ID: "other", WorkflowStepID: "step-done", CreatedAt: t0})
	}
	tasks.tasks = append(tasks.tasks, intake("t-last", "user-a", "low"))
	if err := d.Round(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(launcher.calls) != 1 || launcher.calls[0].TaskID != "t-last" {
		t.Fatalf("a task on page 3 must be found: %+v", launcher.calls)
	}
}
