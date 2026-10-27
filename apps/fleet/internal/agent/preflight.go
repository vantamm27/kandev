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

// DefaultChecks are the checks `fleet-agent preflight` runs on a node. shell
// is the seat user's login shell, which sshd uses for Kandev's commands.
func DefaultChecks(home, shell string, sshPort int) []Check {
	return []Check{
		{Name: "claude", Run: checkBinary("claude")},
		{Name: "ssh-env", Run: checkSSHEnv(home, shell, runtime.GOOS)},
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

// sshDefaultPath is the PATH sshd gives a non-interactive macOS session.
const sshDefaultPath = "/usr/bin:/bin:/usr/sbin:/sbin"

const sshClaudeOnPath = `command -v claude >/dev/null 2>&1 || { echo "claude not found in the SSH session PATH; extend PATH in the shell startup file"; exit 1; }
`

// sshEnvScript reports the first missing piece of a usable SSH session. On
// macOS the login keychain cannot be read over SSH, so the subscription login
// must come from a `claude setup-token` token exported by the shell startup
// files; elsewhere Claude Code's credentials file works too.
func sshEnvScript(goos string) string {
	login := `[ -n "$CLAUDE_CODE_OAUTH_TOKEN" ] || { echo "CLAUDE_CODE_OAUTH_TOKEN is not set in SSH sessions; run claude setup-token and export it from the shell startup file"; exit 1; }
`
	if goos != "darwin" {
		login = `[ -n "$CLAUDE_CODE_OAUTH_TOKEN" ] || [ -f "$HOME/.claude/.credentials.json" ] || { echo "no Claude login for SSH sessions; run claude login, or export a claude setup-token token from the shell startup file"; exit 1; }
`
	}
	return sshClaudeOnPath + login + sshNoAPIBilling + "command -v claude"
}

// sshNoAPIBilling fails when the SSH session would send Claude Code to API
// billing or a gateway instead of the seat's subscription login.
const sshNoAPIBilling = `for v in ANTHROPIC_API_KEY ANTHROPIC_AUTH_TOKEN ANTHROPIC_BASE_URL; do
  eval "val=\${$v:-}"
  [ -z "$val" ] || { echo "$v is set in SSH sessions; remove it so Claude Code uses the seat's subscription"; exit 1; }
done
`

// checkSSHEnv runs the login shell the way sshd runs a remote command, with
// only sshd's variables, so the parent environment cannot hide a gap.
func checkSSHEnv(home, shell, goos string) func(context.Context) CheckResult {
	return func(ctx context.Context) CheckResult {
		out, err := sshShellCommand(ctx, home, shell, sshEnvScript(goos)).Output()
		detail := strings.TrimSpace(string(out))
		if err != nil {
			if detail == "" {
				detail = shell + ": " + err.Error()
			}
			return CheckResult{Detail: detail}
		}
		return CheckResult{OK: true, Detail: "claude at " + detail + ", subscription login available"}
	}
}

// sshShellCommand runs script in the login shell the way sshd runs a remote
// command, with only sshd's variables. bash reads ~/.bashrc for a
// non-interactive command only when SSH_CLIENT/SSH_CONNECTION show that sshd
// started it.
func sshShellCommand(ctx context.Context, home, shell, script string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, shell, "-c", script)
	cmd.Dir = home
	cmd.Env = []string{
		"HOME=" + home, "SHELL=" + shell, "PATH=" + sshDefaultPath,
		"SSH_CLIENT=127.0.0.1 0 22", "SSH_CONNECTION=127.0.0.1 0 127.0.0.1 22",
	}
	return cmd
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
