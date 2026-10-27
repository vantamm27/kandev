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

	"github.com/kandev/kandev/apps/fleet/internal/store"
)

type fakeLister struct{ err error }

func (f fakeLister) ListSeats(context.Context) ([]store.SeatSummary, error) {
	return []store.SeatSummary{{ID: "seat_1", OwnerUserID: "user-a", State: store.SeatOnline, Capacity: 1}}, f.err
}

func getMetrics(t *testing.T, deps Deps, auth string) *httptest.ResponseRecorder {
	t.Helper()
	deps.DB = fakePinger{}
	deps.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	rec := httptest.NewRecorder()
	New(deps).ServeHTTP(rec, req)
	return rec
}

func TestMetricsEndpoint(t *testing.T) {
	if rec := getMetrics(t, Deps{}, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("disabled = %d", rec.Code)
	}
	rec := getMetrics(t, Deps{Metrics: fakeLister{}}, "")
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain; version=0.0.4") ||
		!strings.Contains(rec.Body.String(), `fleet_seats{state="online"} 1`) {
		t.Fatalf("open metrics = %d %q %s", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
	}
	if rec := getMetrics(t, Deps{Metrics: fakeLister{}, MetricsToken: "m"}, "Bearer nope"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token = %d", rec.Code)
	}
	if rec := getMetrics(t, Deps{Metrics: fakeLister{}, MetricsToken: "m"}, "Bearer m"); rec.Code != http.StatusOK {
		t.Fatalf("right token = %d", rec.Code)
	}
	if rec := getMetrics(t, Deps{Metrics: fakeLister{err: errors.New("db")}}, ""); rec.Code != http.StatusInternalServerError {
		t.Fatalf("store error = %d", rec.Code)
	}
}

type fakeTaskOutcomes struct{ err error }

func (f fakeTaskOutcomes) ListTaskOutcomes(context.Context) ([]store.TaskOutcome, error) {
	return []store.TaskOutcome{{Claim: store.Claim{TaskID: "task-1", SeatID: "seat_1", Attempt: 1,
		State: store.ClaimLaunched, TaskState: "IN_PROGRESS"}, OwnerUserID: "user-a"}}, f.err
}

func TestMetricsIncludeTaskOutcomes(t *testing.T) {
	rec := getMetrics(t, Deps{Metrics: fakeLister{}, TaskOutcomes: fakeTaskOutcomes{}}, "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `fleet_tasks{seat="seat_1",owner="user-a",outcome="running"} 1`) {
		t.Fatalf("task metrics = %d %s", rec.Code, rec.Body.String())
	}
	if rec := getMetrics(t, Deps{Metrics: fakeLister{}, TaskOutcomes: fakeTaskOutcomes{err: errors.New("db")}}, ""); rec.Code != http.StatusInternalServerError {
		t.Fatalf("task store error = %d", rec.Code)
	}
}
