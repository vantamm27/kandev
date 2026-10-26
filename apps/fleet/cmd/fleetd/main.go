// Command fleetd runs the fleet registry and dispatcher for Kandev seats.
//
//	fleetd [serve]                                  run the service
//	fleetd token create --owner USER [--labels a,b] [--ttl 24h]
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/kandev/kandev/apps/fleet/internal/config"
	"github.com/kandev/kandev/apps/fleet/internal/kandev"
	"github.com/kandev/kandev/apps/fleet/internal/registry"
	"github.com/kandev/kandev/apps/fleet/internal/server"
	"github.com/kandev/kandev/apps/fleet/internal/store"
)

const (
	shutdownTimeout = 10 * time.Second
	defaultTokenTTL = 24 * time.Hour
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	if err := run(os.Args[1:], os.Stdout, log); err != nil {
		log.Error("fleetd failed", "error", err)
		os.Exit(1)
	}
}

func run(args []string, stdout io.Writer, log *slog.Logger) error {
	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}
	switch {
	case len(args) == 0 || args[0] == "serve":
		return serve(cfg, log)
	case len(args) >= 2 && args[0] == "token" && args[1] == "create":
		return createToken(cfg, args[2:], stdout)
	default:
		return fmt.Errorf("unknown command %q; use serve or token create", strings.Join(args, " "))
	}
}

func createToken(cfg config.Config, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("token create", flag.ContinueOnError)
	owner := fs.String("owner", "", "Kandev user ID that owns the seat (required)")
	labels := fs.String("labels", "", "comma-separated admin labels, for example team:payments")
	ttl := fs.Duration("ttl", defaultTokenTTL, "how long the token stays valid")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*owner) == "" {
		return errors.New("--owner is required")
	}
	if *ttl <= 0 {
		return errors.New("--ttl must be positive")
	}
	ctx := context.Background()
	db, err := store.Open(ctx, cfg.DBPath)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	raw, err := registry.NewToken()
	if err != nil {
		return err
	}
	now := time.Now()
	if err := db.CreateToken(ctx, raw, store.RegistrationToken{
		OwnerUserID: strings.TrimSpace(*owner), Labels: splitLabels(*labels), ExpiresAt: now.Add(*ttl),
	}, now); err != nil {
		return err
	}
	_, err = fmt.Fprintln(stdout, raw)
	return err
}

func splitLabels(raw string) []string {
	var out []string
	for _, label := range strings.Split(raw, ",") {
		if label = strings.TrimSpace(label); label != "" {
			out = append(out, label)
		}
	}
	return out
}

func serve(cfg config.Config, log *slog.Logger) error {
	if cfg.KandevURL == "" || cfg.KandevToken == "" {
		return errors.New("FLEET_KANDEV_URL and FLEET_KANDEV_TOKEN are required to serve")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := store.Open(ctx, cfg.DBPath)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	client := kandev.NewClient(cfg.KandevURL, cfg.KandevToken)
	identity := registry.SSHIdentity{Source: cfg.SSHIdentitySource, File: cfg.SSHIdentityFile}
	httpServer := &http.Server{
		Addr: cfg.ListenAddr,
		Handler: server.New(server.Deps{
			DB: db, Registrar: registry.New(db, client, identity, log), Log: log,
		}),
		ReadHeaderTimeout: 10 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		log.Info("fleetd listening", "addr", cfg.ListenAddr, "db", cfg.DBPath)
		errCh <- httpServer.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	return httpServer.Shutdown(shutdownCtx)
}
