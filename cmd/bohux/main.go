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
	"github.com/bohumix/bohux/internal/cli"
	"github.com/bohumix/bohux/internal/controlplane"
	"github.com/bohumix/bohux/internal/ingress"
	"github.com/bohumix/bohux/internal/mcp"
	"github.com/bohumix/bohux/internal/provider"
	"github.com/bohumix/bohux/internal/store"
	"github.com/bohumix/bohux/internal/version"
	backupwf "github.com/bohumix/bohux/internal/workflow/backup"
	"github.com/bohumix/bohux/internal/workflow/deploy"
	"github.com/bohumix/bohux/providers/docker"
)

var errUsage = errors.New(`usage: bohux <command>

commands:
` + cli.Commands + `
` + cli.ResourceCommands + `
  version         print the version
  controlplane    run the control plane API
  admin migrate   apply database migrations and exit
  admin bootstrap create a tenant and print its first owner token
  admin backup    write an encrypted archive of Bohux's state
  admin restore   restore an archive onto an empty install
  agent register  enroll this node with a bootstrap token (once)
  agent run       run the node agent
  mcp             serve the API as MCP tools over stdio, for AI agents`)

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
	envHealthTimeout   = "BOHUX_DEPLOY_HEALTH_TIMEOUT"
	envOpenBaoAddr     = "BOHUX_OPENBAO_ADDR"
	envOpenBaoToken    = "BOHUX_OPENBAO_TOKEN_FILE"
	envOpenBaoMount    = "BOHUX_OPENBAO_KV_MOUNT"
	envOpenBaoTokenVal = "BOHUX_OPENBAO_TOKEN"
	envBackupRecipient = "BOHUX_BACKUP_RECIPIENT"
	envBackupDBURL     = "BOHUX_BACKUP_DATABASE_URL"
	envBackupDir       = "BOHUX_BACKUP_DIR"
	envBackupSchedule  = "BOHUX_BACKUP_SCHEDULE"
	envBackupKeep      = "BOHUX_BACKUP_KEEP"
	envIngressSocket   = "BOHUX_INGRESS_ADMIN_SOCKET"
	envACMEEmail       = "BOHUX_ACME_EMAIL"

	defaultListenAddr      = "127.0.0.1:7400"
	defaultAgentListenAddr = "127.0.0.1:7443"
	defaultDataDir         = "/var/lib/bohux"
	defaultAgentStateDir   = "/var/lib/bohux-agent"
	defaultTemporalAddress = "127.0.0.1:7233"
	defaultTemporalNS      = "default"
	defaultOpenBaoMount    = "secret"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Getenv); err != nil {
		// `bohux exec` exits with the remote command's status, quietly.
		var exit *cli.ExitError
		if errors.As(err, &exit) {
			os.Exit(exit.Code)
		}
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
	agentRun        func(ctx context.Context, cfg agentConfig) error
	// stdin is what `bohux secret set` reads the value from.
	stdin io.Reader
}

