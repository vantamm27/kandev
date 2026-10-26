package handlers

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kandev/kandev/internal/auth/authn"
	ws "github.com/kandev/kandev/pkg/websocket"
)

func createTaskForAssigneeTest(t *testing.T, ctx context.Context, extra map[string]interface{}) *ws.Message {
	t.Helper()
	svc, repo := newTestTaskService(t)
	workspaces, err := svc.ListWorkspaces(context.Background())
	require.NoError(t, err)
	workflows, err := svc.ListWorkflows(context.Background(), workspaces[0].ID, false)
	require.NoError(t, err)
	h := NewHandlers(svc, nil, nil, nil, nil, repo, repo, nil, nil, nil, nil, nil, testLogger(t))

	payload := map[string]interface{}{
		"workspace_id":     workspaces[0].ID,
		"workflow_id":      workflows[0].ID,
		"title":            "Task",
		"description":      "Do the thing",
		"agent_profile_id": "profile-1",
		"start_agent":      false,
	}
	for k, v := range extra {
		payload[k] = v
	}
	resp, err := h.handleCreateTask(mcpTestExternalContext(ctx), makeWSMessage(t, ws.ActionMCPCreateTask, payload))
	require.NoError(t, err)
	require.NotNil(t, resp)
	return resp
}

func decodeCreatedTask(t *testing.T, resp *ws.Message) map[string]interface{} {
	t.Helper()
	if resp.Type == ws.MessageTypeError {
		t.Fatalf("create task returned error: %s", string(resp.Payload))
	}
	var result map[string]interface{}
	require.NoError(t, json.Unmarshal(resp.Payload, &result))
	return result
}

func TestHandleCreateTask_PriorityAndExplicitAssignee(t *testing.T) {
	resp := createTaskForAssigneeTest(t, context.Background(), map[string]interface{}{
		"priority":         "high",
		"assignee_user_id": "user-7",
	})

	result := decodeCreatedTask(t, resp)
	require.Equal(t, "high", result["priority"])
	require.Equal(t, "user-7", result["assignee_user_id"])
}

func TestHandleCreateTask_AssigneeMeResolvesToCaller(t *testing.T) {
	ctx := authn.WithIdentity(context.Background(), authn.Identity{UserID: "user-42", Role: authn.RoleAdmin})
	resp := createTaskForAssigneeTest(t, ctx, map[string]interface{}{"assignee_user_id": "me"})

	result := decodeCreatedTask(t, resp)
	require.Equal(t, "user-42", result["assignee_user_id"])
	require.Equal(t, "medium", result["priority"])
}

func TestHandleCreateTask_AssigneeMeWithoutRealIdentityLeavesTaskUnassigned(t *testing.T) {
	ctx := authn.WithIdentity(context.Background(), authn.Identity{UserID: "default-user", Synthetic: true})
	resp := createTaskForAssigneeTest(t, ctx, map[string]interface{}{"assignee_user_id": "me"})

	result := decodeCreatedTask(t, resp)
	assignee, _ := result["assignee_user_id"].(string)
	require.Empty(t, assignee)
}

func TestHandleCreateTask_InvalidPriorityIsValidationError(t *testing.T) {
	resp := createTaskForAssigneeTest(t, context.Background(), map[string]interface{}{"priority": "urgent"})

	require.Equal(t, ws.MessageTypeError, resp.Type)
	require.Contains(t, string(resp.Payload), ws.ErrorCodeValidation)
	require.Contains(t, string(resp.Payload), "urgent")
}
