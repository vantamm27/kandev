package tasktrack

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
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
	tr := New(st, fakeTasks{"task-failed": {ID: "task-failed", State: "FAILED"}},
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
	if o := got["task-run"]; o.TaskState != "IN_PROGRESS" || o.FinishedAt != nil {
		t.Errorf("running task = %+v", o)
	}
	for id, state := range map[string]string{"task-review": "REVIEW", "task-gone": StateDeleted, "task-failed": "FAILED"} {
		if o := got[id]; o.TaskState != state || o.FinishedAt == nil {
			t.Errorf("%s = %+v, want finished as %s", id, o, state)
		}
	}
	if o := got["task-down"]; o.TaskState != "" || o.FinishedAt != nil {
		t.Errorf("unreadable task must be left for the next round: %+v", o)
	}
}
