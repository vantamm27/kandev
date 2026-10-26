package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"
)

func TestFreeSlots(t *testing.T) {
	for _, tc := range []struct{ capacity, running, want int }{{2, 0, 2}, {2, 1, 1}, {2, 2, 0}, {2, 5, 0}} {
		if got := FreeSlots(tc.capacity, tc.running); got != tc.want {
			t.Fatalf("FreeSlots(%d, %d) = %d, want %d", tc.capacity, tc.running, got, tc.want)
		}
	}
}

func TestDefaultAgentctlPatternMatchesOnlyTheSSHExecutorBinary(t *testing.T) {
	re := regexp.MustCompile(DefaultAgentctlPattern)
	for cmd, want := range map[string]bool{
		"/Users/seat/.kandev/bin/agentctl -port=40001":                                 true,
		"/Users/seat/.kandev/bin/agentctl":                                             true,
		"/opt/homebrew/lib/node_modules/kandev/bin/agentctl -port=39429":               false,
		"sh -c ~/.kandev/bin/agentctl -port=40001":                                     false,
		"/Users/seat/.kandev/bin/agentctl.sha256":                                      false,
		"/Users/seat/.kandev/bin/agentctl-old -port=1":                                 false,
		"/opt/homebrew/lib/node_modules/@kdlbs/runtime-darwin-arm64/bin/agentctl -p=1": false,
	} {
		if got := re.MatchString(cmd); got != want {
			t.Errorf("match(%q) = %v, want %v", cmd, got, want)
		}
	}
}

func TestProcessSlotCounterCountsMatchingCommandLines(t *testing.T) {
	sleepPath, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("sleep not installed")
	}
	if _, err := exec.LookPath("pgrep"); err != nil {
		t.Skip("pgrep not installed")
	}
	// argv[0] is what pgrep -f sees, so a renamed argv[0] stands in for an
	// agentctl binary without copying one.
	dir := t.TempDir()
	start := func(argv0 string) {
		t.Helper()
		cmd := &exec.Cmd{Path: sleepPath, Args: []string{argv0, "30"}}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	}
	pattern := "^" + regexp.QuoteMeta(dir) + "/seat/bin/agentctl( |$)"
	counter := ProcessSlotCounter{UID: os.Getuid(), Pattern: pattern}
	if n, err := counter.Running(context.Background()); err != nil || n != 0 {
		t.Fatalf("no match = %d, %v", n, err)
	}
	start(filepath.Join(dir, "seat", "bin", "agentctl"))
	start(filepath.Join(dir, "seat", "bin", "agentctl"))
	start(filepath.Join(dir, "other", "bin", "agentctl"))
	n, err := counter.Running(context.Background())
	if err != nil || n != 2 {
		t.Fatalf("running = %d, %v; want 2", n, err)
	}
}
