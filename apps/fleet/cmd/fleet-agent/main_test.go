package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kandev/kandev/apps/fleet/internal/agent"
)

func TestRegisterSavesConfigAndLaunchAgent(t *testing.T) {
	keygen, err := exec.LookPath("ssh-keygen")
	if err != nil {
		t.Skip("ssh-keygen not installed")
	}
	dir := t.TempDir()
	key := filepath.Join(dir, "host")
	if out, err := exec.Command(keygen, "-q", "-t", "ed25519", "-N", "", "-f", key).CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen: %v %s", err, out)
	}
	var sent agent.RegisterRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&sent)
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"seat_id":"seat_9","owner_user_id":"user-a","hmac_secret":"s3cret","kandev_executor_id":"exec-9","kandev_executor_profile_id":"prof-9"}`)
	}))
	defer srv.Close()
	t.Setenv("FLEET_REGISTRATION_TOKEN", "tok-env")
	configPath := filepath.Join(dir, "cfg", "config.json")
	plistPath := filepath.Join(dir, "LaunchAgents", "com.kandev.fleet-agent.plist")
	unitPath := filepath.Join(dir, "systemd", "user", "fleet-agent.service")
	var out bytes.Buffer
	err = run([]string{"register", "--fleetd", srv.URL, "--ssh-host", "10.0.0.5", "--ssh-user", "seat-a",
		"--host-key", key + ".pub", "--labels", "macos, xcode", "--capacity", "2",
		"--config", configPath, "--launch-agent", plistPath, "--systemd-unit", unitPath}, &out)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	want, _ := agent.HostKeyFingerprintFile(key + ".pub")
	if sent.Token != "tok-env" || sent.SSHUser != "seat-a" || sent.HostKeyFingerprint != want ||
		strings.Join(sent.Labels, ",") != "macos,xcode" || sent.Capacity != 2 || sent.AgentVersion != "dev" {
		t.Fatalf("sent %+v", sent)
	}
	cfg, err := agent.LoadConfig(configPath)
	if err != nil || cfg.SeatID != "seat_9" || cfg.HMACSecret != "s3cret" || cfg.QuotaFile != filepath.Join(dir, "cfg", "quota.json") {
		t.Fatalf("config = %+v, %v", cfg, err)
	}
	if _, err := os.Stat(plistPath); err != nil {
		t.Fatalf("plist not written: %v", err)
	}
	unit, err := os.ReadFile(unitPath)
	if err != nil || !strings.Contains(string(unit), `"run" "--config" "`+configPath+`"`) {
		t.Fatalf("systemd unit = %s, %v", unit, err)
	}
	if !strings.Contains(out.String(), "systemctl --user enable --now fleet-agent.service") {
		t.Fatalf("output lacks systemd start hint: %s", out.String())
	}
	if strings.Contains(out.String(), "s3cret") {
		t.Fatal("the HMAC secret must never be printed")
	}
}

func TestRegisterRequiresFlags(t *testing.T) {
	t.Setenv("FLEET_REGISTRATION_TOKEN", "")
	for name, args := range map[string][]string{
		"no command":  {},
		"unknown":     {"fly"},
		"no fleetd":   {"register", "--token", "t", "--ssh-host", "h"},
		"no token":    {"register", "--fleetd", "http://x", "--ssh-host", "h"},
		"no host":     {"register", "--fleetd", "http://x", "--token", "t"},
		"no host key": {"register", "--fleetd", "http://x", "--token", "t", "--ssh-host", "h", "--host-key", "/nonexistent.pub"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := run(args, io.Discard); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}
