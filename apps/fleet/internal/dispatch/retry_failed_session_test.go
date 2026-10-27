package dispatch

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kandev/kandev/apps/fleet/internal/kandev"
	"github.com/kandev/kandev/apps/fleet/internal/schedule"
)

func withSession(t kandev.Task, id, state string) kandev.Task {
	t.PrimarySessionID, t.PrimarySessionState = &id, &state
	return t
}

// A launch that Kandev accepted far enough to create a session, then failed
// (for example on SSH), leaves a FAILED primary session behind. fleetd must
// still retry its own failed launch.
func TestRetriesOwnLaunchThatLeftAFailedSession(t *testing.T) {
	_, tasks, launcher, d, clock := newFixture(t, seatFor("a1", "user-a", 1))
	tasks.tasks = []kandev.Task{intake("t-1", "user-a", "high")}
	launcher.err = errors.New("kandev: session.launch: host key changed")
	if err := d.Round(context.Background()); err != nil {
		t.Fatal(err)
	}
	tasks.tasks = []kandev.Task{withSession(intake("t-1", "user-a", "high"), "s-failed", "FAILED")}
	launcher.err = nil
	*clock = clock.Add(2 * time.Minute)
	if err := d.Round(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(launcher.calls) != 2 {
		t.Fatalf("calls = %d; fleetd's failed launch must be retried", len(launcher.calls))
	}
	if len(launcher.primary) != 1 || launcher.primary[0] != "sess-t-1" {
		t.Fatalf("primary = %v; the retried session must replace the failed primary", launcher.primary)
	}
}

func TestFirstLaunchLeavesPrimaryToKandev(t *testing.T) {
	_, tasks, launcher, d, _ := newFixture(t, seatFor("a1", "user-a", 1))
	tasks.tasks = []kandev.Task{intake("t-1", "user-a", "high")}
	if err := d.Round(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(launcher.calls) != 1 || len(launcher.primary) != 0 {
		t.Fatalf("calls = %d, primary = %v", len(launcher.calls), launcher.primary)
	}
}

func TestLeavesSessionsFleetdDidNotStart(t *testing.T) {
	_, tasks, launcher, d, _ := newFixture(t, seatFor("a1", "user-a", 1))
	tasks.tasks = []kandev.Task{
		withSession(intake("t-ui-failed", "user-a", "high"), "s-1", "FAILED"),
		withSession(intake("t-running", "user-a", "high"), "s-2", "RUNNING"),
	}
	if err := d.Round(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(launcher.calls) != 0 {
		t.Fatalf("launches = %+v; a session fleetd did not start is a person's", launcher.calls)
	}
}

// Opening or creating a task in the web UI prepares a session whose
// environment lives on Kandev's own host. fleetd stops that session and resets
// the environment, then launches on a seat and makes its session primary.
func TestTakesOverTasksTheUIPrepared(t *testing.T) {
	_, tasks, launcher, d, _ := newFixture(t, seatFor("a1", "user-a", 1))
	tasks.tasks = []kandev.Task{withSession(intake("t-opened", "user-a", "high"), "s-prepared", "CREATED")}
	if err := d.Round(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(launcher.stopped) != 1 || launcher.stopped[0] != "s-prepared" || len(tasks.resets) != 1 || tasks.resets[0] != "t-opened" {
		t.Fatalf("stopped = %v, resets = %v; the prepared session and environment must be cleared", launcher.stopped, tasks.resets)
	}
	if len(launcher.calls) != 1 || len(launcher.primary) != 1 || launcher.primary[0] != "sess-t-opened" {
		t.Fatalf("calls = %d, primary = %v", len(launcher.calls), launcher.primary)
	}
}

func TestFailedTakeOverIsALaunchFailure(t *testing.T) {
	st, tasks, launcher, d, _ := newFixture(t, seatFor("a1", "user-a", 1))
	tasks.tasks = []kandev.Task{withSession(intake("t-opened", "user-a", "high"), "s-prepared", "CREATED")}
	tasks.resetErr = errors.New("kandev: active session is running on this task")
	if err := d.Round(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(launcher.calls) != 0 {
		t.Fatalf("launches = %d; nothing launches over an environment that was not reset", len(launcher.calls))
	}
	claims, err := st.ListClaims(context.Background())
	if err != nil || claims["t-opened"].State != "failed" {
		t.Fatalf("claim = %+v, %v; the attempt counts as a failed launch", claims["t-opened"], err)
	}
}

// A task created with the fleet agent profile and not started waits for a
// seat in any step. Without an assignee fleetd cannot pick a seat.
func TestFleetProfileTasksWaitForASeatInAnyStep(t *testing.T) {
	_, tasks, launcher, d, _ := newFixture(t, seatFor("a1", "user-a", 1))
	mine := withSession(intake("t-mine", "user-a", "high"), "s-1", "CREATED")
	mine.WorkflowStepID, mine.PrimaryAgentProfileID = "step-progress", "agent-prof"
	nobody := withSession(intake("t-nobody", "", "high"), "s-2", "CREATED")
	nobody.WorkflowStepID, nobody.PrimaryAgentProfileID = "step-progress", "agent-prof"
	other := withSession(intake("t-other-profile", "user-a", "high"), "s-3", "CREATED")
	other.WorkflowStepID, other.PrimaryAgentProfileID = "step-progress", "api-key-profile"
	done := withSession(intake("t-done", "user-a", "high"), "s-4", "CREATED")
	done.WorkflowStepID, done.PrimaryAgentProfileID, done.State = "step-done", "agent-prof", "COMPLETED"
	tasks.tasks = []kandev.Task{mine, nobody, other, done}
	if err := d.Round(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(launcher.calls) != 1 || launcher.calls[0].TaskID != "t-mine" {
		t.Fatalf("launches = %+v; only the assigned, unfinished fleet-profile task runs", launcher.calls)
	}
	last := d.LastRound()
	if len(last.WaitingTasks) != 1 || last.WaitingTasks[0].TaskID != "t-nobody" || last.WaitingTasks[0].Reason != schedule.ReasonNoAssignee {
		t.Fatalf("waiting = %+v", last.WaitingTasks)
	}
}

func TestDoesNotRelaunchItsOwnLaunchInFlight(t *testing.T) {
	_, tasks, launcher, d, _ := newFixture(t, seatFor("a1", "user-a", 2))
	tasks.tasks = []kandev.Task{intake("t-1", "user-a", "high")}
	if err := d.Round(context.Background()); err != nil {
		t.Fatal(err)
	}
	tasks.tasks = []kandev.Task{withSession(intake("t-1", "user-a", "high"), "sess-t-1", "CREATED")}
	if err := d.Round(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(launcher.calls) != 1 || len(launcher.stopped) != 0 {
		t.Fatalf("calls = %d, stopped = %v; fleetd's own new session is not a prepared one", len(launcher.calls), launcher.stopped)
	}
}
