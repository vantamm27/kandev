package handlers

import (
	"context"
	"strings"

	"github.com/kandev/kandev/internal/auth/authn"
	"github.com/kandev/kandev/internal/task/models"
)

// mcpAssigneeSelf is the create_task_kandev alias for the authenticated caller.
const mcpAssigneeSelf = "me"

// resolveMCPAssigneeUserID expands the "me" alias to the caller's user ID.
// Without a real caller identity (auth disabled, or an internal caller) the
// alias resolves to no assignee, matching the hidden human-assignee surfaces in
// single-user installs. Any other value passes through for service validation.
func resolveMCPAssigneeUserID(ctx context.Context, requested string) string {
	requested = strings.TrimSpace(requested)
	if requested != mcpAssigneeSelf {
		return requested
	}
	identity, ok := authn.IdentityFromContext(ctx)
	if !ok || identity.Synthetic {
		return ""
	}
	return identity.UserID
}

// validateMCPCreateTaskPriority rejects an unknown priority up front so the
// caller gets a validation error naming the value instead of a generic
// create failure. Empty selects the service default.
func validateMCPCreateTaskPriority(priority string) error {
	if priority == "" {
		return nil
	}
	return models.ValidateTaskPriority(priority)
}
