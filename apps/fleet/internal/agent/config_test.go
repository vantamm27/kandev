package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigRoundTripIsOwnerOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.json")
	in := Config{FleetdURL: "http://fleetd:8484", SeatID: "seat_1", HMACSecret: "s", Capacity: 2, QuotaFile: "/tmp/q.json"}
	if err := SaveConfig(path, in); err != nil {
		t.Fatalf("save: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("perm = %v, %v", info.Mode().Perm(), err)
	}
	out, err := LoadConfig(path)
	if err != nil || out != in {
		t.Fatalf("load = %+v, %v", out, err)
	}
}

func TestLoadConfigRejectsUnsafeOrIncompleteFiles(t *testing.T) {
	dir := t.TempDir()
	open := filepath.Join(dir, "open.json")
	if err := os.WriteFile(open, []byte(`{"fleetd_url":"x","seat_id":"s","hmac_secret":"h"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(open); err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Fatalf("world-readable config = %v", err)
	}
	partial := filepath.Join(dir, "partial.json")
	if err := os.WriteFile(partial, []byte(`{"fleetd_url":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(partial); err == nil {
		t.Fatal("incomplete config must error")
	}
	defaulted := filepath.Join(dir, "defaulted.json")
	if err := os.WriteFile(defaulted, []byte(`{"fleetd_url":"x","seat_id":"s","hmac_secret":"h"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if cfg, err := LoadConfig(defaulted); err != nil || cfg.Capacity != 1 {
		t.Fatalf("capacity default = %+v, %v", cfg, err)
	}
}
