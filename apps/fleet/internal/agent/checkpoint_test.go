package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitCmd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// newRepo returns a worktree cloned from a bare remote that accepts push options.
func newRepo(t *testing.T) (worktree, remote string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	remote = filepath.Join(root, "remote.git")
	worktree = filepath.Join(root, "work")
	gitCmd(t, root, "init", "--quiet", "--bare", remote)
	gitCmd(t, remote, "config", "receive.advertisePushOptions", "true")
	gitCmd(t, root, "clone", "--quiet", remote, worktree)
	gitCmd(t, worktree, "config", "user.email", "agent@example.com")
	gitCmd(t, worktree, "config", "user.name", "Agent")
	gitCmd(t, worktree, "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(worktree, "README.md"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, worktree, "add", "README.md")
	gitCmd(t, worktree, "commit", "--quiet", "-m", "init")
	return worktree, remote
}

func TestCheckpointCommitsAndPushesWIPRef(t *testing.T) {
	worktree, remote := newRepo(t)
	if err := os.WriteFile(filepath.Join(worktree, "PROGRESS.md"), []byte("next: tests\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := Checkpoint(context.Background(), CheckpointOptions{Worktree: worktree, TaskID: "task-42", SkipCI: true})
	if err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	if !res.Committed || res.Ref != "refs/agent-wip/task-42" {
		t.Fatalf("result = %+v", res)
	}
	if got := gitCmd(t, remote, "rev-parse", "refs/agent-wip/task-42"); got != res.Commit {
		t.Fatalf("remote ref = %s, want %s", got, res.Commit)
	}
	if msg := gitCmd(t, worktree, "log", "-1", "--format=%s"); msg != "wip(kandev): checkpoint task-42" {
		t.Fatalf("commit message = %q", msg)
	}

	again, err := Checkpoint(context.Background(), CheckpointOptions{Worktree: worktree, TaskID: "task-42"})
	if err != nil || again.Committed || again.Commit != res.Commit {
		t.Fatalf("clean checkpoint = %+v, %v; want a push with no new commit", again, err)
	}
}

func TestCheckpointRejectsBadTaskIDAndReportsGitErrors(t *testing.T) {
	worktree, _ := newRepo(t)
	for _, id := range []string{"", "../main", "a b", "x;rm -rf"} {
		if _, err := Checkpoint(context.Background(), CheckpointOptions{Worktree: worktree, TaskID: id}); err == nil {
			t.Fatalf("task ID %q must be rejected", id)
		}
	}
	_, err := Checkpoint(context.Background(), CheckpointOptions{Worktree: worktree, TaskID: "task-1", Remote: "nowhere"})
	if err == nil || !strings.Contains(err.Error(), "git push") {
		t.Fatalf("bad remote = %v", err)
	}
}
