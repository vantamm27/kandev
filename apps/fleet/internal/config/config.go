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
	// SSHIdentitySource is how Kandev authenticates to seats: "agent" or "file".
	SSHIdentitySource string
	// SSHIdentityFile is the key path on the Kandev host for source "file".
	SSHIdentityFile string
	// AdminToken guards the /v1/admin API; empty disables it.
	AdminToken string
	// MetricsToken, when set, is required to scrape /metrics.
	MetricsToken string
	// WorkspaceID and IntakeStepID select the tasks fleetd dispatches; with
	// either empty the dispatcher does not run.
	WorkspaceID  string
	IntakeStepID string
	// AgentProfileID is the Kandev agent profile dispatched sessions run;
	// empty means the workspace's default agent profile.
	AgentProfileID string
	// DispatchInterval is the time between dispatch rounds.
	DispatchInterval time.Duration
}

const (
	defaultListenAddr       = ":8484"
	defaultDBPath           = "fleet.db"
	defaultHeartbeatTimeout = 90 * time.Second
	defaultDispatchInterval = 20 * time.Second
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

		SSHIdentitySource: get("FLEET_SSH_IDENTITY_SOURCE", ""),
		SSHIdentityFile:   get("FLEET_SSH_IDENTITY_FILE", ""),
		AdminToken:        get("FLEET_ADMIN_TOKEN", ""),
		MetricsToken:      get("FLEET_METRICS_TOKEN", ""),
		WorkspaceID:       get("FLEET_WORKSPACE_ID", ""),
		IntakeStepID:      get("FLEET_INTAKE_STEP_ID", ""),
		AgentProfileID:    get("FLEET_AGENT_PROFILE_ID", ""),
	}
	interval, err := time.ParseDuration(get("FLEET_DISPATCH_INTERVAL", defaultDispatchInterval.String()))
	if err != nil || interval < time.Second {
		return Config{}, fmt.Errorf("FLEET_DISPATCH_INTERVAL must be a duration of at least 1s, got %q", get("FLEET_DISPATCH_INTERVAL", ""))
	}
	cfg.DispatchInterval = interval
	switch cfg.SSHIdentitySource {
	case "", "agent":
	case "file":
		if cfg.SSHIdentityFile == "" {
			return Config{}, errors.New("FLEET_SSH_IDENTITY_FILE is required when FLEET_SSH_IDENTITY_SOURCE=file")
		}
	default:
		return Config{}, fmt.Errorf("FLEET_SSH_IDENTITY_SOURCE must be agent or file, got %q", cfg.SSHIdentitySource)
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
