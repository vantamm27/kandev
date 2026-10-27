package agent

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// SlotCounter reports how many agent sessions this seat is running.
type SlotCounter interface {
	Running(ctx context.Context) (int, error)
}

// DefaultAgentctlPattern matches the command line of the agentctl that Kandev's
// SSH executor installs at ~/.kandev/bin/agentctl, and not other agentctl
// binaries the seat user may run, such as a local Kandev install.
const DefaultAgentctlPattern = `^[^ ]*/\.kandev/bin/agentctl( |$)`

// ProcessSlotCounter counts this user's SSH-executor agentctl processes that
// hold a session: Kandev starts one agentctl per session on the node, but it
// also restarts one when someone views a finished task's files or terminal.
// An agentctl counts while it runs a non-shell child (the agent) or while it
// is younger than StartupGrace, before its agent has started.
type ProcessSlotCounter struct {
	// UID is the seat user's numeric ID.
	UID int
	// Pattern is an extended regular expression over the full command line;
	// empty means DefaultAgentctlPattern.
	Pattern string
}

// StartupGrace is how long a new agentctl counts before its agent appears.
const StartupGrace = 2 * time.Minute

// shells are the programs agentctl runs for user terminals, which do not
// occupy a session slot.
var shells = map[string]bool{"sh": true, "bash": true, "zsh": true, "dash": true, "fish": true, "ksh": true, "tcsh": true, "csh": true}

// Running implements SlotCounter with ps.
func (c ProcessSlotCounter) Running(ctx context.Context) (int, error) {
	pattern := c.Pattern
	if pattern == "" {
		pattern = DefaultAgentctlPattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return 0, fmt.Errorf("agentctl pattern: %w", err)
	}
	out, err := exec.CommandContext(ctx, "ps", "-U", strconv.Itoa(c.UID), "-o", "pid=,ppid=,etime=,args=").Output()
	if err != nil {
		return 0, fmt.Errorf("ps: %w", err)
	}
	return countBusyAgentctl(parseProcesses(out), re), nil
}

type process struct {
	pid, ppid int
	age       time.Duration
	args      string
}

func parseProcesses(out []byte) []process {
	var procs []process
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}
		pid, err1 := strconv.Atoi(f[0])
		ppid, err2 := strconv.Atoi(f[1])
		age, err3 := parseElapsed(f[2])
		if err1 != nil || err2 != nil || err3 != nil {
			continue
		}
		procs = append(procs, process{pid: pid, ppid: ppid, age: age, args: strings.Join(f[3:], " ")})
	}
	return procs
}

func countBusyAgentctl(procs []process, agentctl *regexp.Regexp) int {
	agentChild := map[int]bool{}
	for _, p := range procs {
		if !shells[strings.TrimPrefix(filepath.Base(strings.Fields(p.args)[0]), "-")] {
			agentChild[p.ppid] = true
		}
	}
	busy := 0
	for _, p := range procs {
		if agentctl.MatchString(p.args) && (agentChild[p.pid] || p.age < StartupGrace) {
			busy++
		}
	}
	return busy
}

// parseElapsed reads ps etime, [[dd-]hh:]mm:ss.
func parseElapsed(raw string) (time.Duration, error) {
	days := 0
	if d, rest, ok := strings.Cut(raw, "-"); ok {
		n, err := strconv.Atoi(d)
		if err != nil {
			return 0, err
		}
		days, raw = n, rest
	}
	total := 0
	for _, part := range strings.Split(raw, ":") {
		n, err := strconv.Atoi(part)
		if err != nil {
			return 0, err
		}
		total = total*60 + n
	}
	return time.Duration(days)*24*time.Hour + time.Duration(total)*time.Second, nil
}

// FreeSlots is capacity minus running sessions, never negative.
func FreeSlots(capacity, running int) int {
	if free := capacity - running; free > 0 {
		return free
	}
	return 0
}
