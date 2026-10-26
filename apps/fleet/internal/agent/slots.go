package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
)

// SlotCounter reports how many agent sessions this seat is running.
type SlotCounter interface {
	Running(ctx context.Context) (int, error)
}

// DefaultAgentctlPattern matches the command line of the agentctl that Kandev's
// SSH executor installs at ~/.kandev/bin/agentctl, and not other agentctl
// binaries the seat user may run, such as a local Kandev install.
const DefaultAgentctlPattern = `^[^ ]*/\.kandev/bin/agentctl( |$)`

// ProcessSlotCounter counts this user's SSH-executor agentctl processes:
// Kandev starts one per session on the node.
type ProcessSlotCounter struct {
	// UID is the seat user's numeric ID.
	UID int
	// Pattern is an extended regular expression over the full command line;
	// empty means DefaultAgentctlPattern.
	Pattern string
}

// Running implements SlotCounter with pgrep.
func (c ProcessSlotCounter) Running(ctx context.Context) (int, error) {
	pattern := c.Pattern
	if pattern == "" {
		pattern = DefaultAgentctlPattern
	}
	out, err := exec.CommandContext(ctx, "pgrep", "-U", strconv.Itoa(c.UID), "-f", pattern).Output()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return 0, nil // pgrep exits 1 when nothing matches
	}
	if err != nil {
		return 0, fmt.Errorf("pgrep: %w", err)
	}
	return bytes.Count(bytes.TrimSpace(out), []byte("\n")) + 1, nil
}

// FreeSlots is capacity minus running sessions, never negative.
func FreeSlots(capacity, running int) int {
	if free := capacity - running; free > 0 {
		return free
	}
	return 0
}
