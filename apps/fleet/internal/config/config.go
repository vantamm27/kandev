// Package config loads fleetd settings from the environment.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

// Config is the fleetd runtime configuration.
type Config struct {
	// ListenAddr is the HTTP listen address for the registry API.
	ListenAddr string
	// DBPath is the SQLite database file.
	DBPath string
	// KandevURL is the Kandev backend base URL, for example http://kandev:38429.
	KandevURL string
	// KandevToken is the service user's personal access token.
	KandevToken string
	// HeartbeatTimeout marks a seat offline after this long without a heartbeat.
	HeartbeatTimeout time.Duration
}

const (
	defaultListenAddr       = ":8484"
	defaultDBPath           = "fleet.db"
	defaultHeartbeatTimeout = 90 * time.Second
)

// FromEnv reads the configuration from FLEET_* environment variables.
func FromEnv() (Config, error) {
	return fromLookup(os.LookupEnv)
}

func fromLookup(lookup func(string) (string, bool)) (Config, error) {
	get := func(key, fallback string) string {
		if v, ok := lookup(key); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
		return fallback
	}
	cfg := Config{
		ListenAddr:  get("FLEET_LISTEN_ADDR", defaultListenAddr),
		DBPath:      get("FLEET_DB_PATH", defaultDBPath),
		KandevURL:   strings.TrimRight(get("FLEET_KANDEV_URL", ""), "/"),
		KandevToken: get("FLEET_KANDEV_TOKEN", ""),
	}
	timeout, err := time.ParseDuration(get("FLEET_HEARTBEAT_TIMEOUT", defaultHeartbeatTimeout.String()))
	if err != nil {
		return Config{}, fmt.Errorf("FLEET_HEARTBEAT_TIMEOUT: %w", err)
	}
	if timeout <= 0 {
		return Config{}, errors.New("FLEET_HEARTBEAT_TIMEOUT must be positive")
	}
	cfg.HeartbeatTimeout = timeout
	if cfg.KandevURL != "" {
		u, err := url.Parse(cfg.KandevURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return Config{}, fmt.Errorf("FLEET_KANDEV_URL must be an http(s) URL, got %q", cfg.KandevURL)
		}
	}
	return cfg, nil
}
