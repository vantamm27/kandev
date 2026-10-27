package tasktrack

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/kandev/kandev/apps/fleet/internal/kandev"
	"github.com/kandev/kandev/apps/fleet/internal/store"
)

var now = time.Date(2026, 10, 27, 4, 0, 0, 0, time.UTC)

type fakeTasks map[string]kandev.Task

func (f fakeTasks) GetTask(_ context.Context, id string) (kandev.Task, error) {
	if id == "task-down" {
		return kandev.Task{}, errors.New("kandev unreachable")
	}
	t, ok := f[id]
	if !ok {
		return kandev.Task{}, &kandev.APIError{Status: 404, Message: "task not found"}
	}
	return t, nil
}

type fakeSessions struct{ stopped []string }

func (f *fakeSessions) StopSession(_ context.Context, sessionID, _ string) error {
	f.stopped = append(f.stopped, sessionID)
	return nil
}

func launchedStore(t *testing.T, taskIDs ...string) *store.Store {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "fleet.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.CreateToken(ctx, "tok", store.RegistrationToken{OwnerUserID: "user-a", ExpiresAt: now.Add(time.Hour)}, now); err != nil {
		t.Fatal(err)
	}
	if err := st.RegisterSeat(ctx, "tok", store.Seat{ID: "seat_1", OwnerUserID: "user-a", SSHHost: "h", SSHPort: 22,
		SSHUser: "u", HostKeyFingerprint: "SHA256:x", Capacity: 1, HMACSecret: "s", State: store.SeatOnline}, now); err != nil {
		t.Fatal(err)
	}
	for _, id := range taskIDs {
		if _, err := st.BeginClaim(ctx, id, "seat_1", now); err != nil {
			t.Fatal(err)
		}
		if err := st.FinishClaim(ctx, id, store.ClaimLaunched, "session-"+id, "", now); err != nil {
			t.Fatal(err)
		}
	}
	return st
}

