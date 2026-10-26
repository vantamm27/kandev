package service

import (
	"context"
	"errors"
	"testing"

	"github.com/kandev/kandev/internal/task/models"
)

func TestCreateTaskPersistsHumanAssignee(t *testing.T) {
	svc, _, repo := createTestService(t)
	ctx := context.Background()
	wfID := seedWorkspaceAndWorkflowForCreate(t, ctx, repo, "ws-assignee")

	result, err := svc.CreateTask(ctx, &CreateTaskRequest{
		WorkspaceID:    "ws-assignee",
		WorkflowID:     wfID,
		Title:          "Task",
		AssigneeUserID: " user-7 ",
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if result.Task.AssigneeUserID != "user-7" {
		t.Fatalf("returned assignee = %q, want user-7", result.Task.AssigneeUserID)
	}
	stored, err := repo.GetTask(ctx, result.Task.ID)
	if err != nil {
		t.Fatalf("reload task: %v", err)
	}
	if stored.AssigneeUserID != "user-7" {
		t.Fatalf("stored assignee = %q, want user-7", stored.AssigneeUserID)
	}
}

func TestCreateTaskRejectsAssigneeOutsideWorkspace(t *testing.T) {
	svc, _, repo := createTestService(t)
	ctx := context.Background()
	if err := repo.CreateWorkspace(ctx, &models.Workspace{ID: "ws-owned", Name: "owned", OwnerID: "owner-1"}); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if err := repo.CreateWorkflow(ctx, &models.Workflow{ID: "ws-owned-wf", WorkspaceID: "ws-owned", Name: "WF"}); err != nil {
		t.Fatalf("create workflow: %v", err)
	}

	_, err := svc.CreateTask(ctx, &CreateTaskRequest{
		WorkspaceID:    "ws-owned",
		WorkflowID:     "ws-owned-wf",
		Title:          "Task",
		AssigneeUserID: "stranger",
	})
	if !errors.Is(err, ErrAssigneeCannotReachWorkspace) {
		t.Fatalf("err = %v, want ErrAssigneeCannotReachWorkspace", err)
	}
	tasks, listErr := repo.ListTasks(ctx, "ws-owned-wf")
	if listErr != nil {
		t.Fatalf("list tasks: %v", listErr)
	}
	if len(tasks) != 0 {
		t.Fatalf("a rejected assignee must not leave a task row, got %d", len(tasks))
	}
}
