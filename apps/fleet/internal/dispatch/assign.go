package dispatch

import (
	"context"

	"github.com/kandev/kandev/apps/fleet/internal/kandev"
)

// TaskAssigner reads and assigns Kandev tasks.
type TaskAssigner interface {
	GetTask(ctx context.Context, id string) (kandev.Task, error)
	SetTaskAssignee(ctx context.Context, taskID, userID string) (kandev.Task, error)
}

// Results of Assigner.AssignCreator.
const (
	AssignDone       = "assigned"
	AssignHasOwner   = "already_assigned"
	AssignOtherSpace = "not_fleet_workspace"
)

// Assigner gives a new task in the fleet workspace to the member who created
// it. Kandev does not record a task's creator, and an unassigned task has no
// seat to run on.
type Assigner struct {
	WorkspaceID string
	Tasks       TaskAssigner
}

// AssignCreator assigns the task to userID unless it is outside the fleet
// workspace or already has an assignee.
func (a Assigner) AssignCreator(ctx context.Context, taskID, userID string) (string, error) {
	t, err := a.Tasks.GetTask(ctx, taskID)
	if err != nil {
		return "", err
	}
	switch {
	case t.WorkspaceID != a.WorkspaceID:
		return AssignOtherSpace, nil
	case t.AssigneeUserID != "":
		return AssignHasOwner, nil
	}
	if _, err := a.Tasks.SetTaskAssignee(ctx, taskID, userID); err != nil {
		return "", err
	}
	return AssignDone, nil
}
