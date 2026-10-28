package server

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/kandev/kandev/apps/fleet/internal/dispatch"
	"github.com/kandev/kandev/apps/fleet/internal/kandev"
	"github.com/kandev/kandev/apps/fleet/internal/store"
)

// RoundReader reports the dispatcher's last round for the admin API.
type RoundReader interface {
	LastRound() dispatch.Stats
}

// Recent tasks are those running or finished within recentTaskWindow, newest
// first, at most maxRecentTasks of them.
const (
	recentTaskWindow = 7 * 24 * time.Hour
	maxRecentTasks   = 100
)

type taskView struct {
	TaskID      string     `json:"task_id"`
	Title       string     `json:"title,omitempty"`
	SeatID      string     `json:"seat_id"`
	OwnerUserID string     `json:"owner_user_id"`
	Attempt     int        `json:"attempt"`
	Outcome     string     `json:"outcome"`
	LastError   string     `json:"last_error,omitempty"`
	ClaimedAt   time.Time  `json:"claimed_at"`
	FinishedAt  *time.Time `json:"finished_at,omitempty"`
	// DurationSeconds runs from launch to finish, or to now while the task
	// runs; a failed launch has none.
	DurationSeconds *float64 `json:"duration_seconds,omitempty"`
}

type seatTaskCounts struct {
	SeatID      string         `json:"seat_id"`
	OwnerUserID string         `json:"owner_user_id"`
	Outcomes    map[string]int `json:"outcomes"`
}

type queueView struct {
	At      *time.Time             `json:"at,omitempty"`
	Queued  map[string]int         `json:"queued"`
	Waiting []dispatch.WaitingTask `json:"waiting"`
}

// TaskOwnerAssigner assigns a new task to the member who created it.
type TaskOwnerAssigner interface {
	AssignCreator(ctx context.Context, taskID, userID string) (string, error)
}

// handleTaskStatuses reports the dispatcher's workspace and what fleetd does
// with each task it queued or claimed, as of the last round.
func (s *Server) handleTaskStatuses(w http.ResponseWriter, _ *http.Request) {
	if s.deps.Rounds == nil {
		writeError(w, http.StatusNotFound, "the dispatcher is disabled")
		return
	}
	round := s.deps.Rounds.LastRound()
	body := map[string]any{"workspace_id": s.deps.WorkspaceID, "tasks": round.Tasks}
	if round.Tasks == nil {
		body["tasks"] = map[string]dispatch.TaskStatus{}
	}
	if !round.At.IsZero() {
		body["at"] = round.At
	}
	writeJSON(w, http.StatusOK, body)
}

// handleAssignCreator assigns an unassigned task in the fleet workspace to
// the user who created it.
func (s *Server) handleAssignCreator(w http.ResponseWriter, r *http.Request) {
	if s.deps.Assigner == nil {
		writeError(w, http.StatusNotFound, "the dispatcher is disabled")
		return
	}
	var body struct {
		UserID string `json:"user_id"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.UserID) == "" {
		writeError(w, http.StatusBadRequest, "user_id is required")
		return
	}
	id := r.PathValue("id")
	result, err := s.deps.Assigner.AssignCreator(r.Context(), id, body.UserID)
	switch {
	case kandev.IsNotFound(err):
		writeError(w, http.StatusNotFound, "task not found")
	case err != nil:
		s.deps.Log.Warn("assigning a task to its creator failed", "task_id", id, "error", err)
		writeError(w, http.StatusBadGateway, "kandev: "+err.Error())
	default:
		if result == dispatch.AssignDone {
			s.deps.Log.Info("task assigned to its creator", "task_id", id, "user_id", body.UserID)
		}
		writeJSON(w, http.StatusOK, map[string]string{"task_id": id, "result": result})
	}
}

// handleListTasks reports the queue as of the last round, recent tasks, and
// task counts per seat and outcome.
func (s *Server) handleListTasks(w http.ResponseWriter, r *http.Request) {
	if s.deps.TaskOutcomes == nil {
		writeError(w, http.StatusNotFound, "task tracking is disabled")
		return
	}
	tasks, err := s.deps.TaskOutcomes.ListTaskOutcomes(r.Context())
	if err != nil {
		s.deps.Log.Error("list task outcomes failed", "error", err)
		writeError(w, http.StatusInternalServerError, "list tasks failed")
		return
	}
	queue := queueView{Queued: map[string]int{}, Waiting: []dispatch.WaitingTask{}}
	var titles map[string]string
	if s.deps.Rounds != nil {
		round := s.deps.Rounds.LastRound()
		if !round.At.IsZero() {
			queue.At = &round.At
		}
		if round.Queued != nil {
			queue.Queued = round.Queued
		}
		if round.WaitingTasks != nil {
			queue.Waiting = round.WaitingTasks
		}
		titles = round.Titles
	}
	now := s.now()
	writeJSON(w, http.StatusOK, map[string]any{
		"queue":   queue,
		"recent":  recentTasks(tasks, titles, now),
		"by_seat": countBySeat(tasks),
	})
}

func recentTasks(tasks []store.TaskOutcome, titles map[string]string, now time.Time) []taskView {
	out := []taskView{}
	// tasks come oldest first; walk back from the newest.
	for i := len(tasks) - 1; i >= 0 && len(out) < maxRecentTasks; i-- {
		t := tasks[i]
		if end, ended := t.End(); ended && now.Sub(end) >= recentTaskWindow {
			continue
		}
		v := taskView{
			TaskID: t.TaskID, Title: titles[t.TaskID], SeatID: t.SeatID, OwnerUserID: t.OwnerUserID,
			Attempt: t.Attempt, Outcome: t.Outcome(), LastError: t.LastError, ClaimedAt: t.ClaimedAt, FinishedAt: t.FinishedAt,
		}
		if d, ok := t.Duration(now); ok {
			secs := d.Seconds()
			v.DurationSeconds = &secs
		}
		out = append(out, v)
	}
	return out
}

func countBySeat(tasks []store.TaskOutcome) []seatTaskCounts {
	bySeat := map[string]*seatTaskCounts{}
	for _, t := range tasks {
		c, ok := bySeat[t.SeatID]
		if !ok {
			c = &seatTaskCounts{SeatID: t.SeatID, OwnerUserID: t.OwnerUserID, Outcomes: map[string]int{}}
			bySeat[t.SeatID] = c
		}
		c.Outcomes[t.Outcome()]++
	}
	out := make([]seatTaskCounts, 0, len(bySeat))
	for _, c := range bySeat {
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SeatID < out[j].SeatID })
	return out
}
