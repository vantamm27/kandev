// Package kandev is fleetd's client for the Kandev backend REST and
// WebSocket APIs. Request and response shapes mirror
// apps/backend/internal/task/handlers and internal/orchestrator; the tests pin
// them so a backend change shows up here.
package kandev

import "time"

// ExecutorTypeSSH is Kandev's executor type for a remote SSH host.
const ExecutorTypeSSH = "ssh"

// SSH executor config keys, from internal/agent/runtime/lifecycle/executor_backend.go.
const (
	ConfigSSHHost            = "ssh_host"
	ConfigSSHPort            = "ssh_port"
	ConfigSSHUser            = "ssh_user"
	ConfigSSHHostFingerprint = "ssh_host_fingerprint"
	ConfigSSHIdentitySource  = "ssh_identity_source"
	ConfigSSHIdentityFile    = "ssh_identity_file"
	ConfigSSHProxyJump       = "ssh_proxy_jump"
	ConfigSSHWorkdirRoot     = "ssh_workdir_root"
)

// Executor is the subset of Kandev's ExecutorDTO fleetd uses.
type Executor struct {
	ID          string            `json:"id"`
	OwnerUserID string            `json:"owner_user_id"`
	Name        string            `json:"name"`
	Type        string            `json:"type"`
	Status      string            `json:"status"`
	Resumable   bool              `json:"resumable"`
	Config      map[string]string `json:"config,omitempty"`
}

// CreateExecutorInput is the POST /api/v1/executors body.
type CreateExecutorInput struct {
	OwnerUserID string            `json:"owner_user_id,omitempty"`
	Name        string            `json:"name"`
	Type        string            `json:"type"`
	Status      string            `json:"status,omitempty"`
	Resumable   *bool             `json:"resumable,omitempty"`
	Config      map[string]string `json:"config,omitempty"`
}

// UpdateExecutorInput is the PATCH /api/v1/executors/:id body. Nil fields are
// left unchanged.
type UpdateExecutorInput struct {
	Name   *string           `json:"name,omitempty"`
	Status *string           `json:"status,omitempty"`
	Config map[string]string `json:"config,omitempty"`
}

// ExecutorProfile is the subset of Kandev's ExecutorProfileDTO fleetd uses.
type ExecutorProfile struct {
	ID          string `json:"id"`
	ExecutorID  string `json:"executor_id"`
	OwnerUserID string `json:"owner_user_id"`
	Name        string `json:"name"`
}

// CreateExecutorProfileInput is the POST /api/v1/executors/:id/profiles body.
type CreateExecutorProfileInput struct {
	OwnerUserID   string            `json:"owner_user_id,omitempty"`
	Name          string            `json:"name"`
	McpPolicy     string            `json:"mcp_policy"`
	Config        map[string]string `json:"config,omitempty"`
	PrepareScript string            `json:"prepare_script"`
	CleanupScript string            `json:"cleanup_script"`
}

// Workspace is the subset of a Kandev workspace fleetd reads.
type Workspace struct {
	ID                    string `json:"id"`
	Name                  string `json:"name"`
	DefaultAgentProfileID string `json:"default_agent_profile_id,omitempty"`
}

// ProfileEnvVar is one environment variable of an agent profile; fleetd
// reads only the key.
type ProfileEnvVar struct {
	Key string `json:"key"`
}

// AgentProfile is the subset of a Kandev agent profile fleetd checks.
type AgentProfile struct {
	ID                     string          `json:"id"`
	Name                   string          `json:"name"`
	AgentName              string          `json:"-"`
	EnvVars                []ProfileEnvVar `json:"env_vars,omitempty"`
	ProviderBaseURL        string          `json:"provider_base_url,omitempty"`
	ProviderAPIKeySecretID string          `json:"provider_api_key_secret_id,omitempty"`
}

// Kandev task states fleetd acts on, from pkg/api/v1/task.go.
const (
	TaskStateReview    = "REVIEW"
	TaskStateCompleted = "COMPLETED"
	TaskStateCancelled = "CANCELLED"
	TaskStateFailed    = "FAILED"
)

