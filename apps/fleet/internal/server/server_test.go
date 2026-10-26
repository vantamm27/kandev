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
)

type fakePinger struct{ err error }

func (f fakePinger) Ping(context.Context) error { return f.err }

func get(t *testing.T, h http.Handler, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

func TestHealthz(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	for name, tc := range map[string]struct {
		err    error
		status int
		body   string
	}{
		"healthy":     {status: http.StatusOK, body: `{"status":"ok"}`},
		"db unusable": {err: errors.New("disk gone"), status: http.StatusServiceUnavailable, body: `{"status":"unavailable"}`},
	} {
		t.Run(name, func(t *testing.T) {
			rec := get(t, New(fakePinger{err: tc.err}, log), http.MethodGet, "/healthz")
			if rec.Code != tc.status || strings.TrimSpace(rec.Body.String()) != tc.body {
				t.Fatalf("got %d %q, want %d %q", rec.Code, rec.Body.String(), tc.status, tc.body)
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
				t.Fatalf("content type = %q", ct)
			}
		})
	}
}

func TestHealthzRejectsOtherMethods(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if rec := get(t, New(fakePinger{}, log), http.MethodPost, "/healthz"); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /healthz = %d, want 405", rec.Code)
	}
}
