package config

import (
	"testing"
	"time"
)

func lookupFrom(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		v, ok := values[key]
		return v, ok
	}
}

func TestDefaults(t *testing.T) {
	cfg, err := fromLookup(lookupFrom(nil))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.ListenAddr != ":8484" || cfg.DBPath != "fleet.db" || cfg.HeartbeatTimeout != 90*time.Second {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
	if cfg.KandevURL != "" || cfg.KandevToken != "" {
		t.Fatalf("Kandev settings must default to empty: %+v", cfg)
	}
}

func TestOverridesAndTrimming(t *testing.T) {
	cfg, err := fromLookup(lookupFrom(map[string]string{
		"FLEET_LISTEN_ADDR":       " 127.0.0.1:9000 ",
		"FLEET_DB_PATH":           "/var/lib/fleet/fleet.db",
		"FLEET_KANDEV_URL":        "https://kandev.internal/",
		"FLEET_KANDEV_TOKEN":      "kandev_pat_x",
		"FLEET_HEARTBEAT_TIMEOUT": "2m",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := Config{
		ListenAddr: "127.0.0.1:9000", DBPath: "/var/lib/fleet/fleet.db",
		KandevURL: "https://kandev.internal", KandevToken: "kandev_pat_x", HeartbeatTimeout: 2 * time.Minute,
	}
	if cfg != want {
		t.Fatalf("got %+v, want %+v", cfg, want)
	}
}

func TestRejectsInvalidValues(t *testing.T) {
	for name, values := range map[string]map[string]string{
		"bad duration":     {"FLEET_HEARTBEAT_TIMEOUT": "soon"},
		"zero duration":    {"FLEET_HEARTBEAT_TIMEOUT": "0s"},
		"non-http url":     {"FLEET_KANDEV_URL": "ftp://kandev"},
		"url without host": {"FLEET_KANDEV_URL": "http://"},
		"unparseable url":  {"FLEET_KANDEV_URL": "http://%zz"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := fromLookup(lookupFrom(values)); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}
