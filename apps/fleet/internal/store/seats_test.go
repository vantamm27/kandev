package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

var testNow = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

func newSeat(id, host, user string) Seat {
	return Seat{
		ID: id, OwnerUserID: "user-a", SSHHost: host, SSHPort: 22, SSHUser: user,
		HostKeyFingerprint: "SHA256:fp", Labels: []string{"team:core", "macos"}, Capacity: 2,
		KandevExecutorID: "exec-" + id, KandevExecutorProfileID: "prof-" + id,
		HMACSecret: "secret", State: SeatOnline, AgentVersion: "0.1.0",
	}
}

func TestTokenLifecycle(t *testing.T) {
	s := openTestStore(t, filepath.Join(t.TempDir(), "fleet.db"))
	ctx := context.Background()
	if err := s.CreateToken(ctx, "raw-1", RegistrationToken{OwnerUserID: "user-a", Labels: []string{"team:core"}, ExpiresAt: testNow.Add(time.Hour)}, testNow); err != nil {
		t.Fatalf("create: %v", err)
	}
	tok, err := s.LookupToken(ctx, "raw-1", testNow)
	if err != nil || tok.OwnerUserID != "user-a" || len(tok.Labels) != 1 || tok.Labels[0] != "team:core" {
		t.Fatalf("lookup = %+v, %v", tok, err)
	}
	if _, err := s.LookupToken(ctx, "raw-1", testNow.Add(2*time.Hour)); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("expired lookup = %v", err)
	}
	if _, err := s.LookupToken(ctx, "unknown", testNow); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("unknown lookup = %v", err)
	}
	var stored string
	if err := s.DB().QueryRow(`SELECT token_hash FROM registration_tokens`).Scan(&stored); err != nil || stored == "raw-1" || stored != HashToken("raw-1") {
		t.Fatalf("token must be stored hashed, got %q (%v)", stored, err)
	}
}

func TestRegisterSeatConsumesTokenOnce(t *testing.T) {
	s := openTestStore(t, filepath.Join(t.TempDir(), "fleet.db"))
	ctx := context.Background()
	if err := s.CreateToken(ctx, "raw-1", RegistrationToken{OwnerUserID: "user-a", ExpiresAt: testNow.Add(time.Hour)}, testNow); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.RegisterSeat(ctx, "raw-1", newSeat("seat-1", "mini-1", "seat-a"), testNow); err != nil {
		t.Fatalf("register: %v", err)
	}
	got, err := s.GetSeat(ctx, "seat-1")
	if err != nil || got.KandevExecutorID != "exec-seat-1" || got.State != SeatOnline || len(got.Labels) != 2 || got.Capacity != 2 {
		t.Fatalf("seat = %+v, %v", got, err)
	}
	if err := s.RegisterSeat(ctx, "raw-1", newSeat("seat-2", "mini-2", "seat-b"), testNow); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("reused token = %v, want ErrTokenInvalid", err)
	}
	if _, err := s.GetSeat(ctx, "seat-2"); !errors.Is(err, ErrSeatNotFound) {
		t.Fatalf("seat-2 must not exist, got %v", err)
	}
}

func TestRegisterSeatDuplicateEndpointKeepsToken(t *testing.T) {
	s := openTestStore(t, filepath.Join(t.TempDir(), "fleet.db"))
	ctx := context.Background()
	for _, raw := range []string{"raw-1", "raw-2"} {
		if err := s.CreateToken(ctx, raw, RegistrationToken{OwnerUserID: "user-a", ExpiresAt: testNow.Add(time.Hour)}, testNow); err != nil {
			t.Fatalf("create: %v", err)
		}
	}
	if err := s.RegisterSeat(ctx, "raw-1", newSeat("seat-1", "mini-1", "seat-a"), testNow); err != nil {
		t.Fatalf("register: %v", err)
	}
	taken, err := s.SeatEndpointTaken(ctx, "mini-1", 22, "seat-a")
	if err != nil || !taken {
		t.Fatalf("endpoint taken = %v, %v", taken, err)
	}
	if err := s.RegisterSeat(ctx, "raw-2", newSeat("seat-2", "mini-1", "seat-a"), testNow); !errors.Is(err, ErrSeatExists) {
		t.Fatalf("duplicate endpoint = %v, want ErrSeatExists", err)
	}
	if _, err := s.LookupToken(ctx, "raw-2", testNow); err != nil {
		t.Fatalf("a failed registration must leave its token usable: %v", err)
	}
}

func TestRetiredSeatFreesEndpoint(t *testing.T) {
	s := openTestStore(t, filepath.Join(t.TempDir(), "fleet.db"))
	ctx := context.Background()
	for _, raw := range []string{"raw-1", "raw-2", "raw-3"} {
		if err := s.CreateToken(ctx, raw, RegistrationToken{OwnerUserID: "user-a", ExpiresAt: testNow.Add(time.Hour)}, testNow); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.RegisterSeat(ctx, "raw-1", newSeat("seat-1", "mini-1", "seat-a"), testNow); err != nil {
		t.Fatal(err)
	}
	if err := s.RegisterSeat(ctx, "raw-2", newSeat("seat-2", "mini-1", "seat-a"), testNow); !errors.Is(err, ErrSeatExists) {
		t.Fatalf("live duplicate = %v", err)
	}
	if err := s.SetSeatState(ctx, "seat-1", []string{SeatOnline}, SeatRetired, testNow); err != nil {
		t.Fatal(err)
	}
	if taken, err := s.SeatEndpointTaken(ctx, "mini-1", 22, "seat-a"); err != nil || taken {
		t.Fatalf("retired endpoint taken = %v, %v", taken, err)
	}
	if err := s.RegisterSeat(ctx, "raw-3", newSeat("seat-3", "mini-1", "seat-a"), testNow); err != nil {
		t.Fatalf("re-register after retire: %v", err)
	}
	seats, err := s.ListSeats(ctx)
	if err != nil || len(seats) != 2 {
		t.Fatalf("list = %d seats, %v", len(seats), err)
	}
}
