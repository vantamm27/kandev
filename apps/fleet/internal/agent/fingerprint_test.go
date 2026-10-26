package agent

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestHostKeyFingerprintMatchesSSHKeygen checks against the reference tool so
// the value matches what Kandev pins and `ssh` prints.
func TestHostKeyFingerprintMatchesSSHKeygen(t *testing.T) {
	keygen, err := exec.LookPath("ssh-keygen")
	if err != nil {
		t.Skip("ssh-keygen not installed")
	}
	dir := t.TempDir()
	key := filepath.Join(dir, "host_ed25519")
	if out, err := exec.Command(keygen, "-q", "-t", "ed25519", "-N", "", "-C", "host comment", "-f", key).CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen: %v: %s", err, out)
	}
	out, err := exec.Command(keygen, "-l", "-E", "sha256", "-f", key+".pub").Output()
	if err != nil {
		t.Fatalf("ssh-keygen -l: %v", err)
	}
	want := strings.Fields(string(out))[1]

	got, err := HostKeyFingerprintFile(key + ".pub")
	if err != nil {
		t.Fatalf("HostKeyFingerprintFile: %v", err)
	}
	if got != want {
		t.Fatalf("fingerprint = %s, ssh-keygen says %s", got, want)
	}
}

func TestHostKeyFingerprintRejectsBadInput(t *testing.T) {
	for name, line := range map[string]string{
		"empty":         "",
		"one field":     "ssh-ed25519",
		"not base64":    "ssh-ed25519 !!!",
		"type mismatch": "ssh-rsa AAAAC3NzaC1lZDI1NTE5AAAAIGr1Zl6JjJ6m4V7p2CVrX1JQ0Nqgk8L8U1t0Gm0Yh7aX",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := HostKeyFingerprint(line); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
	if _, err := HostKeyFingerprintFile(filepath.Join(t.TempDir(), "missing.pub")); err == nil {
		t.Fatal("missing file must error")
	}
}
