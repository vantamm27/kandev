package kandev

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

type recordedRequest struct {
	Method, Path, RawQuery, Auth, ContentType string
	Body                                      map[string]any
}

func restServer(t *testing.T, status int, response string) (*Client, *recordedRequest) {
	t.Helper()
	rec := &recordedRequest{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.Method, rec.Path, rec.RawQuery = r.Method, r.URL.Path, r.URL.RawQuery
		rec.Auth, rec.ContentType = r.Header.Get("Authorization"), r.Header.Get("Content-Type")
		if raw, _ := io.ReadAll(r.Body); len(raw) > 0 {
			if err := json.Unmarshal(raw, &rec.Body); err != nil {
				t.Errorf("request body is not JSON: %s", raw)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, response)
	}))
	t.Cleanup(srv.Close)
	return NewClient(srv.URL, "kandev_pat_test"), rec
}

func TestCreateExecutorSendsSSHConfig(t *testing.T) {
	client, rec := restServer(t, http.StatusOK, `{"id":"exec-1","name":"seat-a@mini-1","type":"ssh","status":"active","resumable":true}`)
	resumable := true
	got, err := client.CreateExecutor(context.Background(), CreateExecutorInput{
		OwnerUserID: "user-a", Name: "seat-a@mini-1", Type: ExecutorTypeSSH, Resumable: &resumable,
		Config: map[string]string{ConfigSSHHost: "10.0.0.5", ConfigSSHUser: "seat-a", ConfigSSHHostFingerprint: "SHA256:abc"},
	})
	if err != nil {
		t.Fatalf("CreateExecutor: %v", err)
	}
	if got.ID != "exec-1" || got.Type != "ssh" || !got.Resumable {
		t.Fatalf("decoded executor = %+v", got)
	}
	if rec.Method != http.MethodPost || rec.Path != "/api/v1/executors" {
		t.Fatalf("request = %s %s", rec.Method, rec.Path)
	}
	if rec.Auth != "Bearer kandev_pat_test" || rec.ContentType != "application/json" {
		t.Fatalf("headers: auth=%q content-type=%q", rec.Auth, rec.ContentType)
	}
	config, _ := rec.Body["config"].(map[string]any)
	if rec.Body["owner_user_id"] != "user-a" || rec.Body["type"] != "ssh" || rec.Body["resumable"] != true ||
		config["ssh_host"] != "10.0.0.5" || config["ssh_host_fingerprint"] != "SHA256:abc" {
		t.Fatalf("body = %+v", rec.Body)
	}
}

func TestUpdateAndDeleteExecutorPaths(t *testing.T) {
	client, rec := restServer(t, http.StatusOK, `{"id":"exec/1","status":"disabled"}`)
	status := "disabled"
	if _, err := client.UpdateExecutor(context.Background(), "exec/1", UpdateExecutorInput{Status: &status}); err != nil {
		t.Fatalf("UpdateExecutor: %v", err)
	}
	if rec.Method != http.MethodPatch || rec.Path != "/api/v1/executors/exec/1" || rec.Body["status"] != "disabled" {
		t.Fatalf("update request = %s %s %+v", rec.Method, rec.Path, rec.Body)
	}
	if _, present := rec.Body["name"]; present {
		t.Fatal("nil fields must be omitted from a PATCH")
	}

	client, rec = restServer(t, http.StatusOK, `{"success":true}`)
	if err := client.DeleteExecutor(context.Background(), "exec-1"); err != nil {
		t.Fatalf("DeleteExecutor: %v", err)
	}
	if rec.Method != http.MethodDelete || rec.Path != "/api/v1/executors/exec-1" || rec.Body != nil {
		t.Fatalf("delete request = %s %s %+v", rec.Method, rec.Path, rec.Body)
	}
}

func TestCreateExecutorProfile(t *testing.T) {
	client, rec := restServer(t, http.StatusOK, `{"id":"prof-1","executor_id":"exec-1","name":"seat-a"}`)
	got, err := client.CreateExecutorProfile(context.Background(), "exec-1", CreateExecutorProfileInput{Name: "seat-a", OwnerUserID: "user-a"})
	if err != nil {
		t.Fatalf("CreateExecutorProfile: %v", err)
	}
	if got.ID != "prof-1" || rec.Method != http.MethodPost || rec.Path != "/api/v1/executors/exec-1/profiles" {
		t.Fatalf("got %+v via %s %s", got, rec.Method, rec.Path)
	}
	if rec.Body["name"] != "seat-a" || rec.Body["owner_user_id"] != "user-a" {
		t.Fatalf("body = %+v", rec.Body)
	}
}

