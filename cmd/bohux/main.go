// Command bohux is the single Bohux binary. Modes (controlplane, agent, cli) are added
// slice by slice; see docs/plan/03-progress.md. Flags and environment variables are
// documented in docs/reference/config.md.
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
	"syscall"

	"github.com/bohumix/bohux/internal/controlplane"
	"github.com/bohumix/bohux/internal/store"
	"github.com/bohumix/bohux/internal/version"
)

var errUsage = errors.New(`usage: bohux <command>

commands:
  version         print the version
  controlplane    run the control plane API
  admin migrate   apply database migrations and exit`)

// Environment fallbacks for flags. See docs/reference/config.md.
const (
	envListenAddr  = "BOHUX_LISTEN_ADDR"
	envDatabaseURL = "BOHUX_DATABASE_URL"

	defaultListenAddr = "127.0.0.1:7400"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Getenv); err != nil {
		slog.Error("bohux failed", "err", err)
		os.Exit(1)
	}
}

// deps are the side-effecting calls run makes, replaceable in tests.
type deps struct {
	runControlPlane func(context.Context, controlplane.Config) error
	migrate         func(context.Context, string) error
}

var defaultDeps = deps{runControlPlane: controlplane.Run, migrate: store.MigrateDSN}

func run(ctx context.Context, args []string, stdout io.Writer, getenv func(string) string) error {
	return runWith(ctx, args, stdout, getenv, defaultDeps)
}

func runWith(ctx context.Context, args []string, stdout io.Writer, getenv func(string) string, d deps) error {
	if len(args) == 0 {
		return errUsage
	}
	switch args[0] {
	case "version":
		if _, err := fmt.Fprintln(stdout, version.Version); err != nil {
			return fmt.Errorf("write version: %w", err)
		}
		return nil
	case "controlplane":
		fs := newFlagSet("controlplane")
		listen := fs.String("listen", envOr(getenv, envListenAddr, defaultListenAddr),
			"API listen address (env "+envListenAddr+")")
		dbURL := fs.String("database-url", getenv(envDatabaseURL),
			"Postgres DSN (env "+envDatabaseURL+")")
		if err := fs.Parse(args[1:]); err != nil {
			return fmt.Errorf("controlplane: %w", err)
		}
		if *dbURL == "" {
			return fmt.Errorf("controlplane: --database-url or %s is required", envDatabaseURL)
		}
		if err := d.runControlPlane(ctx, controlplane.Config{ListenAddr: *listen, DatabaseURL: *dbURL}); err != nil {
			return fmt.Errorf("controlplane: %w", err)
		}
		return nil
	case "admin":
		if len(args) < 2 || args[1] != "migrate" {
			return fmt.Errorf("admin: unknown subcommand: %w", errUsage)
		}
		fs := newFlagSet("admin migrate")
		dbURL := fs.String("database-url", getenv(envDatabaseURL),
			"Postgres DSN (env "+envDatabaseURL+")")
		if err := fs.Parse(args[2:]); err != nil {
			return fmt.Errorf("admin migrate: %w", err)
		}
		if *dbURL == "" {
			return fmt.Errorf("admin migrate: --database-url or %s is required", envDatabaseURL)
		}
		if err := d.migrate(ctx, *dbURL); err != nil {
			return fmt.Errorf("admin migrate: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("unknown command %q: %w", args[0], errUsage)
	}
}

func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

func envOr(getenv func(string) string, key, fallback string) string {
	if v := getenv(key); v != "" {
		return v
	}
	return fallback
}
