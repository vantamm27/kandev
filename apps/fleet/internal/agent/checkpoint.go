package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
)

// CheckpointOptions controls one checkpoint push.
type CheckpointOptions struct {
	Worktree string
	TaskID   string
	Remote   string
	// SkipCI sends the ci.skip push option so GitLab does not start a
	// pipeline for a WIP ref.
	SkipCI bool
}

// CheckpointResult says what a checkpoint did.
type CheckpointResult struct {
	Committed bool
	Ref       string
	Commit    string
}

var taskIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// WIPRef is the ref a task's work in progress is pushed to.
func WIPRef(taskID string) string { return "refs/agent-wip/" + taskID }

// Checkpoint commits any uncommitted work in the worktree and force-pushes
// HEAD to refs/agent-wip/<task>. It runs no model, so it works while the
// seat is at its usage limit. Commit hooks still run.
func Checkpoint(ctx context.Context, opts CheckpointOptions) (CheckpointResult, error) {
	if !taskIDPattern.MatchString(opts.TaskID) {
		return CheckpointResult{}, fmt.Errorf("invalid task ID %q", opts.TaskID)
	}
	if opts.Remote == "" {
		opts.Remote = "origin"
	}
	git := func(args ...string) (string, error) { return runGit(ctx, opts.Worktree, args...) }
	status, err := git("status", "--porcelain")
	if err != nil {
		return CheckpointResult{}, err
	}
	res := CheckpointResult{Ref: WIPRef(opts.TaskID)}
	if strings.TrimSpace(status) != "" {
		if _, err := git("add", "--all"); err != nil {
			return CheckpointResult{}, err
		}
		if _, err := git("commit", "--message", "wip(kandev): checkpoint "+opts.TaskID); err != nil {
			return CheckpointResult{}, err
		}
		res.Committed = true
	}
	head, err := git("rev-parse", "HEAD")
	if err != nil {
		return CheckpointResult{}, err
	}
	res.Commit = strings.TrimSpace(head)
	push := []string{"push", "--force"}
	if opts.SkipCI {
		push = append(push, "--push-option=ci.skip")
	}
	push = append(push, opts.Remote, "HEAD:"+res.Ref)
	if _, err := git(push...); err != nil {
		return CheckpointResult{}, err
	}
	return res, nil
}

func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return "", fmt.Errorf("git %s: %s", args[0], strings.TrimSpace(stderr.String()))
		}
		return "", fmt.Errorf("git %s: %w", args[0], err)
	}
	return stdout.String(), nil
}