// Kandev session states fleetd acts on: a session that ended in an error,
// one that was stopped, one that was prepared but never sent a prompt, and
// the states of a session whose agent is alive.
const (
	SessionStateFailed          = "FAILED"
	SessionStateCancelled       = "CANCELLED"
	SessionStateCreated         = "CREATED"
	SessionStateStarting        = "STARTING"
	SessionStateRunning         = "RUNNING"
	SessionStateWaitingForInput = "WAITING_FOR_INPUT"
)

// TaskDone reports whether a task in state needs no more work from its seat.
func TaskDone(state string) bool {
	switch state {
	case TaskStateReview, TaskStateCompleted, TaskStateCancelled:
		return true
	default:
		return false
	}
}

// Task is the subset of Kandev's TaskDTO fleetd uses.
type Task struct {
	ID                  string    `json:"id"`
	WorkspaceID         string    `json:"workspace_id"`
	WorkflowID          string    `json:"workflow_id"`
	WorkflowStepID      string    `json:"workflow_step_id"`
	Title               string    `json:"title"`
	Description         string    `json:"description"`
	State               string    `json:"state"`
	Priority            string    `json:"priority"`
	AssigneeUserID      string    `json:"assignee_user_id,omitempty"`
	PrimarySessionID    *string   `json:"primary_session_id,omitempty"`
	PrimarySessionState *string   `json:"primary_session_state,omitempty"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
	// PrimaryAgentProfileID is the agent profile the task was created with.
	PrimaryAgentProfileID string `json:"primary_agent_profile_id,omitempty"`
}

// PrimarySessionLive reports whether the task's primary session has a live
// agent.
func (t Task) PrimarySessionLive() bool {
	return t.PrimarySessionIs(SessionStateStarting) || t.PrimarySessionIs(SessionStateRunning) ||
		t.PrimarySessionIs(SessionStateWaitingForInput)
}

// PrimarySessionIs reports whether the task's primary session is in state.
func (t Task) PrimarySessionIs(state string) bool {
	return t.PrimarySessionState != nil && *t.PrimarySessionState == state
}

// ListTasksOptions filters GET /api/v1/workspaces/:id/tasks.
type ListTasksOptions struct {
	WorkflowID string
	Page       int
	PageSize   int
}

// TaskPage is one page of workspace tasks.
type TaskPage struct {
	Tasks []Task `json:"tasks"`
	Total int    `json:"total"`
}

// Session launch intents, from internal/orchestrator/session_launch.go.
const (
	IntentStart   = "start"
	IntentResume  = "resume"
	IntentPrepare = "prepare"
)

// LaunchSessionInput is the session.launch WS payload.
type LaunchSessionInput struct {
	TaskID            string `json:"task_id"`
	Intent            string `json:"intent,omitempty"`
	SessionID         string `json:"session_id,omitempty"`
	AgentProfileID    string `json:"agent_profile_id,omitempty"`
	ExecutorID        string `json:"executor_id,omitempty"`
	ExecutorProfileID string `json:"executor_profile_id,omitempty"`
	Prompt            string `json:"prompt,omitempty"`
	Priority          string `json:"priority,omitempty"`
}

// LaunchSessionResult is the session.launch response payload.
type LaunchSessionResult struct {
	Success          bool   `json:"success"`
	InProgress       bool   `json:"in_progress,omitempty"`
	TaskID           string `json:"task_id"`
	SessionID        string `json:"session_id,omitempty"`
	AgentExecutionID string `json:"agent_execution_id,omitempty"`
	AgentProfileID   string `json:"agent_profile_id,omitempty"`
	State            string `json:"state"`
}

// OnEnterAutoStartAgent is the workflow step action that starts the agent
// when a task enters the step.
const OnEnterAutoStartAgent = "auto_start_agent"

// StepAction is one action of a workflow step event.
type StepAction struct {
	Type string `json:"type"`
}

// StepEvents holds the workflow step events fleetd checks.
type StepEvents struct {
	OnEnter []StepAction `json:"on_enter,omitempty"`
}

// WorkflowStep is the subset of Kandev's workflow step fleetd checks.
type WorkflowStep struct {
	ID     string     `json:"id"`
	Name   string     `json:"name"`
	Events StepEvents `json:"events"`
}

// StartsAgentOnEnter reports whether entering the step starts the agent.
func (s WorkflowStep) StartsAgentOnEnter() bool {
	for _, a := range s.Events.OnEnter {
		if a.Type == OnEnterAutoStartAgent {
			return true
		}
	}
	return false
}
