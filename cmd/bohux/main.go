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
	"path/filepath"
	"syscall"

	bohuxv1 "github.com/bohumix/bohux/gen/bohux/v1"
	"github.com/bohumix/bohux/internal/agent"
	"github.com/bohumix/bohux/internal/api"
	"github.com/bohumix/bohux/internal/controlplane"
	"github.com/bohumix/bohux/internal/provider"
	"github.com/bohumix/bohux/internal/store"
	"github.com/bohumix/bohux/internal/version"
	"github.com/bohumix/bohux/providers/docker"
)

var errUsage = errors.New(`usage: bohux <command>

commands:
  version         print the version
  controlplane    run the control plane API
  admin migrate   apply database migrations and exit
  admin bootstrap create a tenant and print its first owner token
  agent register  enroll this node with a bootstrap token (once)
  agent run       run the node agent`)

// Environment fallbacks for flags. See docs/reference/config.md.
const (
	envListenAddr      = "BOHUX_LISTEN_ADDR"
	envAgentListenAddr = "BOHUX_AGENT_LISTEN_ADDR"
	envDatabaseURL     = "BOHUX_DATABASE_URL"
	envDataDir         = "BOHUX_DATA_DIR"
	envAgentStateDir   = "BOHUX_AGENT_STATE_DIR"
	envBootstrapToken  = "BOHUX_BOOTSTRAP_TOKEN"
	envTemporalAddress = "BOHUX_TEMPORAL_ADDRESS"
	envTemporalNS      = "BOHUX_TEMPORAL_NAMESPACE"

	defaultListenAddr      = "127.0.0.1:7400"
	defaultAgentListenAddr = "127.0.0.1:7443"
	defaultDataDir         = "/var/lib/bohux"
	defaultAgentStateDir   = "/var/lib/bohux-agent"
	defaultTemporalAddress = "127.0.0.1:7233"
	defaultTemporalNS      = "default"
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
	bootstrap       func(ctx context.Context, dsn, slug, displayName string) (token string, err error)
	agentRegister   func(context.Context, agent.RegisterOptions) error
	agentRun        func(ctx context.Context, stateDir string) error
}

var defaultDeps = deps{
	runControlPlane: controlplane.Run, migrate: store.MigrateDSN, bootstrap: bootstrap,
	agentRegister: agent.Register, agentRun: runAgent,
}

// nodeProvider is the provider this agent runs: one agent, one target (ADR-0007). v0.1
// is Docker only (ADR-0011); its target is the local daemon, or DOCKER_HOST if set.
const nodeProvider = docker.Name

// agentProviders registers the providers compiled into this binary. Each keeps its
// artifacts under root, <state dir>/workloads (rule 2).
func agentProviders(root string) (*provider.Registry, error) {
	r := provider.NewRegistry()
	if err := r.Add(docker.Name, func(target string) (provider.Provider, error) {
		return docker.New(root, target)
	}); err != nil {
		return nil, err
	}
	return r, nil
}

// runAgent loads the agent's state, then runs the reconcile loop, the control-plane
// stream, the disk-pressure monitor, and image retention side by side until ctx ends.
// The loop and the stream share only the cache: the stream fills it, the reconciler
// acts on it, and either works without the other (rule 1).
func runAgent(ctx context.Context, stateDir string) error {
	st, err := agent.Load(stateDir)
	if err != nil {
		return err
	}
	cache, err := agent.OpenCache(st)
	if err != nil {
		return err
	}
	reg, err := agentProviders(filepath.Join(stateDir, "workloads"))
	if err != nil {
		return err
	}
	p, err := reg.New(nodeProvider, "")
	if err != nil {
		return err
	}
	desc, err := p.Describe(ctx)
	if err != nil {
		return fmt.Errorf("describe provider: %w", err)
	}

	disk := &agent.DiskMonitor{Paths: []string{stateDir}}
	if d, ok := p.(interface {
		DataRoot(context.Context) (string, error)
	}); ok {
		root, err := d.DataRoot(ctx)
		if err != nil {
			return fmt.Errorf("find provider data root: %w", err)
		}
		disk.Paths = append(disk.Paths, root)
	}
	rec := &agent.Reconciler{State: st, Cache: cache, Provider: p}
	opts := agent.RunOptions{State: st, Cache: cache, Reports: rec, Disk: disk,
		Providers: []*bohuxv1.ProviderDescriptor{descriptorProto(desc)}}

	tasks := []func(context.Context) error{
		rec.Run,
		func(ctx context.Context) error { return agent.Run(ctx, opts) },
		disk.Run,
	}
	if images, ok := p.(provider.ImageStore); ok {
		tasks = append(tasks, (&agent.Retention{Images: images, Cache: cache}).Run)
	}
	errc := make(chan error, len(tasks))
	for _, task := range tasks {
		go func() { errc <- task(ctx) }()
	}
	errs := make([]error, len(tasks))
	for i := range tasks {
		errs[i] = <-errc
	}
	return errors.Join(errs...)
}

func descriptorProto(d provider.Descriptor) *bohuxv1.ProviderDescriptor {
	caps := make([]string, len(d.Capabilities))
	for i, c := range d.Capabilities {
		caps[i] = string(c)
	}
	return &bohuxv1.ProviderDescriptor{Name: d.Name, Version: d.Version, Capabilities: caps}
}

