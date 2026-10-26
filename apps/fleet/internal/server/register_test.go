package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kandev/kandev/apps/fleet/internal/registry"
	"github.com/kandev/kandev/apps/fleet/internal/store"
)

type fakeRegistrar struct {
	got    registry.Request
	result registry.Result
	err    error
}

func (f *fakeRegistrar) Register(_ context.Context, req registry.Request) (registry.Result, error) {
	f.got = req
	return f.result, f.err
}

func postRegister(t *testing.T, reg *fakeRegistrar, body string) *httptest.ResponseRecorder {
	t.Helper()
	srv := New(Deps{DB: fakePinger{}, Registrar: reg, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/register", strings.NewReader(body)))
	return rec
}

func TestRegisterReturnsCreatedSeat(t *testing.T) {
	reg := &fakeRegistrar{result: registry.Result{SeatID: "seat_1", HMACSecret: "s3cret", KandevExecutorID: "exec-1"}}
	rec := postRegister(t, reg, `{"token":"tok","ssh_host":"mini-1","ssh_user":"seat-a","host_key_fingerprint":"SHA256:x","capacity":2}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var body registry.Result
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.SeatID != "seat_1" || body.HMACSecret != "s3cret" {
		t.Fatalf("body = %s", rec.Body.String())
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("a response carrying a secret must not be cached")
	}
	if reg.got.SSHHost != "mini-1" || reg.got.Capacity != 2 {
		t.Fatalf("request = %+v", reg.got)
	}
}

func TestRegisterErrorStatuses(t *testing.T) {
	for name, tc := range map[string]struct {
		err    error
		status int
	}{
		"validation":    {&registry.ValidationError{Message: "ssh_user must be a POSIX user name"}, http.StatusBadRequest},
		"bad token":     {store.ErrTokenInvalid, http.StatusUnauthorized},
		"seat exists":   {store.ErrSeatExists, http.StatusConflict},
		"kandev failed": {&registry.KandevError{Err: errors.New("HTTP 403")}, http.StatusBadGateway},
		"internal":      {errors.New("disk full"), http.StatusInternalServerError},
	} {
		t.Run(name, func(t *testing.T) {
			rec := postRegister(t, &fakeRegistrar{err: tc.err}, `{"token":"tok"}`)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d", rec.Code, tc.status)
			}
			if name == "internal" && strings.Contains(rec.Body.String(), "disk full") {
				t.Fatal("internal errors must not leak details")
			}
		})
	}
}

func TestRegisterRejectsMalformedBodies(t *testing.T) {
	for name, body := range map[string]string{
		"not json":      `token=tok`,
		"unknown field": `{"token":"tok","is_admin":true}`,
		"too large":     `{"token":"` + strings.Repeat("a", maxRequestBody) + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			reg := &fakeRegistrar{}
			if rec := postRegister(t, reg, body); rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", rec.Code)
			}
			if reg.got.Token != "" {
				t.Fatal("a malformed body must not reach the registrar")
			}
		})
	}
}
