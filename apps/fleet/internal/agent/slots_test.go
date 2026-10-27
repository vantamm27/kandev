package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"
	"time"
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

func TestCountBusyAgentctlIgnoresIdleWorkspaceAgentctl(t *testing.T) {
	re := regexp.MustCompile(DefaultAgentctlPattern)
	out := []byte(`
  100     1  1-02:03:04 /home/seat/.kandev/bin/agentctl --workdir /a
  101   100       10:00 npm exec @agentclientprotocol/claude-agent-acp@0.81.2
  200     1       30:00 /home/seat/.kandev/bin/agentctl --workdir /b
  201   200       29:00 /bin/bash
  202   200       28:00 -zsh
  300     1       00:45 /home/seat/.kandev/bin/agentctl --workdir /c
  400     1    02:00:00 /opt/homebrew/bin/agentctl -port=1
  401   400    02:00:00 node server.js
`)
	// 100 runs an agent; 200 only serves terminals; 300 is still starting.
	if got := countBusyAgentctl(parseProcesses(out), re); got != 2 {
		t.Fatalf("busy agentctl = %d, want 2", got)
	}
}

func TestParseElapsed(t *testing.T) {
	for raw, want := range map[string]time.Duration{
		"00:45": 45 * time.Second, "10:00": 10 * time.Minute, "02:00:00": 2 * time.Hour,
		"1-02:03:04": 26*time.Hour + 3*time.Minute + 4*time.Second,
	} {
		if got, err := parseElapsed(raw); err != nil || got != want {
			t.Errorf("parseElapsed(%q) = %v, %v; want %v", raw, got, err, want)
		}
	}
	if _, err := parseElapsed("x:1"); err == nil {
		t.Error("garbage must be an error")
	}
}

func TestProcessSlotCounterCountsYoungAgentctlProcesses(t *testing.T) {
	sleepPath, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("sleep not installed")
	}
	// argv[0] is what ps shows, so a renamed argv[0] stands in for an
	// agentctl binary without copying one. A new process counts during
	// StartupGrace even before it has an agent child.
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
