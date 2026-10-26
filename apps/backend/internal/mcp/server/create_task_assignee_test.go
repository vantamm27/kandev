package mcp

import (
	"testing"

	ws "github.com/kandev/kandev/pkg/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateTaskForwardsPriorityAndAssignee(t *testing.T) {
	for _, mode := range []string{ModeTask, ModeExternal} {
		t.Run(mode, func(t *testing.T) {
			backend := &testBackend{response: map[string]any{"id": "task"}}
			s := New(backend, "", "", 10005, newTestLogger(t), "", false, mode)
			result := callTool(t, s, "create_task_kandev", map[string]any{
				"title": "Task", "repository_id": "repo", "start_agent": false,
				"priority": "critical", "assignee_user_id": "me",
			})
			require.False(t, result.IsError)
			assert.Equal(t, ws.ActionMCPCreateTask, backend.lastAction)
			payload, ok := backend.lastPayload.(map[string]any)
			require.True(t, ok)
			assert.Equal(t, "critical", payload["priority"])
			assert.Equal(t, "me", payload["assignee_user_id"])
		})
	}
}

func TestCreateTaskOmitsUnsetPriorityAndAssignee(t *testing.T) {
	backend := &testBackend{response: map[string]any{"id": "task"}}
	s := New(backend, "", "", 10005, newTestLogger(t), "", false, ModeExternal)
	result := callTool(t, s, "create_task_kandev", map[string]any{
		"title": "Task", "repository_id": "repo", "start_agent": false,
	})
	require.False(t, result.IsError)
	payload, ok := backend.lastPayload.(map[string]any)
	require.True(t, ok)
	assert.NotContains(t, payload, "priority")
	assert.NotContains(t, payload, "assignee_user_id")
}

func TestCreateTaskRejectsUnknownPriority(t *testing.T) {
	backend := &testBackend{response: map[string]any{"id": "task"}}
	s := New(backend, "", "", 10005, newTestLogger(t), "", false, ModeExternal)
	result := callTool(t, s, "create_task_kandev", map[string]any{
		"title": "Task", "repository_id": "repo", "start_agent": false, "priority": "urgent",
	})
	require.True(t, result.IsError)
	assert.Empty(t, backend.lastAction)
}
