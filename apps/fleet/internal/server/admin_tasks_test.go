package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kandev/kandev/apps/fleet/internal/dispatch"
	"github.com/kandev/kandev/apps/fleet/internal/kandev"
	"github.com/kandev/kandev/apps/fleet/internal/store"
)

var tasksNow = time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)

type listedOutcomes []store.TaskOutcome

func (l listedOutcomes) ListTaskOutcomes(context.Context) ([]store.TaskOutcome, error) { return l, nil }

type fixedRound dispatch.Stats

func (f fixedRound) LastRound() dispatch.Stats { return dispatch.Stats(f) }

func outcome(task, seat, state, taskState string, claimed time.Time, finished *time.Time) store.TaskOutcome {
	return store.TaskOutcome{
		Claim:       store.Claim{TaskID: task, SeatID: seat, Attempt: 1, State: state, TaskState: taskState, ClaimedAt: claimed, FinishedAt: finished},
		OwnerUserID: "owner_" + seat,
	}
}

func TestAdminListTasks(t *testing.T) {
	doneAt := tasksNow.Add(-time.Hour)
	longAgo := tasksNow.Add(-8 * 24 * time.Hour)
	outcomes := listedOutcomes{
		outcome("t_old", "seat_a", store.ClaimLaunched, "COMPLETED", longAgo.Add(-time.Minute), &longAgo),
		outcome("t_done", "seat_a", store.ClaimLaunched, "REVIEW", doneAt.Add(-10*time.Minute), &doneAt),
		outcome("t_run", "seat_b", store.ClaimLaunched, "IN_PROGRESS", tasksNow.Add(-5*time.Minute), nil),
		outcome("t_fail", "seat_b", store.ClaimFailed, "", tasksNow.Add(-2*time.Minute), nil),
	}
	round := fixedRound{
		At: tasksNow, Queued: map[string]int{"low": 1},
		WaitingTasks: []dispatch.WaitingTask{{TaskID: "t_wait", Title: "Wait", Priority: "low", Reason: "weekly_pacing"}},
		Titles:       map[string]string{"t_run": "Running task"},
	}
	srv := New(Deps{
		DB: fakePinger{}, Admin: &fakeAdmin{}, AdminToken: "tok", TaskOutcomes: outcomes, Rounds: round,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: func() time.Time { return tasksNow },
	})
	req := httptest.NewRequest(http.MethodGet, "/v1/admin/tasks", nil)
	req.Header.Set("Authorization", "Bearer tok")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Queue struct {
			Queued  map[string]int         `json:"queued"`
			Waiting []dispatch.WaitingTask `json:"waiting"`
		} `json:"queue"`
		Recent []taskView       `json:"recent"`
		BySeat []seatTaskCounts `json:"by_seat"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Queue.Queued["low"] != 1 || len(got.Queue.Waiting) != 1 || got.Queue.Waiting[0].Reason != "weekly_pacing" {
		t.Fatalf("queue = %+v", got.Queue)
	}
	var ids []string
	for _, v := range got.Recent {
		ids = append(ids, v.TaskID)
	}
	if len(ids) != 3 || ids[0] != "t_fail" || ids[1] != "t_run" || ids[2] != "t_done" {
		t.Fatalf("recent = %v; want newest first without the task finished over a week ago", ids)
	}
	run, fail := got.Recent[1], got.Recent[0]
	if run.Title != "Running task" || run.Outcome != "running" || run.DurationSeconds == nil || *run.DurationSeconds != 300 {
		t.Fatalf("running task = %+v", run)
	}
	if fail.Outcome != "launch_failed" || fail.DurationSeconds != nil {
		t.Fatalf("failed launch = %+v", fail)
	}
	if len(got.BySeat) != 2 || got.BySeat[0].Outcomes["completed"] != 1 || got.BySeat[0].Outcomes["review"] != 1 ||
		got.BySeat[1].Outcomes["running"] != 1 || got.BySeat[1].Outcomes["launch_failed"] != 1 {
		t.Fatalf("by seat = %+v", got.BySeat)
	}
}

func TestAdminListTasksWithoutTracking(t *testing.T) {
	if rec := adminRequest(t, &fakeAdmin{}, "tok", http.MethodGet, "/v1/admin/tasks", "Bearer tok"); rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 without task tracking", rec.Code)
	}
}

func TestAdminTaskStatuses(t *testing.T) {
	reset := tasksNow.Add(2 * time.Hour)
	round := fixedRound{At: tasksNow, Tasks: map[string]dispatch.TaskStatus{
		"t_paused": {Status: dispatch.StatusPaused, SeatID: "seat_a", ResetAt: &reset},
	}}
	srv := New(Deps{DB: fakePinger{}, Admin: &fakeAdmin{}, AdminToken: "tok", Rounds: round, WorkspaceID: "ws-1",
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	req := httptest.NewRequest(http.MethodGet, "/v1/admin/tasks/status", nil)
	req.Header.Set("Authorization", "Bearer tok")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d %s", rec.Code, rec.Body.String())
	}
	var got struct {
		At          time.Time                      `json:"at"`
		WorkspaceID string                         `json:"workspace_id"`
		Tasks       map[string]dispatch.TaskStatus `json:"tasks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if p := got.Tasks["t_paused"]; !got.At.Equal(tasksNow) || got.WorkspaceID != "ws-1" || p.Status != "paused" || p.ResetAt == nil || !p.ResetAt.Equal(reset) {
		t.Fatalf("body = %s", rec.Body.String())
	}
	if rec := adminRequest(t, &fakeAdmin{}, "tok", http.MethodGet, "/v1/admin/tasks/status", "Bearer tok"); rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 without the dispatcher", rec.Code)
	}
}

type recordingAssigner struct {
	taskID, userID string
	err            error
}

func (r *recordingAssigner) AssignCreator(_ context.Context, taskID, userID string) (string, error) {
	r.taskID, r.userID = taskID, userID
	return dispatch.AssignDone, r.err
}

func TestAssignCreatorEndpoint(t *testing.T) {
	assigner := &recordingAssigner{}
	srv := New(Deps{DB: fakePinger{}, Admin: &fakeAdmin{}, AdminToken: "tok", Assigner: assigner,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	call := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/admin/tasks/t_1/assign-creator", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer tok")
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec
	}
	if rec := call(`{"user_id":"user-a"}`); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"result":"assigned"`) {
		t.Fatalf("assign = %d %s", rec.Code, rec.Body.String())
	}
	if assigner.taskID != "t_1" || assigner.userID != "user-a" {
		t.Fatalf("assigner got %+v", assigner)
	}
	if rec := call(`{"user_id":" "}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("blank user = %d", rec.Code)
	}
	assigner.err = &kandev.APIError{Status: 404, Message: "task not found"}
	if rec := call(`{"user_id":"user-a"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("missing task = %d", rec.Code)
	}
}
