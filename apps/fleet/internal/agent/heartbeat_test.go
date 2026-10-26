package agent

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kandev/kandev/apps/fleet/internal/server"
	"github.com/kandev/kandev/apps/fleet/internal/store"
)

type fixedSlots int

func (f fixedSlots) Running(context.Context) (int, error) { return int(f), nil }

// realFleetd runs the real fleetd heartbeat handler on a real store.
func realFleetd(t *testing.T, now time.Time) (*httptest.Server, *store.Store) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "fleet.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.CreateToken(context.Background(), "tok", store.RegistrationToken{OwnerUserID: "user-a", ExpiresAt: now.Add(time.Hour)}, now); err != nil {
		t.Fatal(err)
	}
	if err := st.RegisterSeat(context.Background(), "tok", store.Seat{ID: "seat_1", OwnerUserID: "user-a", SSHHost: "mini-1",
		SSHPort: 22, SSHUser: "seat-a", HostKeyFingerprint: "SHA256:x", Capacity: 2, HMACSecret: "seat-secret", State: store.SeatOnline}, now); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(server.New(server.Deps{DB: st, Seats: st, Log: log}))
	t.Cleanup(srv.Close)
	return srv, st
}

func TestReporterAgainstRealFleetd(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	srv, st := realFleetd(t, now)
	quotaPath := filepath.Join(t.TempDir(), "quota.json")
	clock := now
	reporter := &Reporter{
		Config:  Config{FleetdURL: srv.URL, SeatID: "seat_1", HMACSecret: "seat-secret", Capacity: 2, QuotaFile: quotaPath},
		Version: "test", HTTP: NewHTTPClient(), Slots: fixedSlots(1),
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: func() time.Time { return clock },
	}

	state, err := reporter.SendOnce(context.Background())
	if err != nil || state != store.SeatOnline {
		t.Fatalf("first heartbeat = %q, %v", state, err)
	}

	reset := now.Add(3 * time.Hour).Format(time.RFC3339)
	if err := os.WriteFile(quotaPath, []byte(`{"limit":"5h","reset_5h_at":"`+reset+`","updated_at":"`+now.Format(time.RFC3339)+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	clock = now.Add(time.Second)
	state, err = reporter.SendOnce(context.Background())
	if err != nil || state != store.SeatCooldown5h {
		t.Fatalf("limit heartbeat = %q, %v", state, err)
	}
	seat, _ := st.GetSeat(context.Background(), "seat_1")
	if seat.State != store.SeatCooldown5h || seat.AgentVersion != "test" {
		t.Fatalf("seat = %+v", seat)
	}

	reporter.Config.HMACSecret = "stolen-guess"
	clock = now.Add(2 * time.Second)
	if _, err := reporter.SendOnce(context.Background()); err == nil || !strings.Contains(err.Error(), "HTTP 401") {
		t.Fatalf("wrong secret = %v, want HTTP 401", err)
	}
}

func TestReporterRunStopsOnCancel(t *testing.T) {
	now := time.Now().UTC()
	srv, _ := realFleetd(t, now)
	reporter := &Reporter{
		Config: Config{FleetdURL: srv.URL, SeatID: "seat_1", HMACSecret: "seat-secret", Capacity: 1},
		HTTP:   NewHTTPClient(), Slots: fixedSlots(0),
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: time.Now,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); reporter.Run(ctx, time.Hour) }()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}
