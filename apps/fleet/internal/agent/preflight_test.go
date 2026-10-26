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

	r := checkSSHEnv(home, zsh)(context.Background())
	if r.OK || !strings.Contains(r.Detail, "claude not found") {
		t.Fatalf("without startup files = %+v", r)
	}

	zshenv := filepath.Join(home, ".zshenv")
	if err := os.WriteFile(zshenv, []byte("export PATH=\""+bin+":$PATH\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r = checkSSHEnv(home, zsh)(context.Background())
	if r.OK || !strings.Contains(r.Detail, "CLAUDE_CODE_OAUTH_TOKEN") {
		t.Fatalf("PATH only = %+v", r)
	}

	if err := os.WriteFile(zshenv, []byte("export PATH=\""+bin+":$PATH\"\nexport CLAUDE_CODE_OAUTH_TOKEN=secret-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r = checkSSHEnv(home, zsh)(context.Background())
	if !r.OK || strings.Contains(r.Detail, "secret-value") {
		t.Fatalf("PATH and token = %+v", r)
	}
}
