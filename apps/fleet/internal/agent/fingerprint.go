// Package agent implements fleet-agent, the per-seat process on a node.
package agent

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
)

// DefaultHostKeyFile is the node's ed25519 host public key on macOS and Linux.
const DefaultHostKeyFile = "/etc/ssh/ssh_host_ed25519_key.pub"

// HostKeyFingerprint returns the OpenSSH SHA256 fingerprint
// ("SHA256:<unpadded base64>") of the public key in an authorized_keys or
// *.pub line, the same value `ssh-keygen -lf` prints and Kandev pins.
func HostKeyFingerprint(pubLine string) (string, error) {
	fields := strings.Fields(pubLine)
	if len(fields) < 2 {
		return "", errors.New("host key line must be '<type> <base64> [comment]'")
	}
	blob, err := base64.StdEncoding.DecodeString(fields[1])
	if err != nil {
		return "", fmt.Errorf("decode host key: %w", err)
	}
	if !strings.HasPrefix(string(blobType(blob)), fields[0]) {
		return "", fmt.Errorf("host key blob type does not match %q", fields[0])
	}
	sum := sha256.Sum256(blob)
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:]), nil
}

// HostKeyFingerprintFile reads a *.pub file and fingerprints it.
func HostKeyFingerprintFile(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read host key: %w", err)
	}
	return HostKeyFingerprint(string(raw))
}

// blobType reads the length-prefixed key type at the start of an SSH wire
// format public key.
func blobType(blob []byte) []byte {
	if len(blob) < 4 {
		return nil
	}
	n := int(blob[0])<<24 | int(blob[1])<<16 | int(blob[2])<<8 | int(blob[3])
	if n <= 0 || 4+n > len(blob) {
		return nil
	}
	return blob[4 : 4+n]
}
