package agent

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"golang.org/x/crypto/ssh"
)

// errHostKeySeen ends a probe handshake once the host key is known.
var errHostKeySeen = errors.New("host key observed")

// NegotiatedHostKeyFingerprint connects to the sshd at addr with the default
// golang.org/x/crypto client configuration, the one Kandev's SSH executor
// uses, and returns the fingerprint of the host key that handshake selects.
// A host usually has several host keys and the client's algorithm order picks
// one, so reading a fixed key file can pin a key Kandev never sees.
func NegotiatedHostKeyFingerprint(ctx context.Context, addr string) (string, error) {
	d := net.Dialer{Timeout: 5 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return "", fmt.Errorf("connect to sshd: %w", err)
	}
	defer func() { _ = conn.Close() }()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else {
		_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
	}
	var fingerprint string
	cfg := &ssh.ClientConfig{
		User: "fleet-host-key-probe",
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			fingerprint = ssh.FingerprintSHA256(key)
			return errHostKeySeen
		},
	}
	_, _, _, err = ssh.NewClientConn(conn, addr, cfg)
	if fingerprint != "" {
		return fingerprint, nil
	}
	return "", fmt.Errorf("ssh handshake with %s: %w", addr, err)
}
