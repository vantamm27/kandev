package server

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kandev/kandev/apps/fleet/internal/admin"
	"github.com/kandev/kandev/apps/fleet/internal/store"
)

type fakeAdmin struct {
	err     error
	retired string
	force   bool
	drained string
}

func (f *fakeAdmin) ListSeats(context.Context) ([]store.SeatSummary, error) {
	return []store.SeatSummary{{ID: "seat_1", State: store.SeatOnline}}, f.err
}
func (f *fakeAdmin) Drain(_ context.Context, id string) error  { f.drained = id; return f.err }
func (f *fakeAdmin) Undrain(_ context.Context, _ string) error { return f.err }
func (f *fakeAdmin) Retire(_ context.Context, id string, force bool) error {
	f.retired, f.force = id, force
	return f.err
}
func (f *fakeAdmin) RotateSecret(context.Context, string) (string, error) { return "new-secret", f.err }
func (f *fakeAdmin) ListHandoffs(_ context.Context, states ...string) ([]store.Handoff, error) {
	return []store.Handoff{{ID: "hoff_1", State: strings.Join(states, "+")}}, f.err
}

func adminRequest(t *testing.T, a Admin, token, method, path, auth string) *httptest.ResponseRecorder {
	t.Helper()
	srv := New(Deps{DB: fakePinger{}, Admin: a, AdminToken: token, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	req := httptest.NewRequest(method, path, nil)
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func TestAdminAuth(t *testing.T) {
	a := &fakeAdmin{}
	if rec := adminRequest(t, a, "", http.MethodGet, "/v1/admin/seats", "Bearer anything"); rec.Code != http.StatusNotFound {
		t.Fatalf("disabled admin API = %d", rec.Code)
	}
	for _, auth := range []string{"", "Bearer wrong", "admin-token", "Basic admin-token"} {
		if rec := adminRequest(t, a, "admin-token", http.MethodGet, "/v1/admin/seats", auth); rec.Code != http.StatusUnauthorized {
			t.Fatalf("auth %q = %d, want 401", auth, rec.Code)
		}
	}
	rec := adminRequest(t, a, "admin-token", http.MethodGet, "/v1/admin/seats", "Bearer admin-token")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"id":"seat_1"`) || strings.Contains(rec.Body.String(), "hmac") {
		t.Fatalf("list = %d %s", rec.Code, rec.Body.String())
	}
}

func TestAdminActions(t *testing.T) {
	a := &fakeAdmin{}
	if rec := adminRequest(t, a, "tok", http.MethodPost, "/v1/admin/seats/seat_9/drain", "Bearer tok"); rec.Code != http.StatusOK || a.drained != "seat_9" {
		t.Fatalf("drain = %d, drained %q", rec.Code, a.drained)
	}
	if rec := adminRequest(t, a, "tok", http.MethodPost, "/v1/admin/seats/seat_9/retire?force=true", "Bearer tok"); rec.Code != http.StatusOK || a.retired != "seat_9" || !a.force {
		t.Fatalf("retire = %d, %q force=%v", rec.Code, a.retired, a.force)
	}
	rec := adminRequest(t, a, "tok", http.MethodPost, "/v1/admin/seats/seat_9/rotate-secret", "Bearer tok")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "new-secret") || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("rotate = %d %s", rec.Code, rec.Body.String())
	}
}

func TestAdminErrorStatuses(t *testing.T) {
	for name, tc := range map[string]struct {
		err    error
		status int
	}{
		"not found": {store.ErrSeatNotFound, http.StatusNotFound},
		"conflict":  {store.ErrSeatStateConflict, http.StatusConflict},
		"busy":      {admin.ErrSeatBusy, http.StatusConflict},
		"kandev":    {&admin.KandevError{Err: errors.New("HTTP 409")}, http.StatusBadGateway},
		"internal":  {errors.New("disk"), http.StatusInternalServerError},
	} {
		t.Run(name, func(t *testing.T) {
			rec := adminRequest(t, &fakeAdmin{err: tc.err}, "tok", http.MethodPost, "/v1/admin/seats/seat_1/retire", "Bearer tok")
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d", rec.Code, tc.status)
			}
		})
	}
}

func TestAdminListHandoffs(t *testing.T) {
	rec := adminRequest(t, &fakeAdmin{}, "tok", http.MethodGet, "/v1/admin/handoffs?state=offered,exhausted", "Bearer tok")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"state":"offered+exhausted"`) {
		t.Fatalf("list handoffs = %d %s", rec.Code, rec.Body.String())
	}
}
