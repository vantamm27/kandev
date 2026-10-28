package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kandev/kandev/pkg/pluginsdk"
)

type fakeFleetd struct {
	seats     string
	acceptErr int
	seatErr   int
	gotSeat   string
	gotAuth   string
	gotSeatOp string
	// gotAssign is "<path> <user_id>" of the last assign-creator call.
	gotAssign string
}

func (f *fakeFleetd) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/admin/seats":
			_, _ = io.WriteString(w, f.seats)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/admin/handoffs":
			if r.URL.Query().Get("state") != "offered,exhausted" {
				t.Errorf("handoff query = %q", r.URL.RawQuery)
			}
			_, _ = io.WriteString(w, `{"handoffs":[{"id":"hoff_1","task_id":"task-1","from_seat_id":"seat_x","state":"offered","attempt":1}]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/admin/handoffs/hoff_1/accept":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.gotSeat = body["seat_id"]
			if f.acceptErr != 0 {
				w.WriteHeader(f.acceptErr)
				_, _ = io.WriteString(w, `{"error":"the handoff is no longer offered"}`)
				return
			}
			_, _ = io.WriteString(w, `{"id":"hoff_1","task_id":"task-1","to_seat_id":"`+f.gotSeat+`","state":"done"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/admin/tasks/status":
			_, _ = io.WriteString(w, `{"workspace_id":"ws-1","tasks":{"task-1":{"status":"paused","seat_id":"seat_b1","reset_at":"2026-10-28T14:20:00Z"}}}`)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/assign-creator"):
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.gotAssign = r.URL.Path + " " + body["user_id"]
			_, _ = io.WriteString(w, `{"task_id":"task-1","result":"assigned"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/admin/tasks":
			_, _ = io.WriteString(w, `{"queue":{"queued":{"low":1},"waiting":[]},"recent":[],"by_seat":[]}`)
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/v1/admin/seats/"):
			f.gotSeatOp = r.URL.RequestURI()
			if f.seatErr != 0 {
				w.WriteHeader(f.seatErr)
				_, _ = io.WriteString(w, `{"error":"seat is busy"}`)
				return
			}
			_, _ = io.WriteString(w, `{"seat_id":"seat_b1","status":"ok"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

const twoSeats = `{"seats":[
  {"id":"seat_b1","owner_user_id":"user-b","state":"online","capacity":2,"free_slots":1},
  {"id":"seat_b2","owner_user_id":"user-b","state":"online","capacity":2,"free_slots":2},
  {"id":"seat_c","owner_user_id":"user-c","state":"cooldown_week","capacity":1,"free_slots":1},
  {"id":"seat_old","owner_user_id":"user-b","state":"retired","capacity":1,"free_slots":1}]}`

func newPlugin(url string) *fleetPlugin {
	return &fleetPlugin{config: func(context.Context) (fleetdConfig, error) {
		return fleetdConfig{URL: url, AdminToken: "admin-secret"}, nil
	}}
}

func act(t *testing.T, p *fleetPlugin, key, actor, body string) (int, map[string]any) {
	t.Helper()
	resp, err := p.HandleAction(context.Background(), &pluginsdk.PluginActionRequest{
		ActionKey: key, Context: pluginsdk.VerifiedActionContext{ActorID: actor, WorkspaceID: "ws-1"}, Body: []byte(body),
	})
	if err != nil {
		t.Fatalf("HandleAction: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(resp.Body, &out); err != nil {
		t.Fatalf("response body: %v", err)
	}
	return resp.Status, out
}

func TestOverview(t *testing.T) {
	f := &fakeFleetd{seats: twoSeats}
	srv := f.server(t)
	status, out := act(t, newPlugin(srv.URL), actionOverview, "user-b", `{}`)
	if status != http.StatusOK || out["actor_id"] != "user-b" || out["can_accept"] != true {
		t.Fatalf("overview = %d %+v", status, out)
	}
	if seats := out["seats"].([]any); len(seats) != 3 {
		t.Fatalf("retired seats must be hidden, got %d", len(seats))
	}
	if f.gotAuth != "Bearer admin-secret" {
		t.Fatalf("fleetd auth = %q", f.gotAuth)
	}
	if _, out := act(t, newPlugin(srv.URL), actionOverview, "user-c", `{}`); out["can_accept"] != false {
		t.Fatal("a member whose only seat is cooling down cannot accept")
	}
}

func TestAcceptUsesTheActorsOwnSeat(t *testing.T) {
	f := &fakeFleetd{seats: twoSeats}
	srv := f.server(t)
	status, out := act(t, newPlugin(srv.URL), actionAccept, "user-b", `{"handoffId":"hoff_1","seat_id":"seat_c"}`)
	if status != http.StatusOK || out["state"] != "done" {
		t.Fatalf("accept = %d %+v", status, out)
	}
	if f.gotSeat != "seat_b2" {
		t.Fatalf("accepted onto %q; the actor's seat with the most free slots, never a body-supplied seat", f.gotSeat)
	}
}

func TestAcceptRejections(t *testing.T) {
	f := &fakeFleetd{seats: twoSeats}
	srv := f.server(t)
	if status, _ := act(t, newPlugin(srv.URL), actionAccept, "user-c", `{"handoffId":"hoff_1"}`); status != http.StatusConflict {
		t.Fatalf("no usable seat = %d", status)
	}
	if status, _ := act(t, newPlugin(srv.URL), actionAccept, "user-b", `{}`); status != http.StatusBadRequest {
		t.Fatalf("missing handoff = %d", status)
	}
	f.acceptErr = http.StatusConflict
	status, out := act(t, newPlugin(srv.URL), actionAccept, "user-b", `{"handoffId":"hoff_1"}`)
	if status != http.StatusConflict || !strings.Contains(out["error"].(string), "no longer offered") {
		t.Fatalf("taken offer = %d %+v", status, out)
	}
	f.acceptErr = http.StatusBadGateway
	if status, out := act(t, newPlugin(srv.URL), actionAccept, "user-b", `{"handoffId":"hoff_1"}`); status != http.StatusBadGateway || out["error"] != "fleetd is unavailable" {
		t.Fatalf("fleetd error = %d %+v; upstream details stay out of the browser", status, out)
	}
}

func TestUnconfiguredAndUnknown(t *testing.T) {
	p := &fleetPlugin{config: func(context.Context) (fleetdConfig, error) { return fleetdConfig{}, errNotConfigured }}
	if status, out := act(t, p, actionOverview, "user-b", `{}`); status != http.StatusServiceUnavailable || !strings.Contains(out["error"].(string), "Settings > Plugins") {
		t.Fatalf("unconfigured = %d %+v", status, out)
	}
	if status, _ := act(t, newPlugin("http://127.0.0.1:1"), "fleet.nope", "user-b", `{}`); status != http.StatusNotFound {
		t.Fatalf("unknown action = %d", status)
	}
}

func TestTasksPassesFleetdThrough(t *testing.T) {
	srv := (&fakeFleetd{}).server(t)
	status, out := act(t, newPlugin(srv.URL), actionTasks, "user-b", `{}`)
	if status != http.StatusOK || out["queue"].(map[string]any)["queued"].(map[string]any)["low"] != float64(1) {
		t.Fatalf("tasks = %d %+v", status, out)
	}
}

func TestManageSeat(t *testing.T) {
	f := &fakeFleetd{}
	srv := f.server(t)
	p := newPlugin(srv.URL)
	for op, want := range map[string]string{
		"drain": "/v1/admin/seats/seat_b1/drain", "undrain": "/v1/admin/seats/seat_b1/undrain",
		"retire": "/v1/admin/seats/seat_b1/retire", "retire_force": "/v1/admin/seats/seat_b1/retire?force=true",
	} {
		if status, _ := act(t, p, actionManageSeat, "admin-1", `{"seatId":"seat_b1","op":"`+op+`"}`); status != http.StatusOK || f.gotSeatOp != want {
			t.Fatalf("%s = %d, called %q, want %q", op, status, f.gotSeatOp, want)
		}
	}
	for _, body := range []string{`{"seatId":"seat_b1","op":"rotate-secret"}`, `{"seatId":"seat_b1","op":"check"}`, `{"seatId":"../handoffs","op":"drain"}`, `{"op":"drain"}`, `nope`} {
		if status, _ := act(t, p, actionManageSeat, "admin-1", body); status != http.StatusBadRequest {
			t.Fatalf("%s = %d, want 400", body, status)
		}
	}
	f.seatErr = http.StatusConflict
	if status, out := act(t, p, actionManageSeat, "admin-1", `{"seatId":"seat_b1","op":"retire"}`); status != http.StatusConflict || out["error"] != "seat is busy" {
		t.Fatalf("busy retire = %d %+v", status, out)
	}
}

func TestTaskStatusPassesFleetdThrough(t *testing.T) {
	srv := (&fakeFleetd{}).server(t)
	status, out := act(t, newPlugin(srv.URL), actionTaskStatus, "user-b", `{}`)
	task, _ := out["tasks"].(map[string]any)["task-1"].(map[string]any)
	if status != http.StatusOK || out["workspace_id"] != "ws-1" || task["status"] != "paused" {
		t.Fatalf("status = %d %+v", status, out)
	}
}

func TestTaskCreatedAssignsTheActor(t *testing.T) {
	f := &fakeFleetd{}
	p := newPlugin(f.server(t).URL)
	if status, out := act(t, p, actionTaskCreated, "user-b", `{"taskId":"task-1"}`); status != http.StatusOK || out["result"] != "assigned" {
		t.Fatalf("created = %d %+v", status, out)
	}
	if f.gotAssign != "/v1/admin/tasks/task-1/assign-creator user-b" {
		t.Fatalf("fleetd got %q; the assignee must be the verified actor", f.gotAssign)
	}
	for _, body := range []string{`{}`, `{"taskId":"../seats"}`, `nope`} {
		if status, _ := act(t, p, actionTaskCreated, "user-b", body); status != http.StatusBadRequest {
			t.Fatalf("%s = %d, want 400", body, status)
		}
	}
	if status, _ := act(t, p, actionTaskCreated, "", `{"taskId":"task-1"}`); status != http.StatusUnauthorized {
		t.Fatalf("anonymous = %d, want 401", status)
	}
}
