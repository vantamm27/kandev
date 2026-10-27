package kandev

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// APIError is a non-2xx Kandev REST response.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("kandev: HTTP %d: %s", e.Status, e.Message)
}

// IsNotFound reports whether err is a Kandev 404.
func IsNotFound(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound
}

// Client calls the Kandev REST API with a personal access token.
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

const defaultRequestTimeout = 30 * time.Second

// maxErrorBody bounds how much of an error response is kept in APIError.
const maxErrorBody = 4 << 10

// NewClient returns a client for baseURL (for example http://kandev:38429).
func NewClient(baseURL, token string) *Client {
	return &Client{baseURL: baseURL, token: token, http: &http.Client{Timeout: defaultRequestTimeout}}
}

// CreateExecutor creates an executor.
func (c *Client) CreateExecutor(ctx context.Context, in CreateExecutorInput) (Executor, error) {
	var out Executor
	err := c.do(ctx, http.MethodPost, "/api/v1/executors", in, &out)
	return out, err
}

// UpdateExecutor patches an executor.
func (c *Client) UpdateExecutor(ctx context.Context, id string, in UpdateExecutorInput) (Executor, error) {
	var out Executor
	err := c.do(ctx, http.MethodPatch, "/api/v1/executors/"+url.PathEscape(id), in, &out)
	return out, err
}

// DeleteExecutor deletes an executor.
func (c *Client) DeleteExecutor(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/api/v1/executors/"+url.PathEscape(id), nil, nil)
}

// CreateExecutorProfile creates a profile under an executor.
func (c *Client) CreateExecutorProfile(ctx context.Context, executorID string, in CreateExecutorProfileInput) (ExecutorProfile, error) {
	var out ExecutorProfile
	err := c.do(ctx, http.MethodPost, "/api/v1/executors/"+url.PathEscape(executorID)+"/profiles", in, &out)
	return out, err
}

// GetWorkspace reads one workspace.
func (c *Client) GetWorkspace(ctx context.Context, id string) (Workspace, error) {
	var out Workspace
	err := c.do(ctx, http.MethodGet, "/api/v1/workspaces/"+url.PathEscape(id), nil, &out)
	return out, err
}

// GetWorkflowStep reads one workflow step.
func (c *Client) GetWorkflowStep(ctx context.Context, id string) (WorkflowStep, error) {
	var out struct {
		Step WorkflowStep `json:"step"`
	}
	err := c.do(ctx, http.MethodGet, "/api/v1/workflow/steps/"+url.PathEscape(id), nil, &out)
	return out.Step, err
}

// GetAgentProfile finds an agent profile by ID in Kandev's agent list.
func (c *Client) GetAgentProfile(ctx context.Context, id string) (AgentProfile, error) {
	var out struct {
		Agents []struct {
			Name     string         `json:"name"`
			Profiles []AgentProfile `json:"profiles"`
		} `json:"agents"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/v1/agents", nil, &out); err != nil {
		return AgentProfile{}, err
	}
	for _, a := range out.Agents {
		for _, p := range a.Profiles {
			if p.ID == id {
				p.AgentName = a.Name
				return p, nil
			}
		}
	}
	return AgentProfile{}, &APIError{Status: http.StatusNotFound, Message: "agent profile " + id + " not found"}
}

// GetTask reads one task.
func (c *Client) GetTask(ctx context.Context, id string) (Task, error) {
	var out Task
	err := c.do(ctx, http.MethodGet, "/api/v1/tasks/"+url.PathEscape(id), nil, &out)
	return out, err
}

// ListWorkspaceTasks reads one page of a workspace's tasks.
func (c *Client) ListWorkspaceTasks(ctx context.Context, workspaceID string, opts ListTasksOptions) (TaskPage, error) {
	q := url.Values{}
	if opts.WorkflowID != "" {
		q.Set("workflow_id", opts.WorkflowID)
	}
	if opts.Page > 0 {
		q.Set("page", strconv.Itoa(opts.Page))
	}
	if opts.PageSize > 0 {
		q.Set("page_size", strconv.Itoa(opts.PageSize))
	}
	path := "/api/v1/workspaces/" + url.PathEscape(workspaceID) + "/tasks"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	var out TaskPage
	err := c.do(ctx, http.MethodGet, path, nil, &out)
	return out, err
}

// ResetTaskEnvironment tears down a task's environment so the next launch
// builds a fresh one. Kandev refuses it while a session is starting or running.
func (c *Client) ResetTaskEnvironment(ctx context.Context, taskID string) error {
	return c.do(ctx, http.MethodPost, "/api/v1/tasks/"+url.PathEscape(taskID)+"/environment/reset", map[string]bool{}, nil)
}

// SetTaskAssignee sets the human assignee; an empty userID unassigns.
func (c *Client) SetTaskAssignee(ctx context.Context, taskID, userID string) (Task, error) {
	var out Task
	body := map[string]*string{"assignee_user_id": &userID}
	err := c.do(ctx, http.MethodPatch, "/api/v1/tasks/"+url.PathEscape(taskID), body, &out)
	return out, err
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("kandev: encode %s %s: %w", method, path, err)
		}
		reader = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return fmt.Errorf("kandev: build %s %s: %w", method, path, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("kandev: %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return decodeAPIError(resp)
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("kandev: decode %s %s: %w", method, path, err)
	}
	return nil
}

func decodeAPIError(resp *http.Response) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	var body struct {
		Error string `json:"error"`
	}
	message := string(bytes.TrimSpace(raw))
	if json.Unmarshal(raw, &body) == nil && body.Error != "" {
		message = body.Error
	}
	if message == "" {
		message = http.StatusText(resp.StatusCode)
	}
	return &APIError{Status: resp.StatusCode, Message: message}
}
