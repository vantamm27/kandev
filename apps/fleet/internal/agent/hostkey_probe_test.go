package agent

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"net"
	"testing"

	"golang.org/x/crypto/ssh"
)

// startSSHServer serves SSH handshakes with the given host keys and rejects
// every login, which is all a host key probe needs.
func startSSHServer(t *testing.T, signers ...ssh.Signer) string {
	t.Helper()
	cfg := &ssh.ServerConfig{PasswordCallback: func(ssh.ConnMetadata, []byte) (*ssh.Permissions, error) {
		return nil, errors.New("no logins")
	}}
	for _, s := range signers {
		cfg.AddHostKey(s)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				_, _, _, _ = ssh.NewServerConn(conn, cfg)
			}()
		}
	}()
	return ln.Addr().String()
}

func TestNegotiatedHostKeyFingerprintMatchesADefaultGoClient(t *testing.T) {
	_, edKey, _ := ed25519.GenerateKey(rand.Reader)
	ecKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	edSigner, _ := ssh.NewSignerFromKey(edKey)
	ecSigner, _ := ssh.NewSignerFromKey(ecKey)
	addr := startSSHServer(t, edSigner, ecSigner)

	// What Kandev's executor sees: a default client config.
	var seen string
	_, _ = ssh.Dial("tcp", addr, &ssh.ClientConfig{User: "x", HostKeyCallback: func(_ string, _ net.Addr, k ssh.PublicKey) error {
		seen = ssh.FingerprintSHA256(k)
		return nil
	}})
	got, err := NegotiatedHostKeyFingerprint(context.Background(), addr)
	if err != nil {
		t.Fatal(err)
	}
	if got == "" || got != seen {
		t.Fatalf("probe = %q, default client saw %q", got, seen)
	}
	if got == ssh.FingerprintSHA256(edSigner.PublicKey()) {
		t.Log("note: the default client picked ed25519 on this x/crypto version")
	}
}

func TestNegotiatedHostKeyFingerprintFailsWithoutSSH(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	if _, err := NegotiatedHostKeyFingerprint(context.Background(), addr); err == nil {
		t.Fatal("a closed port must fail")
	}
}
