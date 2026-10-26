package agent

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"time"
)

// CheckResult is one preflight check outcome.
type CheckResult struct {
	Name    string
	OK      bool
	Skipped bool
	Detail  string
}

// Check runs one preflight check.
type Check struct {
	Name string
	Run  func(ctx context.Context) CheckResult
}

// MinFreeDiskBytes is the free space a seat needs for worktrees and builds.
const MinFreeDiskBytes = 20 << 30

// DefaultChecks are the checks `fleet-agent preflight` runs on a node.
func DefaultChecks(home string, sshPort int) []Check {
	return []Check{
		{Name: "claude", Run: checkBinary("claude")},
		{Name: "git", Run: checkBinary("git")},
		{Name: "sshd", Run: checkTCP(fmt.Sprintf("127.0.0.1:%d", sshPort))},
		{Name: "disk", Run: checkDisk(home, MinFreeDiskBytes)},
		{Name: "sleep", Run: checkSleepDisabled},
	}
}

// RunChecks runs every check and reports whether all passed or were skipped.
func RunChecks(ctx context.Context, checks []Check) ([]CheckResult, bool) {
	results := make([]CheckResult, 0, len(checks))
	ok := true
	for _, c := range checks {
		r := c.Run(ctx)
		r.Name = c.Name
		if !r.OK && !r.Skipped {
			ok = false
		}
		results = append(results, r)
	}
	return results, ok
}

func checkBinary(name string) func(context.Context) CheckResult {
	return func(context.Context) CheckResult {
		path, err := exec.LookPath(name)
		if err != nil {
			return CheckResult{Detail: name + " not found in PATH"}
		}
		return CheckResult{OK: true, Detail: path}
	}
}

func checkTCP(addr string) func(context.Context) CheckResult {
	return func(ctx context.Context) CheckResult {
		d := net.Dialer{Timeout: 3 * time.Second}
		conn, err := d.DialContext(ctx, "tcp", addr)
		if err != nil {
			return CheckResult{Detail: "cannot connect to " + addr + ": " + err.Error()}
		}
		_ = conn.Close()
		return CheckResult{OK: true, Detail: addr + " accepts connections"}
	}
}

func checkDisk(path string, min uint64) func(context.Context) CheckResult {
	return func(context.Context) CheckResult {
		var st syscall.Statfs_t
		if err := syscall.Statfs(path, &st); err != nil {
			return CheckResult{Detail: "statfs " + path + ": " + err.Error()}
		}
		free := uint64(st.Bavail) * uint64(st.Bsize) //nolint:unconvert // field types differ by OS
		detail := fmt.Sprintf("%.1f GiB free on %s", float64(free)/(1<<30), path)
		return CheckResult{OK: free >= min, Detail: detail}
	}
}

func checkSleepDisabled(ctx context.Context) CheckResult {
	if runtime.GOOS != "darwin" {
		return CheckResult{Skipped: true, Detail: "only checked on macOS"}
	}
	out, err := exec.CommandContext(ctx, "pmset", "-g").Output()
	if err != nil {
		return CheckResult{Detail: "pmset -g: " + err.Error()}
	}
	return sleepSettingResult(out)
}

// sleepSettingResult reads the system "sleep" minutes from `pmset -g` output.
func sleepSettingResult(out []byte) CheckResult {
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) >= 2 && fields[0] == "sleep" {
			if fields[1] == "0" {
				return CheckResult{OK: true, Detail: "system sleep is disabled"}
			}
			return CheckResult{Detail: "system sleep is " + fields[1] + " minutes; run sudo pmset -a sleep 0"}
		}
	}
	return CheckResult{Detail: "pmset -g did not report a sleep setting"}
}
