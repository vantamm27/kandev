package dispatch

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kandev/kandev/apps/fleet/internal/kandev"
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

// Opening an intake task in the web UI prepares a session whose workspace
// lives on Kandev's default executor; Kandev refuses to start another session
// of that task on a seat, so fleetd leaves the task alone.
func TestLeavesTasksTheUIPrepared(t *testing.T) {
	_, tasks, launcher, d, _ := newFixture(t, seatFor("a1", "user-a", 1))
	tasks.tasks = []kandev.Task{withSession(intake("t-opened", "user-a", "high"), "s-prepared", "CREATED")}
	if err := d.Round(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(launcher.calls) != 0 {
		t.Fatalf("launches = %+v; a UI-prepared task cannot run on a seat", launcher.calls)
	}
}