func TestTaskReadsAndAssignee(t *testing.T) {
	client, rec := restServer(t, http.StatusOK, `{"id":"task-1","priority":"high","assignee_user_id":"user-a","primary_session_id":"s-1"}`)
	task, err := client.GetTask(context.Background(), "task-1")
	if err != nil || task.Priority != "high" || task.AssigneeUserID != "user-a" || task.PrimarySessionID == nil || *task.PrimarySessionID != "s-1" {
		t.Fatalf("GetTask = %+v, %v", task, err)
	}
	if rec.Method != http.MethodGet || rec.Path != "/api/v1/tasks/task-1" {
		t.Fatalf("request = %s %s", rec.Method, rec.Path)
	}

	client, rec = restServer(t, http.StatusOK, `{"id":"task-1"}`)
	if _, err := client.SetTaskAssignee(context.Background(), "task-1", ""); err != nil {
		t.Fatalf("SetTaskAssignee: %v", err)
	}
	if rec.Method != http.MethodPatch || rec.Path != "/api/v1/tasks/task-1" {
		t.Fatalf("request = %s %s", rec.Method, rec.Path)
	}
	if value, present := rec.Body["assignee_user_id"]; !present || value != "" {
		t.Fatalf("unassign must send an explicit empty assignee, body = %+v", rec.Body)
	}

	client, rec = restServer(t, http.StatusOK, `{"success":true}`)
	if err := client.ResetTaskEnvironment(context.Background(), "task-1"); err != nil {
		t.Fatalf("ResetTaskEnvironment: %v", err)
	}
	if rec.Method != http.MethodPost || rec.Path != "/api/v1/tasks/task-1/environment/reset" {
		t.Fatalf("request = %s %s", rec.Method, rec.Path)
	}

	client, rec = restServer(t, http.StatusOK, `{"tasks":[{"id":"a"},{"id":"b"}],"total":7}`)
	page, err := client.ListWorkspaceTasks(context.Background(), "ws-1", ListTasksOptions{WorkflowID: "wf-1", Page: 2, PageSize: 50})
	if err != nil || len(page.Tasks) != 2 || page.Total != 7 {
		t.Fatalf("ListWorkspaceTasks = %+v, %v", page, err)
	}
	if rec.Path != "/api/v1/workspaces/ws-1/tasks" || rec.RawQuery != "page=2&page_size=50&workflow_id=wf-1" {
		t.Fatalf("request = %s?%s", rec.Path, rec.RawQuery)
	}
}

func TestAPIErrors(t *testing.T) {
	client, _ := restServer(t, http.StatusNotFound, `{"error":"task not found"}`)
	_, err := client.GetTask(context.Background(), "missing")
	if !IsNotFound(err) || err.Error() != "kandev: HTTP 404: task not found" {
		t.Fatalf("err = %v", err)
	}

	client, _ = restServer(t, http.StatusBadGateway, "upstream down")
	_, err = client.GetTask(context.Background(), "x")
	if IsNotFound(err) || err.Error() != "kandev: HTTP 502: upstream down" {
		t.Fatalf("err = %v", err)
	}

	client, _ = restServer(t, http.StatusInternalServerError, "")
	_, err = client.GetTask(context.Background(), "x")
	if err == nil || err.Error() != "kandev: HTTP 500: Internal Server Error" {
		t.Fatalf("err = %v", err)
	}
}

func TestGetWorkspaceReadsTheDefaultAgentProfile(t *testing.T) {
	client, rec := restServer(t, http.StatusOK, `{"id":"ws-1","name":"fleet","default_agent_profile_id":"agent-1"}`)
	ws, err := client.GetWorkspace(context.Background(), "ws-1")
	if err != nil || ws.ID != "ws-1" || ws.DefaultAgentProfileID != "agent-1" {
		t.Fatalf("GetWorkspace = %+v, %v", ws, err)
	}
	if rec.Method != http.MethodGet || rec.Path != "/api/v1/workspaces/ws-1" {
		t.Fatalf("request = %s %s", rec.Method, rec.Path)
	}
}

func TestGetWorkflowStepReadsOnEnterActions(t *testing.T) {
	client, rec := restServer(t, http.StatusOK,
		`{"step":{"id":"step-1","name":"Plan","events":{"on_enter":[{"type":"enable_plan_mode"},{"type":"auto_start_agent"}]}}}`)
	step, err := client.GetWorkflowStep(context.Background(), "step-1")
	if err != nil || step.ID != "step-1" || step.Name != "Plan" || !step.StartsAgentOnEnter() {
		t.Fatalf("GetWorkflowStep = %+v, %v", step, err)
	}
	if rec.Method != http.MethodGet || rec.Path != "/api/v1/workflow/steps/step-1" {
		t.Fatalf("request = %s %s", rec.Method, rec.Path)
	}
	if (WorkflowStep{}).StartsAgentOnEnter() {
		t.Fatal("a step without actions does not start the agent")
	}
}

func TestGetAgentProfileFindsItAcrossAgents(t *testing.T) {
	body := `{"agents":[{"name":"codex-acp","profiles":[{"id":"p-codex","name":"Codex"}]},` +
		`{"name":"claude-acp","profiles":[{"id":"p-1","name":"team","env_vars":[{"key":"ANTHROPIC_API_KEY","secret_id":"s"}],"provider_base_url":"https://gw"}]}]}`
	client, rec := restServer(t, http.StatusOK, body)
	p, err := client.GetAgentProfile(context.Background(), "p-1")
	if err != nil || p.ID != "p-1" || p.AgentName != "claude-acp" || len(p.EnvVars) != 1 || p.EnvVars[0].Key != "ANTHROPIC_API_KEY" || p.ProviderBaseURL != "https://gw" {
		t.Fatalf("GetAgentProfile = %+v, %v", p, err)
	}
	if rec.Path != "/api/v1/agents" {
		t.Fatalf("path = %s", rec.Path)
	}
	client, _ = restServer(t, http.StatusOK, body)
	if _, err := client.GetAgentProfile(context.Background(), "missing"); !IsNotFound(err) {
		t.Fatalf("missing profile err = %v; want not found", err)
	}
}
