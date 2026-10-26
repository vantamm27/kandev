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

// ProcessSlotCounter counts this user's agentctl processes: Kandev's SSH
// executor starts one agentctl per session on the node.
type ProcessSlotCounter struct {
	// UID is the seat user's numeric ID.
	UID int
	// Name is the process name to count; empty means "agentctl".
	Name string
}

// Running implements SlotCounter with pgrep.
func (c ProcessSlotCounter) Running(ctx context.Context) (int, error) {
	name := c.Name
	if name == "" {
		name = "agentctl"
	}
	out, err := exec.CommandContext(ctx, "pgrep", "-U", strconv.Itoa(c.UID), "-x", name).Output()
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
