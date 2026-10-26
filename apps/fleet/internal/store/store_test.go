package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func openTestStore(t *testing.T, path string) *Store {
	t.Helper()
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestOpenCreatesSchemaWithOwnerOnlyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "fleet.db")
	s := openTestStore(t, path)

	for _, table := range []string{"seats", "registration_tokens", "dispatch_claims", "handoffs"} {
		var n int
		if err := s.DB().QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&n); err != nil || n != 1 {
			t.Fatalf("table %s missing (n=%d, err=%v)", table, n, err)
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("database permissions = %o, want 600", perm)
	}
	if err := s.Ping(context.Background()); err != nil {
		t.Fatalf("ping: %v", err)
	}
}

func TestOpenIsIdempotentAcrossRestarts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fleet.db")
	first, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	if _, err := first.DB().Exec(`INSERT INTO registration_tokens (token_hash, owner_user_id, expires_at, created_at)
		VALUES ('h', 'user-a', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	second := openTestStore(t, path)
	var versions, tokens int
	if err := second.DB().QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&versions); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if versions != len(migrations) {
		t.Fatalf("schema_migrations rows = %d, want %d", versions, len(migrations))
	}
	if err := second.DB().QueryRow(`SELECT COUNT(*) FROM registration_tokens`).Scan(&tokens); err != nil || tokens != 1 {
		t.Fatalf("data must survive reopen (tokens=%d, err=%v)", tokens, err)
	}
}

func TestSeatEndpointIsUnique(t *testing.T) {
	s := openTestStore(t, filepath.Join(t.TempDir(), "fleet.db"))
	insert := `INSERT INTO seats (id, owner_user_id, ssh_host, ssh_user, host_key_fingerprint, hmac_secret, state, created_at, updated_at)
		VALUES (?, 'user-a', 'mini-1', 'seat-a', 'SHA256:x', 's', 'online', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`
	if _, err := s.DB().Exec(insert, "seat-1"); err != nil {
		t.Fatalf("first seat: %v", err)
	}
	if _, err := s.DB().Exec(insert, "seat-2"); err == nil {
		t.Fatal("a second seat on the same host, port, and user must be rejected")
	}
}
