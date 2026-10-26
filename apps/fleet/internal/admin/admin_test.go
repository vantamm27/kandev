package admin

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

type fakeKandev struct {
	deleted []string
	err     error
}

func (f *fakeKandev) DeleteExecutor(_ context.Context, id string) error {
	f.deleted = append(f.deleted, id)
	return f.err
}

var now = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

func setup(t *testing.T, k *fakeKandev) (*Service, *store.Store) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "fleet.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	register(t, st, "tok-1", "seat_1")
	svc := New(st, k, slog.New(slog.NewTextHandler(io.Discard, nil)))
	svc.now = func() time.Time { return now }
	return svc, st
}

func register(t *testing.T, st *store.Store, token, id string) {
	t.Helper()
	ctx := context.Background()
	if err := st.CreateToken(ctx, token, store.RegistrationToken{OwnerUserID: "user-a", ExpiresAt: now.Add(time.Hour)}, now); err != nil {
		t.Fatal(err)
	}
	if err := st.RegisterSeat(ctx, token, store.Seat{ID: id, OwnerUserID: "user-a", SSHHost: "mini-1", SSHPort: 22,
		SSHUser: "seat-a", HostKeyFingerprint: "SHA256:x", Capacity: 2, KandevExecutorID: "exec-" + id,
		HMACSecret: "old", State: store.SeatOnline}, now); err != nil {
		t.Fatalf("register %s: %v", id, err)
	}
}

func heartbeat(t *testing.T, st *store.Store, id string, freeSlots int, signedAt int64) {
	t.Helper()
	seat, err := st.GetSeat(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.ApplyHeartbeat(context.Background(), id, store.HeartbeatUpdate{State: seat.State, FreeSlots: freeSlots, SignedAt: signedAt}, now); err != nil {
		t.Fatal(err)
	}
}

func state(t *testing.T, st *store.Store, id string) string {
	t.Helper()
	seat, err := st.GetSeat(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return seat.State
}

func TestDrainAndUndrain(t *testing.T) {
	svc, st := setup(t, &fakeKandev{})
	ctx := context.Background()
	if err := svc.Undrain(ctx, "seat_1"); !errors.Is(err, store.ErrSeatStateConflict) {
		t.Fatalf("undrain an online seat = %v", err)
	}
	if err := svc.Drain(ctx, "seat_1"); err != nil || state(t, st, "seat_1") != store.SeatDraining {
		t.Fatalf("drain = %v, state %s", err, state(t, st, "seat_1"))
	}
	if err := svc.Drain(ctx, "seat_1"); !errors.Is(err, store.ErrSeatStateConflict) {
		t.Fatalf("drain twice = %v", err)
	}
	if err := svc.Undrain(ctx, "seat_1"); err != nil || state(t, st, "seat_1") != store.SeatOnline {
		t.Fatalf("undrain = %v", err)
	}
	if err := svc.Drain(ctx, "missing"); !errors.Is(err, store.ErrSeatNotFound) {
		t.Fatalf("drain missing = %v", err)
	}
}

func TestRetireRequiresDrainedIdleSeat(t *testing.T) {
	k := &fakeKandev{}
	svc, st := setup(t, k)
	ctx := context.Background()
	if err := svc.Retire(ctx, "seat_1", false); !errors.Is(err, store.ErrSeatStateConflict) {
		t.Fatalf("retire online seat = %v", err)
	}
	heartbeat(t, st, "seat_1", 1, 1)
	if err := svc.Drain(ctx, "seat_1"); err != nil {
		t.Fatal(err)
	}
	if err := svc.Retire(ctx, "seat_1", false); !errors.Is(err, ErrSeatBusy) {
		t.Fatalf("retire with a running session = %v", err)
	}
	if len(k.deleted) != 0 {
		t.Fatal("a refused retire must not touch Kandev")
	}
	heartbeat(t, st, "seat_1", 2, 2)
	if err := svc.Retire(ctx, "seat_1", false); err != nil {
		t.Fatalf("retire idle drained seat: %v", err)
	}
	if state(t, st, "seat_1") != store.SeatRetired || len(k.deleted) != 1 || k.deleted[0] != "exec-seat_1" {
		t.Fatalf("state %s, deleted %v", state(t, st, "seat_1"), k.deleted)
	}
	if err := svc.Retire(ctx, "seat_1", true); !errors.Is(err, store.ErrSeatStateConflict) {
		t.Fatalf("retire twice = %v", err)
	}
	register(t, st, "tok-2", "seat_2")
	if state(t, st, "seat_2") != store.SeatOnline {
		t.Fatal("a retired seat's endpoint must be registrable again")
	}
}

func TestRetireKandevOutcomes(t *testing.T) {
	t.Run("executor already gone", func(t *testing.T) {
		svc, st := setup(t, &fakeKandev{err: &kandev.APIError{Status: 404, Message: "executor not found"}})
		if err := svc.Retire(context.Background(), "seat_1", true); err != nil {
			t.Fatalf("retire = %v", err)
		}
		if state(t, st, "seat_1") != store.SeatRetired {
			t.Fatal("a missing executor still retires the seat")
		}
	})
	t.Run("kandev refuses", func(t *testing.T) {
		svc, st := setup(t, &fakeKandev{err: &kandev.APIError{Status: 409, Message: "executor in use"}})
		err := svc.Retire(context.Background(), "seat_1", true)
		var kerr *KandevError
		if !errors.As(err, &kerr) {
			t.Fatalf("retire = %v, want KandevError", err)
		}
		if state(t, st, "seat_1") != store.SeatOnline {
			t.Fatal("a failed Kandev delete must leave the seat as it was")
		}
	})
}

func TestRotateSecret(t *testing.T) {
	svc, st := setup(t, &fakeKandev{})
	heartbeat(t, st, "seat_1", 2, 500)
	secret, err := svc.RotateSecret(context.Background(), "seat_1")
	if err != nil || len(secret) != 64 {
		t.Fatalf("rotate = %q, %v", secret, err)
	}
	seat, _ := st.GetSeat(context.Background(), "seat_1")
	if seat.HMACSecret != secret {
		t.Fatal("secret not stored")
	}
	heartbeat(t, st, "seat_1", 2, 1) // replay counter resets with the new secret
	if _, err := svc.RotateSecret(context.Background(), "missing"); !errors.Is(err, store.ErrSeatNotFound) {
		t.Fatalf("rotate missing = %v", err)
	}
}
