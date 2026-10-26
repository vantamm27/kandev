package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kandev/kandev/apps/fleet/internal/store"
)

func TestTokenCreatePrintsUsableToken(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "fleet.db")
	t.Setenv("FLEET_DB_PATH", dbPath)
	var out bytes.Buffer
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := run([]string{"token", "create", "--owner", "user-a", "--labels", "team:core, macos", "--ttl", "1h"}, &out, log); err != nil {
		t.Fatalf("run: %v", err)
	}
	raw := strings.TrimSpace(out.String())
	if !strings.HasPrefix(raw, "fleet_reg_") {
		t.Fatalf("output = %q", raw)
	}
	st, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = st.Close() }()
	tok, err := st.LookupToken(context.Background(), raw, time.Now())
	if err != nil || tok.OwnerUserID != "user-a" || strings.Join(tok.Labels, ",") != "team:core,macos" {
		t.Fatalf("token = %+v, %v", tok, err)
	}
	if _, err := st.LookupToken(context.Background(), raw, time.Now().Add(2*time.Hour)); err == nil {
		t.Fatal("token must expire after --ttl")
	}
}

func TestCommandErrors(t *testing.T) {
	t.Setenv("FLEET_DB_PATH", filepath.Join(t.TempDir(), "fleet.db"))
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	for name, args := range map[string][]string{
		"missing owner":        {"token", "create"},
		"bad ttl":              {"token", "create", "--owner", "u", "--ttl", "-1h"},
		"unknown command":      {"seats"},
		"serve without kandev": {"serve"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := run(args, io.Discard, log); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}
