// Command fleet-agent runs on a node as the seat's macOS user.
//
//	fleet-agent register --fleetd URL --token TOKEN --ssh-host HOST [flags]
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"time"

	"github.com/kandev/kandev/apps/fleet/internal/agent"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "fleet-agent:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: fleet-agent register|version [flags]")
	}
	switch args[0] {
	case "register":
		return register(args[1:], stdout)
	case "version":
		_, err := fmt.Fprintln(stdout, version)
		return err
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func register(args []string, stdout io.Writer) error {
	defaultConfig, err := agent.DefaultConfigPath()
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("register", flag.ContinueOnError)
	fleetd := fs.String("fleetd", "", "fleetd base URL (required)")
	token := fs.String("token", "", "one-time registration token (required); prefer FLEET_REGISTRATION_TOKEN")
	sshHost := fs.String("ssh-host", "", "address Kandev uses to reach this node (required)")
	sshPort := fs.Int("ssh-port", 22, "sshd port")
	sshUser := fs.String("ssh-user", "", "this seat's macOS user (default: current user)")
	hostKey := fs.String("host-key", agent.DefaultHostKeyFile, "sshd host public key file")
	labels := fs.String("labels", "", "comma-separated node labels, for example macos,xcode")
	capacity := fs.Int("capacity", 1, "concurrent tasks for this seat")
	configPath := fs.String("config", defaultConfig, "where to save the registration")
	quotaFile := fs.String("quota-file", "", "quota report file written by a Claude Code hook (default: next to the config)")
	launchAgent := fs.String("launch-agent", "", "also write a LaunchAgent plist to this path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *token == "" {
		*token = os.Getenv("FLEET_REGISTRATION_TOKEN")
	}
	if *fleetd == "" || *token == "" || *sshHost == "" {
		return errors.New("--fleetd, --token (or FLEET_REGISTRATION_TOKEN), and --ssh-host are required")
	}
	if *sshUser == "" {
		current, err := user.Current()
		if err != nil {
			return fmt.Errorf("current user: %w", err)
		}
		*sshUser = current.Username
	}
	fingerprint, err := agent.HostKeyFingerprintFile(*hostKey)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	res, err := agent.Register(ctx, agent.NewHTTPClient(), *fleetd, agent.RegisterRequest{
		Token: *token, SSHHost: *sshHost, SSHPort: *sshPort, SSHUser: *sshUser,
		HostKeyFingerprint: fingerprint, Labels: splitList(*labels), Capacity: *capacity, AgentVersion: version,
	})
	if err != nil {
		return err
	}
	if *quotaFile == "" {
		*quotaFile = filepath.Join(filepath.Dir(*configPath), "quota.json")
	}
	if err := agent.SaveConfig(*configPath, agent.Config{
		FleetdURL: *fleetd, SeatID: res.SeatID, HMACSecret: res.HMACSecret, Capacity: *capacity, QuotaFile: *quotaFile,
	}); err != nil {
		return fmt.Errorf("registered seat %s but could not save the config: %w", res.SeatID, err)
	}
	if _, err := fmt.Fprintf(stdout, "registered seat %s for %s (Kandev executor %s, profile %s)\nconfig: %s\n",
		res.SeatID, res.OwnerUserID, res.KandevExecutorID, res.KandevExecutorProfileID, *configPath); err != nil {
		return err
	}
	if *launchAgent != "" {
		return writeLaunchAgent(*launchAgent, *configPath, stdout)
	}
	return nil
}

func writeLaunchAgent(path, configPath string, stdout io.Writer) error {
	binary, err := os.Executable()
	if err != nil {
		return err
	}
	logPath := filepath.Join(filepath.Dir(configPath), "fleet-agent.log")
	plist, err := agent.LaunchAgentPlist(binary, configPath, logPath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, plist, 0o644); err != nil {
		return fmt.Errorf("write LaunchAgent: %w", err)
	}
	_, err = fmt.Fprintf(stdout, "LaunchAgent: %s\nstart it with: launchctl bootstrap gui/$(id -u) %s\n", path, path)
	return err
}

func splitList(raw string) []string {
	var out []string
	for _, item := range strings.Split(raw, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}
