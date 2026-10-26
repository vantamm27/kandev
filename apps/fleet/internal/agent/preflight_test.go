package agent

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunChecksAggregates(t *testing.T) {
	pass := func(context.Context) CheckResult { return CheckResult{OK: true} }
	skip := func(context.Context) CheckResult { return CheckResult{Skipped: true} }
	fail := func(context.Context) CheckResult { return CheckResult{Detail: "nope"} }
	if results, ok := RunChecks(context.Background(), []Check{{"a", pass}, {"b", skip}}); !ok || results[1].Name != "b" {
		t.Fatalf("pass+skip = %v %+v", ok, results)
	}
	if _, ok := RunChecks(context.Background(), []Check{{"a", pass}, {"c", fail}}); ok {
		t.Fatal("a failing check must fail preflight")
	}
}

func TestSleepSettingParsing(t *testing.T) {
	disabled := []byte("System-wide power settings:\nCurrently in use:\n standby              0\n sleep                0 (sleep prevented by sharingd)\n displaysleep         10\n")
	if r := sleepSettingResult(disabled); !r.OK {
		t.Fatalf("disabled = %+v", r)
	}
	enabled := []byte("Currently in use:\n sleep                1\n displaysleep         10\n")
	if r := sleepSettingResult(enabled); r.OK {
		t.Fatalf("enabled = %+v", r)
	}
	if r := sleepSettingResult([]byte("nothing here")); r.OK {
		t.Fatal("missing setting must not pass")
	}
}

func TestTCPAndDiskChecks(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if r := checkTCP(addr)(context.Background()); !r.OK {
		t.Fatalf("listening = %+v", r)
	}
	_ = ln.Close()
	if r := checkTCP(addr)(context.Background()); r.OK {
		t.Fatal("closed port must fail")
	}
	dir := t.TempDir()
	if r := checkDisk(dir, 0)(context.Background()); !r.OK {
		t.Fatalf("zero minimum = %+v", r)
	}
	if r := checkDisk(dir, 1<<62)(context.Background()); r.OK {
		t.Fatal("an impossible minimum must fail")
	}
	if r := checkBinary("fleet-no-such-binary")(context.Background()); r.OK {
		t.Fatal("missing binary must fail")
	}
}

func TestSSHEnvCheckUsesTheShellStartupFilesOnly(t *testing.T) {
	zsh, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("zsh not installed")
	}
	home := t.TempDir()
	bin := filepath.Join(home, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "from-the-parent-process")

	r := checkSSHEnv(home, zsh, "darwin")(context.Background())
	if r.OK || !strings.Contains(r.Detail, "claude not found") {
		t.Fatalf("without startup files = %+v", r)
	}

	zshenv := filepath.Join(home, ".zshenv")
	if err := os.WriteFile(zshenv, []byte("export PATH=\""+bin+":$PATH\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r = checkSSHEnv(home, zsh, "darwin")(context.Background())
	if r.OK || !strings.Contains(r.Detail, "CLAUDE_CODE_OAUTH_TOKEN") {
		t.Fatalf("PATH only = %+v", r)
	}

	if err := os.WriteFile(zshenv, []byte("export PATH=\""+bin+":$PATH\"\nexport CLAUDE_CODE_OAUTH_TOKEN=secret-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r = checkSSHEnv(home, zsh, "darwin")(context.Background())
	if !r.OK || strings.Contains(r.Detail, "secret-value") {
		t.Fatalf("PATH and token = %+v", r)
	}
}

func TestSSHEnvCheckAcceptsTheCredentialsFileOffMacOS(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not installed")
	}
	home := t.TempDir()
	bin := filepath.Join(home, "bin")
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// sh reads no startup file here, so put claude on sshd's default PATH via a
	// wrapper shell that extends PATH first.
	wrapper := filepath.Join(home, "shell")
	if err := os.WriteFile(wrapper, []byte("#!/bin/sh\nPATH=\""+bin+":$PATH\" exec "+sh+" \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if r := checkSSHEnv(home, wrapper, "linux")(context.Background()); r.OK {
		t.Fatalf("no login at all = %+v", r)
	}
	if err := os.WriteFile(filepath.Join(home, ".claude", ".credentials.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if r := checkSSHEnv(home, wrapper, "linux")(context.Background()); !r.OK {
		t.Fatalf("credentials file on linux = %+v", r)
	}
	if r := checkSSHEnv(home, wrapper, "darwin")(context.Background()); r.OK {
		t.Fatal("macOS must still require the token: the keychain is not readable over SSH")
	}
}

func TestSSHEnvCheckLooksLikeAnSSHSession(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not installed")
	}
	home := t.TempDir()
	bin := filepath.Join(home, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Like bash, this shell reads its startup file only when sshd started it.
	wrapper := filepath.Join(home, "shell")
	script := "#!/bin/sh\nif [ -n \"$SSH_CLIENT\" ] && [ -n \"$SSH_CONNECTION\" ]; then export CLAUDE_CODE_OAUTH_TOKEN=x; fi\n" +
		"PATH=\"" + bin + ":$PATH\" exec " + sh + " \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if r := checkSSHEnv(home, wrapper, "linux")(context.Background()); !r.OK {
		t.Fatalf("shell started like sshd = %+v", r)
	}
}

func TestSSHEnvCheckRejectsAPIBillingVariables(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not installed")
	}
	home := t.TempDir()
	bin := filepath.Join(home, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL"} {
		wrapper := filepath.Join(home, "shell-"+key)
		script := "#!/bin/sh\nexport CLAUDE_CODE_OAUTH_TOKEN=x " + key + "=y\nPATH=\"" + bin + ":$PATH\" exec " + sh + " \"$@\"\n"
		if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		r := checkSSHEnv(home, wrapper, "linux")(context.Background())
		if r.OK || !strings.Contains(r.Detail, key) || strings.Contains(r.Detail, "=y") {
			t.Errorf("%s set = %+v; want a failure naming the variable, not its value", key, r)
		}
	}
}
