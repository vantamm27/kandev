// Command fleet-agent runs on a node as the seat's OS user.
//
//	fleet-agent register --fleetd URL --token TOKEN --ssh-host HOST [flags]
//	fleet-agent run [--config PATH] [--interval 30s]
//	fleet-agent preflight [--ssh-port 22]
//	fleet-agent checkpoint --worktree DIR --task ID [--remote origin] [--no-ci-skip]
//	fleet-agent host-key [--ssh-port 22]
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"
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
		return errors.New("usage: fleet-agent register|run|preflight|checkpoint|host-key|version [flags]")
	}
	switch args[0] {
	case "register":
		return register(args[1:], stdout)
	case "run":
		return runReporter(args[1:])
	case "preflight":
		return preflight(args[1:], stdout)
	case "checkpoint":
		return checkpoint(args[1:], stdout)
	case "host-key":
		return printHostKey(args[1:], stdout)
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
	sshUser := fs.String("ssh-user", "", "this seat's OS user (default: current user)")
	hostKey := fs.String("host-key", "", "pin this sshd host public key file instead of probing the local sshd")
	labels := fs.String("labels", "", "comma-separated node labels, for example macos,xcode")
	capacity := fs.Int("capacity", 1, "concurrent tasks for this seat")
	configPath := fs.String("config", defaultConfig, "where to save the registration")
	quotaFile := fs.String("quota-file", "", "quota report file written by a Claude Code hook (default: next to the config)")
	launchAgent := fs.String("launch-agent", "", "also write a macOS LaunchAgent plist to this path")
	systemdUnit := fs.String("systemd-unit", "", "also write a Linux systemd user unit to this path")
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
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	fingerprint, err := seatHostKeyFingerprint(ctx, *hostKey, *sshPort)
	if err != nil {
		return err
	}
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
		if err := writeLaunchAgent(*launchAgent, *configPath, stdout); err != nil {
			return err
		}
	}
	if *systemdUnit != "" {
		return writeSystemdUnit(*systemdUnit, *configPath, stdout)
	}
	return nil
}

func writeSystemdUnit(path, configPath string, stdout io.Writer) error {
	binary, err := os.Executable()
	if err != nil {
		return err
	}
	unit, err := agent.SystemdUnit(binary, configPath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, unit, 0o644); err != nil {
		return fmt.Errorf("write systemd unit: %w", err)
	}
	_, err = fmt.Fprintf(stdout, "systemd unit: %s\nstart it with: systemctl --user daemon-reload && systemctl --user enable --now %s\n"+
		"keep it running without a login session: sudo loginctl enable-linger $USER\n", path, agent.SystemdUnitName)
	return err
}

// printHostKey prints the fingerprint register would pin, for checking or
// repairing a seat's Kandev executor.
func printHostKey(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("host-key", flag.ContinueOnError)
	sshPort := fs.Int("ssh-port", 22, "sshd port")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fingerprint, err := seatHostKeyFingerprint(ctx, "", *sshPort)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(stdout, fingerprint)
	return err
}

// seatHostKeyFingerprint pins the host key Kandev will see: the one the local
// sshd presents to a default Go SSH client, unless a key file is named.
func seatHostKeyFingerprint(ctx context.Context, hostKeyFile string, sshPort int) (string, error) {
	if hostKeyFile != "" {
		return agent.HostKeyFingerprintFile(hostKeyFile)
	}
	return agent.NegotiatedHostKeyFingerprint(ctx, fmt.Sprintf("127.0.0.1:%d", sshPort))
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

func runReporter(args []string) error {
	defaultConfig, err := agent.DefaultConfigPath()
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	configPath := fs.String("config", defaultConfig, "saved registration")
	interval := fs.Duration("interval", agent.HeartbeatInterval, "heartbeat interval")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *interval < time.Second {
		return errors.New("--interval must be at least 1s")
	}
	cfg, err := agent.LoadConfig(*configPath)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	reporter := &agent.Reporter{
		Config: cfg, Version: version, HTTP: agent.NewHTTPClient(),
		Slots: agent.ProcessSlotCounter{UID: os.Getuid()},
		Log:   slog.New(slog.NewJSONHandler(os.Stderr, nil)), Now: time.Now,
	}
	reporter.Log.Info("fleet-agent running", "seat_id", cfg.SeatID, "fleetd", cfg.FleetdURL, "version", version)
	reporter.Run(ctx, *interval)
	return nil
}

func preflight(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("preflight", flag.ContinueOnError)
	sshPort := fs.Int("ssh-port", 22, "sshd port")
	if err := fs.Parse(args); err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/zsh"
	}
	results, ok := agent.RunChecks(context.Background(), agent.DefaultChecks(home, shell, *sshPort))
	for _, r := range results {
		mark := "FAIL"
		switch {
		case r.Skipped:
			mark = "SKIP"
		case r.OK:
			mark = "ok"
		}
		if _, err := fmt.Fprintf(stdout, "%-4s %-7s %s\n", mark, r.Name, r.Detail); err != nil {
			return err
		}
	}
	if !ok {
		return errors.New("preflight failed")
	}
	return nil
}

func checkpoint(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("checkpoint", flag.ContinueOnError)
	worktree := fs.String("worktree", "", "task worktree (required)")
	task := fs.String("task", "", "Kandev task ID (required)")
	remote := fs.String("remote", "origin", "git remote")
	noCISkip := fs.Bool("no-ci-skip", false, "do not send the ci.skip push option")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *worktree == "" || *task == "" {
		return errors.New("--worktree and --task are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	res, err := agent.Checkpoint(ctx, agent.CheckpointOptions{Worktree: *worktree, TaskID: *task, Remote: *remote, SkipCI: !*noCISkip})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "pushed %s to %s (new commit: %v)\n", res.Commit, res.Ref, res.Committed)
	return err
}
