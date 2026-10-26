package nudge

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kandev/kandev/apps/fleet/internal/kandev"
	"github.com/kandev/kandev/apps/fleet/internal/store"
)

var now = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

type fakeTasks map[string]string

func (f fakeTasks) GetTask(_ context.Context, id string) (kandev.Task, error) {
	return kandev.Task{ID: id, State: f[id]}, nil
}

type fakeMessenger struct {
	sent []string
	err  error
}

func (f *fakeMessenger) AddMessage(_ context.Context, taskID, sessionID, content string) error {
	if f.err != nil {
		return f.err
	}
	if content != Message(taskID) || !strings.Contains(content, "refs/agent-wip/"+taskID) {
		return errors.New("unexpected content")
	}
	f.sent = append(f.sent, taskID+"/"+sessionID)
	return nil
}

func pct(v float64) *float64 { return &v }

func setup(t *testing.T, tasks fakeTasks) (*store.Store, *fakeMessenger, *Nudger) {
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
		SSHUser: "seat-a", HostKeyFingerprint: "SHA256:x", Capacity: 3, HMACSecret: "s", State: store.SeatOnline}, now); err != nil {
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
	m := &fakeMessenger{}
	return st, m, New(st, tasks, m, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func beat(t *testing.T, st *store.Store, state string, q5h, qweek *float64, signedAt int64) {
	t.Helper()
	if err := st.ApplyHeartbeat(context.Background(), "seat_1", store.HeartbeatUpdate{State: state, Quota5hPct: q5h, QuotaWeekPct: qweek, FreeSlots: 1, SignedAt: signedAt}, now); err != nil {
		t.Fatal(err)
	}
}

func TestNearLimit(t *testing.T) {
	for name, tc := range map[string]struct {
		seat store.SeatSummary
		want bool
	}{
		"below":           {store.SeatSummary{State: store.SeatOnline, Quota5hPct: pct(84.9)}, false},
		"5h at threshold": {store.SeatSummary{State: store.SeatBusy, Quota5hPct: pct(85)}, true},
		"weekly high":     {store.SeatSummary{State: store.SeatOnline, QuotaWeekPct: pct(91)}, true},
		"unknown":         {store.SeatSummary{State: store.SeatOnline}, false},
		"already limited": {store.SeatSummary{State: store.SeatCooldown5h, Quota5hPct: pct(100)}, false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := NearLimit(tc.seat); got != tc.want {
				t.Fatalf("NearLimit = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestScanNudgesRunningTasksOncePerWindow(t *testing.T) {
	st, m, n := setup(t, fakeTasks{"t-run": "IN_PROGRESS", "t-review": "REVIEW"})
	ctx := context.Background()
	beat(t, st, store.SeatOnline, pct(50), nil, 1)
	if sent, err := n.Scan(ctx, now); err != nil || sent != 0 {
		t.Fatalf("below threshold sent %d, %v", sent, err)
	}
	beat(t, st, store.SeatBusy, pct(88), nil, 2)
	if sent, err := n.Scan(ctx, now); err != nil || sent != 1 || len(m.sent) != 1 || m.sent[0] != "t-run/sess-t-run" {
		t.Fatalf("sent %d (%v), %v; only the running task is nudged", sent, m.sent, err)
	}
	if sent, _ := n.Scan(ctx, now.Add(time.Hour)); sent != 0 {
		t.Fatal("one nudge per task per 5-hour window")
	}
	if sent, _ := n.Scan(ctx, now.Add(Every+time.Minute)); sent != 1 {
		t.Fatal("the next window gets a new nudge")
	}
}

func TestFailedNudgeIsRetried(t *testing.T) {
	st, m, n := setup(t, fakeTasks{"t-run": "IN_PROGRESS"})
	beat(t, st, store.SeatOnline, nil, pct(95), 1)
	m.err = errors.New("kandev: websocket closed")
	if sent, _ := n.Scan(context.Background(), now); sent != 0 {
		t.Fatal("a failed nudge is not counted")
	}
	m.err = nil
	if sent, _ := n.Scan(context.Background(), now); sent != 1 {
		t.Fatal("a failed nudge must be retried on the next scan")
	}
}
