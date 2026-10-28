package limits

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/kandev/kandev/apps/fleet/internal/kandev"
	"github.com/kandev/kandev/apps/fleet/internal/store"
)

var now = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

type fakeTasks map[string]kandev.Task

func (f fakeTasks) GetTask(_ context.Context, id string) (kandev.Task, error) {
	t, ok := f[id]
	if !ok {
		return kandev.Task{}, &kandev.APIError{Status: 404, Message: "task not found"}
	}
	return t, nil
}

func ptr(t time.Time) *time.Time { return &t }

func TestBlocked(t *testing.T) {
	for name, tc := range map[string]struct {
		seat store.SeatSummary
		want bool
	}{
		"online":            {store.SeatSummary{State: store.SeatOnline}, false},
		"weekly":            {store.SeatSummary{State: store.SeatCooldownWeek}, true},
		"5h, reset soon":    {store.SeatSummary{State: store.SeatCooldown5h, Reset5hAt: ptr(now.Add(90 * time.Minute))}, false},
		"5h, reset far":     {store.SeatSummary{State: store.SeatCooldown5h, Reset5hAt: ptr(now.Add(3 * time.Hour))}, true},
		"5h, reset unknown": {store.SeatSummary{State: store.SeatCooldown5h}, true},
	} {
		t.Run(name, func(t *testing.T) {
			if got := Blocked(tc.seat, now, DefaultWaitThreshold); got != tc.want {
				t.Fatalf("Blocked = %v, want %v", got, tc.want)
			}
		})
	}
}

func setup(t *testing.T, tasks fakeTasks) (*store.Store, *Detector) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "fleet.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	if err := st.CreateToken(ctx, "tok", store.RegistrationToken{OwnerUserID: "user-a", ExpiresAt: now.Add(time.Hour)}, now); err != nil {
		t.Fatal(err)
	}
	if err := st.RegisterSeat(ctx, "tok", store.Seat{ID: "seat_1", OwnerUserID: "user-a", SSHHost: "mini-1", SSHPort: 22,
		SSHUser: "seat-a", HostKeyFingerprint: "SHA256:x", Capacity: 2, HMACSecret: "s", State: store.SeatOnline}, now); err != nil {
		t.Fatal(err)
	}
	for id := range tasks {
		if _, err := st.BeginClaim(ctx, id, "seat_1", now); err != nil {
			t.Fatal(err)
		}
		if err := st.FinishClaim(ctx, id, store.ClaimLaunched, "sess-"+id, "", now); err != nil {
			t.Fatal(err)
		}
	}
	return st, New(st, tasks, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func beat(t *testing.T, st *store.Store, state, limit string, signedAt int64) {
	t.Helper()
	if err := st.ApplyHeartbeat(context.Background(), "seat_1", store.HeartbeatUpdate{State: state, LimitReason: limit, SignedAt: signedAt}, now); err != nil {
		t.Fatal(err)
	}
}

func TestScanOffersOnlyUnfinishedTasksOnBlockedSeats(t *testing.T) {
	st, d := setup(t, fakeTasks{
		"t-running": {ID: "t-running", State: "IN_PROGRESS"},
		"t-review":  {ID: "t-review", State: "REVIEW"},
	})
	if n, err := d.Scan(context.Background(), now); err != nil || n != 0 {
		t.Fatalf("online seat offered %d, %v", n, err)
	}
	beat(t, st, store.SeatCooldownWeek, "week", 1)
	n, err := d.Scan(context.Background(), now)
	if err != nil || n != 1 {
		t.Fatalf("offers = %d, %v; only the unfinished task moves", n, err)
	}
	offered, _ := st.ListHandoffs(context.Background(), store.HandoffOffered)
	if len(offered) != 1 || offered[0].TaskID != "t-running" || offered[0].FromSeatID != "seat_1" {
		t.Fatalf("offered = %+v", offered)
	}
	if n, _ := d.Scan(context.Background(), now); n != 0 {
		t.Fatal("a second scan must not duplicate the offer")
	}

	beat(t, st, store.SeatOnline, "", 2)
	if _, err := d.Scan(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if offered, _ := st.ListHandoffs(context.Background(), store.HandoffOffered); len(offered) != 0 {
		t.Fatalf("offers must be withdrawn when the seat recovers: %+v", offered)
	}
}

func TestScanIgnoresDeletedTasks(t *testing.T) {
	st, d := setup(t, fakeTasks{})
	if _, err := st.BeginClaim(context.Background(), "t-gone", "seat_1", now); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishClaim(context.Background(), "t-gone", store.ClaimLaunched, "s", "", now); err != nil {
		t.Fatal(err)
	}
	beat(t, st, store.SeatCooldownWeek, "week", 1)
	if n, err := d.Scan(context.Background(), now); err != nil || n != 0 {
		t.Fatalf("a deleted task gets no offer: %d, %v", n, err)
	}
}

// A task stopped by the limit sits in REVIEW (paused) or FAILED; both still
// need work and move to another member when the seat is blocked for long. A
// task in REVIEW for a person, and a finished one, do not.
func TestScanOffersTasksStoppedByTheLimit(t *testing.T) {
	st, d := setup(t, fakeTasks{
		"t-paused": {ID: "t-paused", State: "REVIEW"},
		"t-failed": {ID: "t-failed", State: "FAILED"},
		"t-review": {ID: "t-review", State: "REVIEW"},
		"t-done":   {ID: "t-done", State: "COMPLETED"},
	})
	ctx := context.Background()
	if _, err := st.PauseClaim(ctx, "t-paused", now); err != nil {
		t.Fatal(err)
	}
	if _, err := st.FailRunningClaim(ctx, "t-failed", "FAILED", "the session stopped at the seat's usage limit", now); err != nil {
		t.Fatal(err)
	}
	beat(t, st, store.SeatCooldownWeek, "week", 1)
	if n, err := d.Scan(ctx, now); err != nil || n != 2 {
		t.Fatalf("offers = %d, %v", n, err)
	}
	offered, _ := st.ListHandoffs(ctx, store.HandoffOffered)
	got := map[string]bool{}
	for _, h := range offered {
		got[h.TaskID] = true
	}
	if !got["t-paused"] || !got["t-failed"] {
		t.Fatalf("offered = %v; want the paused and the failed task", got)
	}
}