// bootstrap migrates, then creates the tenant and its first token.
func bootstrap(ctx context.Context, dsn, slug, displayName string) (string, error) {
	if err := store.MigrateDSN(ctx, dsn); err != nil {
		return "", err
	}
	st, err := store.Open(ctx, dsn)
	if err != nil {
		return "", err
	}
	defer st.Close()
	_, token, err := api.Bootstrap(ctx, st, slug, displayName)
	return token, err
}

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
		agentListen := fs.String("agent-listen", envOr(getenv, envAgentListenAddr, defaultAgentListenAddr),
			"agent TLS listen address (env "+envAgentListenAddr+")")
		dbURL := fs.String("database-url", getenv(envDatabaseURL),
			"Postgres DSN (env "+envDatabaseURL+")")
		dataDir := fs.String("data-dir", envOr(getenv, envDataDir, defaultDataDir),
			"directory for the node CA and signing key; back it up (env "+envDataDir+")")
		temporalAddr := fs.String("temporal-address", envOr(getenv, envTemporalAddress, defaultTemporalAddress),
			"Temporal frontend host:port for deploys (env "+envTemporalAddress+")")
		temporalNS := fs.String("temporal-namespace", envOr(getenv, envTemporalNS, defaultTemporalNS),
			"Temporal namespace (env "+envTemporalNS+")")
		if err := fs.Parse(args[1:]); err != nil {
			return fmt.Errorf("controlplane: %w", err)
		}
		if *dbURL == "" {
			return fmt.Errorf("controlplane: --database-url or %s is required", envDatabaseURL)
		}
		cfg := controlplane.Config{ListenAddr: *listen, AgentListenAddr: *agentListen,
			DatabaseURL: *dbURL, DataDir: *dataDir, TemporalAddress: *temporalAddr, TemporalNamespace: *temporalNS}
		if err := d.runControlPlane(ctx, cfg); err != nil {
			return fmt.Errorf("controlplane: %w", err)
		}
		return nil
	case "admin":
		if len(args) < 2 {
			return fmt.Errorf("admin: missing subcommand: %w", errUsage)
		}
		switch args[1] {
		case "migrate":
		case "bootstrap":
			return adminBootstrap(ctx, args[2:], stdout, getenv, d)
		default:
			return fmt.Errorf("admin: unknown subcommand %q: %w", args[1], errUsage)
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
	case "agent":
		return agentCommand(ctx, args[1:], getenv, d)
	default:
		return fmt.Errorf("unknown command %q: %w", args[0], errUsage)
	}
}

func agentCommand(ctx context.Context, args []string, getenv func(string) string, d deps) error {
	if len(args) == 0 {
		return fmt.Errorf("agent: missing subcommand: %w", errUsage)
	}
	fs := newFlagSet("agent " + args[0])
	stateDir := fs.String("state-dir", envOr(getenv, envAgentStateDir, defaultAgentStateDir),
		"agent keys and identity (env "+envAgentStateDir+")")
	switch args[0] {
	case "register":
		cp := fs.String("control-plane", "", "control plane agent address, host:port (required)")
		token := fs.String("token", getenv(envBootstrapToken),
			"bootstrap token; prefer the env var so it stays out of shell history (env "+envBootstrapToken+")")
		pin := fs.String("ca-cert-sha256", "", "node CA pin from CreateBootstrapToken (required)")
		if err := fs.Parse(args[1:]); err != nil {
			return fmt.Errorf("agent register: %w", err)
		}
		if *cp == "" || *token == "" || *pin == "" {
			return errors.New("agent register: --control-plane, --token (or " + envBootstrapToken +
				"), and --ca-cert-sha256 are required")
		}
		err := d.agentRegister(ctx, agent.RegisterOptions{StateDir: *stateDir, ControlPlane: *cp,
			Token: *token, CACertSHA256: *pin, Docker: agent.DockerCLI{}})
		if err != nil {
			return fmt.Errorf("agent register: %w", err)
		}
		return nil
	case "run":
		if err := fs.Parse(args[1:]); err != nil {
			return fmt.Errorf("agent run: %w", err)
		}
		if err := d.agentRun(ctx, *stateDir); err != nil {
			return fmt.Errorf("agent run: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("agent: unknown subcommand %q: %w", args[0], errUsage)
	}
}

func adminBootstrap(ctx context.Context, args []string, stdout io.Writer, getenv func(string) string, d deps) error {
	fs := newFlagSet("admin bootstrap")
	dbURL := fs.String("database-url", getenv(envDatabaseURL), "Postgres DSN (env "+envDatabaseURL+")")
	slug := fs.String("slug", "", "tenant slug (required)")
	name := fs.String("display-name", "", "tenant display name")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("admin bootstrap: %w", err)
	}
	if *dbURL == "" {
		return fmt.Errorf("admin bootstrap: --database-url or %s is required", envDatabaseURL)
	}
	if *slug == "" {
		return errors.New("admin bootstrap: --slug is required")
	}
	token, err := d.bootstrap(ctx, *dbURL, *slug, *name)
	if err != nil {
		return fmt.Errorf("admin bootstrap: %w", err)
	}
	// The only time this token is shown (ADR-0022). It goes to stdout, never to logs.
	if _, err := fmt.Fprintf(stdout, "Tenant %q created.\n"+
		"Owner token for the whole tenant (shown once, store it now):\n\n%s\n", *slug, token); err != nil {
		return fmt.Errorf("admin bootstrap: write token: %w", err)
	}
	return nil
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
