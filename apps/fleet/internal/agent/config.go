package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Config is fleet-agent's saved registration, readable only by the seat user.
type Config struct {
	FleetdURL  string `json:"fleetd_url"`
	SeatID     string `json:"seat_id"`
	HMACSecret string `json:"hmac_secret"`
	Capacity   int    `json:"capacity"`
	// QuotaFile is where a Claude Code hook or statusline script writes the
	// seat's current usage limit and quota; see QuotaReport.
	QuotaFile string `json:"quota_file"`
}

// DefaultConfigPath is ~/.config/fleet-agent/config.json.
func DefaultConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "fleet-agent", "config.json"), nil
}

// SaveConfig writes cfg with owner-only permissions.
func SaveConfig(path string, cfg Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("install config: %w", err)
	}
	return nil
}

// LoadConfig reads a saved config and refuses one other users can read,
// since it holds the seat's HMAC secret.
func LoadConfig(path string) (Config, error) {
	info, err := os.Stat(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return Config{}, fmt.Errorf("config %s must not be readable by other users (chmod 600)", path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return Config{}, fmt.Errorf("decode config: %w", err)
	}
	if cfg.FleetdURL == "" || cfg.SeatID == "" || cfg.HMACSecret == "" {
		return Config{}, errors.New("config is missing fleetd_url, seat_id, or hmac_secret; run fleet-agent register")
	}
	if cfg.Capacity < 1 {
		cfg.Capacity = 1
	}
	return cfg, nil
}
