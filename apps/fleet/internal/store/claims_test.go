package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestClaimLifecycle(t *testing.T) {
	s := registeredStore(t, newSeat("seat-1", "mini-1", "seat-a"))
	ctx := context.Background()
	attempt, err := s.BeginClaim(ctx, "task-1", "seat-1", testNow)
	if err != nil || attempt != 1 {
		t.Fatalf("first claim = %d, %v", attempt, err)
	}
	if _, err := s.BeginClaim(ctx, "task-1", "seat-1", testNow); !errors.Is(err, ErrAlreadyClaimed) {
		t.Fatalf("claiming a launching task = %v", err)
	}
	if err := s.FinishClaim(ctx, "task-1", ClaimFailed, "", "kandev down", testNow); err != nil {
		t.Fatal(err)
	}
	attempt, err = s.BeginClaim(ctx, "task-1", "seat-1", testNow.Add(time.Minute))
	if err != nil || attempt != 2 {
		t.Fatalf("retry after failure = %d, %v", attempt, err)
	}
	if err := s.FinishClaim(ctx, "task-1", ClaimLaunched, "session-1", "", testNow); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginClaim(ctx, "task-1", "seat-1", testNow); !errors.Is(err, ErrAlreadyClaimed) {
		t.Fatalf("claiming a launched task = %v", err)
	}
	if err := s.FinishClaim(ctx, "task-1", ClaimFailed, "", "", testNow); err == nil {
		t.Fatal("finishing a claim that is not launching must fail")
	}
	claims, err := s.ListClaims(ctx)
	if err != nil || claims["task-1"].State != ClaimLaunched || claims["task-1"].SessionID != "session-1" || claims["task-1"].Attempt != 2 {
		t.Fatalf("claims = %+v, %v", claims, err)
	}
}

func TestFailStaleLaunchingAndUnconfirmedLaunches(t *testing.T) {
	s := registeredStore(t, newSeat("seat-1", "mini-1", "seat-a"), newSeat("seat-2", "mini-2", "seat-a"))
	ctx := context.Background()
	for _, id := range []string{"old", "new", "done"} {
		if _, err := s.BeginClaim(ctx, id, "seat-1", testNow); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.FinishClaim(ctx, "done", ClaimLaunched, "s", "", testNow); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`UPDATE dispatch_claims SET updated_at = ? WHERE task_id = 'old'`, testNow.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	n, err := s.FailStaleLaunching(ctx, testNow.Add(-5*time.Minute), testNow)
	if err != nil || n != 1 {
		t.Fatalf("stale launching failed = %d, %v", n, err)
	}
	pending, err := s.UnconfirmedLaunches(ctx)
	if err != nil || pending["seat-1"] != 2 || pending["seat-2"] != 0 {
		t.Fatalf("unconfirmed before heartbeat = %v, %v", pending, err)
	}
	if err := s.ApplyHeartbeat(ctx, "seat-1", HeartbeatUpdate{State: SeatOnline, FreeSlots: 0, SignedAt: 1}, testNow.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if pending, err := s.UnconfirmedLaunches(ctx); err != nil || pending["seat-1"] != 0 {
		t.Fatalf("a heartbeat after the launches confirms them, got %v, %v", pending, err)
	}
}
