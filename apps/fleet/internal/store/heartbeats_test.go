package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func registeredStore(t *testing.T, seats ...Seat) *Store {
	t.Helper()
	s := openTestStore(t, filepath.Join(t.TempDir(), "fleet.db"))
	for i, seat := range seats {
		raw := "raw-" + seat.ID
		if err := s.CreateToken(context.Background(), raw, RegistrationToken{OwnerUserID: "user-a", ExpiresAt: testNow.Add(time.Hour)}, testNow); err != nil {
			t.Fatalf("token %d: %v", i, err)
		}
		if err := s.RegisterSeat(context.Background(), raw, seat, testNow); err != nil {
			t.Fatalf("register %d: %v", i, err)
		}
	}
	return s
}

func TestApplyHeartbeatRejectsReplays(t *testing.T) {
	s := registeredStore(t, newSeat("seat-1", "mini-1", "seat-a"))
	ctx := context.Background()
	pct := 72.5
	reset := testNow.Add(2 * time.Hour)
	u := HeartbeatUpdate{State: SeatCooldown5h, LimitReason: "5h", Quota5hPct: &pct, Reset5hAt: &reset, FreeSlots: 1, AgentVersion: "0.2.0", SignedAt: 100}
	if err := s.ApplyHeartbeat(ctx, "seat-1", u, testNow); err != nil {
		t.Fatalf("first heartbeat: %v", err)
	}
	got, err := s.GetSeat(ctx, "seat-1")
	if err != nil || got.State != SeatCooldown5h || got.AgentVersion != "0.2.0" {
		t.Fatalf("seat = %+v, %v", got, err)
	}
	if err := s.ApplyHeartbeat(ctx, "seat-1", u, testNow); !errors.Is(err, ErrStaleSignature) {
		t.Fatalf("replayed heartbeat = %v, want ErrStaleSignature", err)
	}
	u.SignedAt = 99
	if err := s.ApplyHeartbeat(ctx, "seat-1", u, testNow); !errors.Is(err, ErrStaleSignature) {
		t.Fatalf("older heartbeat = %v, want ErrStaleSignature", err)
	}
	u.SignedAt = 101
	if err := s.ApplyHeartbeat(ctx, "seat-1", u, testNow); err != nil {
		t.Fatalf("newer heartbeat: %v", err)
	}
	if err := s.ApplyHeartbeat(ctx, "missing", u, testNow); !errors.Is(err, ErrSeatNotFound) {
		t.Fatalf("unknown seat = %v, want ErrSeatNotFound", err)
	}
}

func TestMarkStaleSeatsOffline(t *testing.T) {
	fresh, silent, never, draining := newSeat("fresh", "m1", "a"), newSeat("silent", "m2", "a"), newSeat("never", "m3", "a"), newSeat("draining", "m4", "a")
	draining.State = SeatDraining
	s := registeredStore(t, fresh, silent, never, draining)
	ctx := context.Background()
	beat := func(id string, at time.Time) {
		if err := s.ApplyHeartbeat(ctx, id, HeartbeatUpdate{State: SeatOnline, FreeSlots: 1, SignedAt: at.Unix()}, at); err != nil {
			t.Fatalf("heartbeat %s: %v", id, err)
		}
	}
	now := testNow.Add(10 * time.Minute)
	beat("fresh", now.Add(-30*time.Second))
	beat("silent", now.Add(-5*time.Minute))

	n, err := s.MarkStaleSeatsOffline(ctx, now.Add(-90*time.Second), now)
	if err != nil || n != 2 {
		t.Fatalf("marked = %d, %v; want silent and never-heartbeating seats", n, err)
	}
	for id, want := range map[string]string{"fresh": SeatOnline, "silent": SeatOffline, "never": SeatOffline, "draining": SeatDraining} {
		got, err := s.GetSeat(ctx, id)
		if err != nil || got.State != want {
			t.Fatalf("%s state = %s, want %s (%v)", id, got.State, want, err)
		}
	}
	if n, err := s.MarkStaleSeatsOffline(ctx, now.Add(-90*time.Second), now); err != nil || n != 0 {
		t.Fatalf("second sweep marked %d, %v; offline seats must be left alone", n, err)
	}
}