var defaultDeps = deps{
	runControlPlane: controlplane.Run, migrate: store.MigrateDSN, bootstrap: bootstrap,
	agentRegister: agent.Register, agentRun: runAgent, stdin: os.Stdin,
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

// agentConfig is `bohux agent run`'s configuration.
type agentConfig struct {
	StateDir string
	// IngressSocket is the node's Caddy admin socket (ADR-0034); empty: no ingress.
	IngressSocket string
	ACMEEmail     string
}

// runAgent loads the agent's state, then runs the reconcile loop, the control-plane
// stream (which also carries build requests, ADR-0028), the disk-pressure monitor, and
// image retention side by side until ctx ends.
// The loop and the stream share only the cache: the stream fills it, the reconciler
// acts on it, and either works without the other (rule 1).
func runAgent(ctx context.Context, cfg agentConfig) error {
	stateDir := cfg.StateDir
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
	sampler := &agent.Sampler{Paths: disk.Paths, Provider: p}
	link := &agent.Link{}
	rec := &agent.Reconciler{State: st, Cache: cache, Provider: p, Usage: sampler, Link: link}
	if cfg.IngressSocket != "" {
		rec.Ingress = &ingress.Caddy{AdminSocket: cfg.IngressSocket, ACMEEmail: cfg.ACMEEmail,
			Dir: filepath.Join(stateDir, "ingress")}
	}
	opts := agent.RunOptions{State: st, Cache: cache, Reports: rec, Disk: disk, Link: link,
		Providers: []*bohuxv1.ProviderDescriptor{descriptorProto(desc)},
		Builder:   &agent.Builder{Disk: disk, WorkDir: filepath.Join(stateDir, "builds"), Secrets: st},
		Workloads: rec}

	tasks := []func(context.Context) error{
		rec.Run,
		func(ctx context.Context) error { return agent.Run(ctx, opts) },
		disk.Run,
		sampler.Run,
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
		baoAddr := fs.String("openbao-address", getenv(envOpenBaoAddr),
			"OpenBao URL for secret values; empty disables secrets (env "+envOpenBaoAddr+")")
		baoToken := fs.String("openbao-token-file", getenv(envOpenBaoToken),
			"file holding the OpenBao token (env "+envOpenBaoToken+")")
		baoMount := fs.String("openbao-kv-mount", envOr(getenv, envOpenBaoMount, defaultOpenBaoMount),
			"OpenBao KV v2 mount for secret values (env "+envOpenBaoMount+")")
		healthTimeout := fs.Duration("deploy-health-timeout", deploy.DefaultHealthTimeout,
			"how long a deployed release has to turn healthy before it is rolled back (env "+envHealthTimeout+")")
		if v := getenv(envHealthTimeout); v != "" {
			if err := fs.Set("deploy-health-timeout", v); err != nil {
				return fmt.Errorf("controlplane: %s: %w", envHealthTimeout, err)
			}
		}
		backupRecipient := fs.String("backup-recipient", getenv(envBackupRecipient),
			"age public key nightly backups are encrypted to; empty disables them (env "+envBackupRecipient+")")
		backupDB := fs.String("backup-database-url", getenv(envBackupDBURL),
			"Postgres DSN of the read-only BYPASSRLS backup role (env "+envBackupDBURL+")")
		backupDir := fs.String("backup-dir", envOr(getenv, envBackupDir, backupwf.DefaultDir),
			"directory nightly backups are written to (env "+envBackupDir+")")
		backupSchedule := fs.String("backup-schedule", envOr(getenv, envBackupSchedule, backupwf.DefaultSchedule),
			"cron expression, UTC, for nightly backups (env "+envBackupSchedule+")")
		backupKeep := fs.Int("backup-keep", backupwf.DefaultKeep,
			"how many nightly backups to keep (env "+envBackupKeep+")")
		if v := getenv(envBackupKeep); v != "" {
			if err := fs.Set("backup-keep", v); err != nil {
				return fmt.Errorf("controlplane: %s: %w", envBackupKeep, err)
			}
		}
		if err := fs.Parse(args[1:]); err != nil {
			return fmt.Errorf("controlplane: %w", err)
		}
		if *dbURL == "" {
			return fmt.Errorf("controlplane: --database-url or %s is required", envDatabaseURL)
		}
		if *backupRecipient != "" && *backupDB == "" {
			return fmt.Errorf("controlplane: --backup-database-url or %s is required with --backup-recipient", envBackupDBURL)
		}
		if *backupKeep < 1 {
			return fmt.Errorf("controlplane: --backup-keep must be at least 1")
		}
		if *baoAddr != "" && *baoToken == "" && getenv(envOpenBaoTokenVal) == "" {
			return fmt.Errorf("controlplane: --openbao-token-file, %s, or %s is required with --openbao-address",
				envOpenBaoToken, envOpenBaoTokenVal)
		}
		cfg := controlplane.Config{ListenAddr: *listen, AgentListenAddr: *agentListen,
			DatabaseURL: *dbURL, DataDir: *dataDir, TemporalAddress: *temporalAddr, TemporalNamespace: *temporalNS,
			DeployHealthTimeout: *healthTimeout, OpenBaoAddress: *baoAddr, OpenBaoTokenFile: *baoToken,
			OpenBaoMount: *baoMount, OpenBaoToken: getenv(envOpenBaoTokenVal),
			Backup: controlplane.BackupConfig{Recipient: *backupRecipient, DatabaseURL: *backupDB,
				Dir: *backupDir, Schedule: *backupSchedule, Keep: *backupKeep}}
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
		case "backup":
			return adminBackup(ctx, args[2:], stdout, getenv)
		case "restore":
			return adminRestore(ctx, args[2:], stdout, getenv)
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
	case "mcp":
		// An MCP server over stdio for AI agents (ADR-0018); a client of the API.
		token := getenv(cli.EnvToken)
		if token == "" {
			return fmt.Errorf("mcp: set %s to an API token (a scoped, short-lived one)", cli.EnvToken)
		}
		base := getenv(cli.EnvAPIURL)
		if base == "" {
			base = cli.DefaultAPIURL
		}
		return mcp.Run(ctx, mcp.Config{APIURL: base, Token: token, HTTP: cli.HTTPClient(base)})
	case "deploy", "rollback", "scale", "logs", "exec", "project", "env", "app", "route", "secret", "node", "webhook", "channel", "audit":
		if err := cli.Run(ctx, args[0], args[1:], d.stdin, stdout, getenv); err != nil {
			if errors.Is(err, cli.ErrUsage) {
				return fmt.Errorf("%w\n\n%w", err, errUsage)
			}
			return err
		}
		return nil
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
		socket := fs.String("ingress-admin-socket", getenv(envIngressSocket),
			"Caddy admin API Unix socket of this node's ingress; empty: no ingress (env "+envIngressSocket+")")
		email := fs.String("acme-email", getenv(envACMEEmail),
			"ACME account e-mail for the ingress's certificates (env "+envACMEEmail+")")
		if err := fs.Parse(args[1:]); err != nil {
			return fmt.Errorf("agent run: %w", err)
		}
		if err := d.agentRun(ctx, agentConfig{StateDir: *stateDir, IngressSocket: *socket, ACMEEmail: *email}); err != nil {
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
