package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kandev/kandev/apps/fleet/internal/dispatch"
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