func TestTrackRecordsStatesAndFinishes(t *testing.T) {
	st := launchedStore(t, "task-run", "task-review", "task-gone", "task-down", "task-failed")
	sessions := &fakeSessions{}
	tr := New(st, fakeTasks{"task-failed": {ID: "task-failed", State: "FAILED"}}, sessions,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	listed := map[string]kandev.Task{
		"task-run":    {ID: "task-run", State: "IN_PROGRESS"},
		"task-review": {ID: "task-review", State: "REVIEW"},
	}
	claims, err := st.ListClaims(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := tr.Track(context.Background(), claims, listed, now.Add(time.Minute)); err != nil {
		t.Fatalf("track: %v", err)
	}
	got, err := st.ListClaims(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for id, state := range map[string]string{"task-run": "IN_PROGRESS", "task-review": "REVIEW"} {
		if o := got[id]; o.TaskState != state || o.FinishedAt != nil {
			t.Errorf("%s = %+v, want unfinished in %s", id, o, state)
		}
	}
	for id, state := range map[string]string{"task-gone": StateDeleted, "task-failed": "FAILED"} {
		if o := got[id]; o.TaskState != state || o.FinishedAt == nil {
			t.Errorf("%s = %+v, want finished as %s", id, o, state)
		}
	}
	if o := got["task-down"]; o.TaskState != "" || o.FinishedAt != nil {
		t.Errorf("unreadable task must be left for the next round: %+v", o)
	}
	sort.Strings(sessions.stopped)
	if strings.Join(sessions.stopped, ",") != "session-task-failed" {
		t.Errorf("stopped sessions = %v; want the ended task's session, not the deleted, running, or review ones", sessions.stopped)
	}
	sessions.stopped = nil
	claims, _ = st.ListClaims(context.Background())
	if err := tr.Track(context.Background(), claims, listed, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(sessions.stopped) != 0 {
		t.Errorf("a finished task's session must be stopped once, got %v", sessions.stopped)
	}
}

func TestReleaseFreesOldestReviewSeatsForWaitingOwners(t *testing.T) {
	ctx := context.Background()
	st := launchedStore(t, "task-old", "task-new", "task-fresh", "task-run")
	// launchedStore claims every task on seat_1; spread them over two seats.
	if err := st.CreateToken(ctx, "tok2", store.RegistrationToken{OwnerUserID: "user-a", ExpiresAt: now.Add(time.Hour)}, now); err != nil {
		t.Fatal(err)
	}
	if err := st.RegisterSeat(ctx, "tok2", store.Seat{ID: "seat_2", OwnerUserID: "user-a", SSHHost: "h2", SSHPort: 22,
		SSHUser: "u", HostKeyFingerprint: "SHA256:y", Capacity: 1, HMACSecret: "s", State: store.SeatOnline}, now); err != nil {
		t.Fatal(err)
	}
	if err := st.MoveClaim(ctx, "task-new", "seat_2", "session-task-new", now); err != nil {
		t.Fatal(err)
	}
	for id, at := range map[string]time.Time{
		"task-old":   now.Add(-time.Hour),
		"task-new":   now.Add(-30 * time.Minute),
		"task-fresh": now.Add(-time.Minute),
	} {
		if err := st.RecordTaskState(ctx, id, "REVIEW", false, at); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.RecordTaskState(ctx, "task-run", "IN_PROGRESS", false, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	sessions := &fakeSessions{}
	tr := New(st, fakeTasks{}, sessions, slog.New(slog.NewTextHandler(io.Discard, nil)))
	tr.ReviewGrace = 5 * time.Minute
	seatOwners := map[string]string{"seat_1": "user-a", "seat_2": "user-a"}

	if err := tr.Release(ctx, map[string]int{"user-b": 3}, seatOwners, nil, now); err != nil {
		t.Fatal(err)
	}
	if len(sessions.stopped) != 0 {
		t.Fatalf("no waiting task of the seats' owner, nothing to free: %v", sessions.stopped)
	}
	listed := map[string]kandev.Task{"task-old": {ID: "task-old", WorkflowStepID: "step-approve"}}
	if err := tr.Release(ctx, map[string]int{"user-a": 1}, seatOwners, listed, now); err != nil {
		t.Fatal(err)
	}
	if strings.Join(sessions.stopped, ",") != "session-task-old" {
		t.Fatalf("stopped = %v; want only the oldest review session", sessions.stopped)
	}
	claims, _ := st.ListClaims(ctx)
	if c := claims["task-old"]; c.State != store.ClaimReleased || c.FinishedAt == nil || c.ReleasedStepID != "step-approve" {
		t.Fatalf("freed task's claim = %+v", c)
	}
	if err := tr.Release(ctx, map[string]int{"user-a": 5}, seatOwners, nil, now); err != nil {
		t.Fatal(err)
	}
	if strings.Join(sessions.stopped, ",") != "session-task-old,session-task-new" {
		t.Fatalf("stopped = %v; a review younger than the grace or a running task must keep its seat", sessions.stopped)
	}
	if err := tr.Release(ctx, map[string]int{"user-a": 1}, map[string]string{}, nil, now); err != nil {
		t.Fatal(err)
	}
	if len(sessions.stopped) != 2 {
		t.Fatalf("a seat that cannot take work (cooling down, draining) must not be freed: %v", sessions.stopped)
	}
}

func TestReleaseKeepsTheClaimWhenStoppingFails(t *testing.T) {
	ctx := context.Background()
	st := launchedStore(t, "task-1")
	if err := st.RecordTaskState(ctx, "task-1", "REVIEW", false, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	tr := New(st, fakeTasks{}, failingSessions{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := tr.Release(ctx, map[string]int{"user-a": 1}, map[string]string{"seat_1": "user-a"}, nil, now); err != nil {
		t.Fatal(err)
	}
	claims, _ := st.ListClaims(ctx)
	if c := claims["task-1"]; c.State != store.ClaimLaunched || c.FinishedAt != nil {
		t.Fatalf("a session that is still running must keep its claim: %+v", c)
	}
}

type failingSessions struct{}

func (failingSessions) StopSession(context.Context, string, string) error {
	return errors.New("kandev unreachable")
}
