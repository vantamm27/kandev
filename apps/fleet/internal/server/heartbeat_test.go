package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kandev/kandev/apps/fleet/internal/hmacauth"
	"github.com/kandev/kandev/apps/fleet/internal/store"
)

var hbNow = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

func heartbeatServer(t *testing.T) (*Server, *store.Store) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "fleet.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.CreateToken(context.Background(), "tok", store.RegistrationToken{OwnerUserID: "user-a", ExpiresAt: hbNow.Add(time.Hour)}, hbNow); err != nil {
		t.Fatal(err)
	}
	seat := store.Seat{ID: "seat_1", OwnerUserID: "user-a", SSHHost: "mini-1", SSHPort: 22, SSHUser: "seat-a",
		HostKeyFingerprint: "SHA256:x", Capacity: 2, HMACSecret: "seat-secret", State: store.SeatOnline}
	if err := st.RegisterSeat(context.Background(), "tok", seat, hbNow); err != nil {
		t.Fatal(err)
	}
	clock := hbNow
	srv := New(Deps{DB: st, Seats: st, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: func() time.Time { return clock }})
	return srv, st
}

func sendHeartbeat(t *testing.T, srv *Server, body string, secret string, at time.Time) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/heartbeat", strings.NewReader(body))
	hmacauth.SetHeaders(req, "seat_1", []byte(secret), []byte(body), at)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func TestHeartbeatUpdatesSeatState(t *testing.T) {
	srv, st := heartbeatServer(t)
	rec := sendHeartbeat(t, srv, `{"limit":"5h","quota_5h_pct":100,"reset_5h_at":"2026-10-26T14:00:00Z","free_slots":5,"agent_version":"0.2.0"}`, "seat-secret", hbNow)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var body heartbeatResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.State != store.SeatCooldown5h || body.SeatID != "seat_1" {
		t.Fatalf("response = %s", rec.Body.String())
	}
	seat, _ := st.GetSeat(context.Background(), "seat_1")
	if seat.State != store.SeatCooldown5h || seat.AgentVersion != "0.2.0" {
		t.Fatalf("seat = %+v", seat)
	}
	var freeSlots int
	if err := st.DB().QueryRow(`SELECT free_slots FROM seats WHERE id = 'seat_1'`).Scan(&freeSlots); err != nil || freeSlots != 2 {
		t.Fatalf("free_slots = %d (%v), want capped at capacity 2", freeSlots, err)
	}

	rec = sendHeartbeat(t, srv, `{"free_slots":0}`, "seat-secret", hbNow.Add(time.Second))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"state":"busy"`) {
		t.Fatalf("limit cleared with no free slot: %d %s", rec.Code, rec.Body.String())
	}
}

func TestHeartbeatRejections(t *testing.T) {
	srv, _ := heartbeatServer(t)
	if rec := sendHeartbeat(t, srv, `{"free_slots":1}`, "wrong-secret", hbNow); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong secret = %d", rec.Code)
	}
	if rec := sendHeartbeat(t, srv, `{"free_slots":1}`, "seat-secret", hbNow.Add(-time.Hour)); rec.Code != http.StatusUnauthorized {
		t.Fatalf("old timestamp = %d", rec.Code)
	}
	if rec := sendHeartbeat(t, srv, `{"free_slots":-1}`, "seat-secret", hbNow); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid body = %d", rec.Code)
	}
	if rec := sendHeartbeat(t, srv, `{"free_slots":1,"admin":true}`, "seat-secret", hbNow); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown field = %d", rec.Code)
	}
	if rec := sendHeartbeat(t, srv, `{"free_slots":1}`, "seat-secret", hbNow); rec.Code != http.StatusOK {
		t.Fatalf("valid = %d %s", rec.Code, rec.Body.String())
	}
	if rec := sendHeartbeat(t, srv, `{"free_slots":1}`, "seat-secret", hbNow); rec.Code != http.StatusConflict {
		t.Fatalf("replay = %d, want 409", rec.Code)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/heartbeat", strings.NewReader(`{}`))
	hmacauth.SetHeaders(req, "seat_unknown", []byte("seat-secret"), []byte(`{}`), hbNow)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unknown seat = %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/heartbeat", strings.NewReader(`{}`)))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unsigned = %d", rec.Code)
	}
}
