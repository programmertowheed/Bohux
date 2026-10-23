// Package controlplane runs the Bohux control plane: the public API (bohux.v1) over
// ConnectRPC, the agent endpoint (ADR-0006, ADR-0007), and the deploy worker
// (ADR-0026), and the maintenance schedules: the nightly backup (ADR-0033) and the
// weekly restore drill (ADR-0051).
package controlplane

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"filippo.io/age"
	"go.temporal.io/sdk/client"
	tlog "go.temporal.io/sdk/log"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	"github.com/bohumix/bohux/internal/agentapi"
	"github.com/bohumix/bohux/internal/api"
	"github.com/bohumix/bohux/internal/auth"
	"github.com/bohumix/bohux/internal/drill"
	"github.com/bohumix/bohux/internal/notify"
	"github.com/bohumix/bohux/internal/pki"
	"github.com/bohumix/bohux/internal/secrets/openbao"
	"github.com/bohumix/bohux/internal/store"
	"github.com/bohumix/bohux/internal/webui"
	backupwf "github.com/bohumix/bohux/internal/workflow/backup"
	"github.com/bohumix/bohux/internal/workflow/deploy"
	drillwf "github.com/bohumix/bohux/internal/workflow/drill"
	"github.com/bohumix/bohux/internal/workflow/maintenance"
)

// Config is everything the control plane needs at start. See docs/reference/config.md.
type Config struct {
	// ListenAddr is the TCP address the API listens on.
	ListenAddr string
	// AgentListenAddr is the TLS listener for agents (bohux.agent.v1, ADR-0024).
	AgentListenAddr string
	// DatabaseURL is the Postgres DSN. Its role must own the schema and must not bypass
	// row-level security (ADR-0021, data-model.md).
	DatabaseURL string
	// DataDir holds the node CA and assignment signing key (ADR-0024). It must be
	// backed up with the database.
	DataDir string
	// TemporalAddress is the Temporal frontend (host:port) deploys run on (ADR-0002).
	// The control plane starts and serves without it; deploys wait for it.
	TemporalAddress string
	// TemporalNamespace is the Temporal namespace for Bohux's workflows.
	TemporalNamespace string
	// OpenBaoAddress is the OpenBao server that holds secret values (ADR-0008). Empty
	// disables secrets: SetSecret is unavailable and assignments with secrets fail.
	OpenBaoAddress string
	// OpenBaoTokenFile holds the OpenBao token. A file, not a flag value, so the token
	// stays out of process listings and shell history.
	OpenBaoTokenFile string
	// OpenBaoToken is the token itself, used when no file is given: the control plane
	// running as an App receives it as a secret env var (ADR-0032). Never logged.
	OpenBaoToken string
	// OpenBaoMount is the KV v2 mount for secret values.
	OpenBaoMount string
	// DeployHealthTimeout is how long a deployed Release has to turn healthy before
	// the deploy compensates. Zero means deploy.DefaultHealthTimeout.
	DeployHealthTimeout time.Duration
	// Backup configures the nightly backup schedule (ADR-0033). An empty Recipient
	// disables it, and removes a schedule an earlier configuration created.
	Backup BackupConfig
	// Drill configures the weekly restore drill (ADR-0051). An empty IdentityFile
	// disables it, and removes a schedule an earlier configuration created.
	Drill DrillConfig
	// Mail is the SMTP relay for EMAIL notification channels (ADR-0043). An empty
	// Addr disables email.
	Mail MailConfig
}

// MailConfig is the install's SMTP relay (ADR-0043).
type MailConfig struct {
	// Addr is host:port; port 465 is implicit TLS, any other must offer STARTTLS.
	Addr     string
	From     string
	Username string
	// PasswordFile holds the relay password; Password is used when no file is given
	// (the control plane as an App gets it as a secret env var). Never logged.
	PasswordFile string
	Password     string
}

// relay resolves the mail configuration, reading the password file.
func (m MailConfig) relay() (notify.SMTP, error) {
	if m.Addr == "" {
		return notify.SMTP{}, nil
	}
	if _, _, err := net.SplitHostPort(m.Addr); err != nil {
		return notify.SMTP{}, fmt.Errorf("SMTP address: %w", err)
	}
	if m.From == "" {
		return notify.SMTP{}, errors.New("an SMTP relay needs a sender address")
	}
	password := m.Password
	if m.PasswordFile != "" {
		raw, err := os.ReadFile(m.PasswordFile)
		if err != nil {
			return notify.SMTP{}, fmt.Errorf("read SMTP password file: %w", err)
		}
		password = strings.TrimSpace(string(raw))
	}
	if m.Username != "" && password == "" {
		return notify.SMTP{}, errors.New("an SMTP username needs a password")
	}
	return notify.SMTP{Addr: m.Addr, From: m.From, Username: m.Username, Password: password}, nil
}

// BackupConfig is the nightly backup's configuration (ADR-0033).
type BackupConfig struct {
	// Recipient is the age public key archives are encrypted to.
	Recipient string
	// DatabaseURL is a read-only role that bypasses row-level security. Required
	// with Recipient.
	DatabaseURL string
	// Dir is where archives are written.
	Dir string
	// Schedule is a cron expression in UTC; empty means 03:17 nightly.
	Schedule string
	// Keep is how many archives stay; zero means seven.
	Keep int
}

// DrillConfig is the weekly restore drill's configuration (ADR-0051).
type DrillConfig struct {
	// IdentityFile holds the age identity the drill decrypts archives with.
	// Identity is the identity itself, used when no file is given: the control plane
	// running as an App receives it as a secret env var (ADR-0032), as it does the
	// OpenBao token. Never logged. With both empty, drills are off and nothing else
	// here is read — a drill needs a private key on the host, so it is opt-in.
	IdentityFile string
	Identity     string
	// DatabaseURL is the drill role: LOGIN CREATEDB, no BYPASSRLS. It creates, owns
	// and migrates each run's scratch database.
	DatabaseURL string
	// AdminDatabaseURL is the drill admin role: LOGIN BYPASSRLS, and granted
	// SET ON PARAMETER session_replication_role. It loads the rows.
	AdminDatabaseURL string
	// KVMount is the drill's own OpenBao KV v2 mount, which the drill wipes. It must
	// not be the live mount.
	KVMount string
	// Schedule is a cron expression in UTC; empty means Sunday 04:47.
	Schedule string
}

// enabled reports whether drills are configured.
func (d DrillConfig) enabled() bool { return d.IdentityFile != "" || d.Identity != "" }

// identity reads the drill's age identity and returns it with its public half, which
// becomes a recipient on every archive (ADR-0051 item 2). The file wins over the
// value, as it does for the OpenBao token.
func (d DrillConfig) identity() (ids []age.Identity, recipient string, err error) {
	from, source := strings.NewReader(d.Identity), "the drill identity"
	if d.IdentityFile != "" {
		raw, err := os.ReadFile(d.IdentityFile)
		if err != nil {
			return nil, "", fmt.Errorf("read the drill identity: %w", err)
		}
		from, source = strings.NewReader(string(raw)), "the drill identity "+d.IdentityFile
	}
	ids, err = age.ParseIdentities(from)
	if err != nil {
		return nil, "", fmt.Errorf("parse %s: %w", source, err)
	}
	// The public half has to come from an X25519 identity: it is what the backup
	// encrypts to, and age gives no way to recover a recipient from any other kind.
	// ParseIdentities only yields X25519 keys, so the error below is a guard against
	// that changing, not a case a file can reach today.
	for _, id := range ids {
		if x, ok := id.(*age.X25519Identity); ok {
			return ids, x.Recipient().String(), nil
		}
	}
	return nil, "", fmt.Errorf("%s holds no age X25519 key", source)
}

// validate refuses a drill configuration that could not run, or that would wipe live
// secrets.
func (d DrillConfig) validate(liveMount string) error {
	if !d.enabled() {
		return nil
	}
	if d.DatabaseURL == "" || d.AdminDatabaseURL == "" {
		return errors.New("a restore drill needs both drill database URLs")
	}
	if d.KVMount != "" && d.KVMount == liveMount {
		return fmt.Errorf("the drill KV mount is the live one (%q); a drill wipes its mount", d.KVMount)
	}
	return nil
}

// shutdownTimeout bounds how long in-flight requests get after the context ends.
const shutdownTimeout = 10 * time.Second

// Run migrates the database, then serves until ctx is cancelled. A clean shutdown
// returns nil.
func Run(ctx context.Context, cfg Config) error {
	if cfg.DatabaseURL == "" {
		return errors.New("controlplane: database URL is required")
	}
	if cfg.DataDir == "" {
		return errors.New("controlplane: data directory is required")
	}
	if cfg.TemporalAddress == "" || cfg.TemporalNamespace == "" {
		return errors.New("controlplane: Temporal address and namespace are required")
	}
	if cfg.Backup.Recipient != "" && (cfg.Backup.DatabaseURL == "" || cfg.Backup.Dir == "") {
		return errors.New("controlplane: a backup recipient needs a backup database URL and directory")
	}
	if err := cfg.Drill.validate(cfg.OpenBaoMount); err != nil {
		return fmt.Errorf("controlplane: %w", err)
	}
	ln, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.ListenAddr, err)
	}
	agentLn, err := net.Listen("tcp", cfg.AgentListenAddr)
	if err != nil {
		_ = ln.Close()
		return fmt.Errorf("listen on %s: %w", cfg.AgentListenAddr, err)
	}
	return Serve(ctx, ln, agentLn, cfg)
}

// Serve is Run on existing listeners (the API's and the agents'), which it closes.
func Serve(ctx context.Context, ln, agentLn net.Listener, cfg Config) error {
	defer func() { _ = ln.Close() }()
	defer func() { _ = agentLn.Close() }()
	authority, err := pki.LoadOrCreate(cfg.DataDir, nil)
	if err != nil {
		return fmt.Errorf("load node PKI: %w", err)
	}
	if err := store.MigrateDSN(ctx, cfg.DatabaseURL); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	st, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()

	// Lazy: Temporal being down must not stop the API from serving.
	temporal, err := client.NewLazyClient(client.Options{
		HostPort: cfg.TemporalAddress, Namespace: cfg.TemporalNamespace,
		Logger: tlog.NewStructuredLogger(slog.Default()),
	})
	if err != nil {
		return fmt.Errorf("temporal client: %w", err)
	}
	defer temporal.Close()

	agents := &agentapi.Server{Store: st, PKI: authority}
	var secretBackend api.SecretBackend
	var bao *openbao.Client
	if cfg.OpenBaoAddress != "" {
		bao, err = openBao(cfg)
		if err != nil {
			return err
		}
		secretBackend = bao
		agents.Secrets = &openbao.Resolver{Store: st, Bao: bao}
	}
	mail, err := cfg.Mail.relay()
	if err != nil {
		return fmt.Errorf("controlplane: %w", err)
	}
	// Drills are set up before backups: their identity's public half becomes a second
	// recipient on every archive, so an archive written from now on can be drilled
	// (ADR-0051 item 2). Archives written before this cannot, and the drill's verdict
	// says so rather than looking like a corrupt archive.
	var drills *drillwf.Activities
	var drillRecipient string
	if cfg.Drill.enabled() {
		ids, recipient, err := cfg.Drill.identity()
		if err != nil {
			return fmt.Errorf("controlplane: %w", err)
		}
		drillRecipient = recipient
		var drillBao *openbao.Client
		if bao != nil && cfg.Drill.KVMount != "" {
			copy := *bao
			copy.Mount = cfg.Drill.KVMount
			drillBao = &copy
		}
		drills = &drillwf.Activities{
			Config: drill.Config{
				Identities: ids, ScratchDSN: cfg.Drill.DatabaseURL, AdminDSN: cfg.Drill.AdminDatabaseURL,
				Bao: drillBao, LiveMount: cfg.OpenBaoMount,
			},
			Dir: cfg.Backup.Dir, Keep: cfg.Backup.Keep,
		}
	}
	var backups *backupwf.Activities
	if b := cfg.Backup; b.Recipient != "" {
		recipients := []string{b.Recipient}
		if drillRecipient != "" && drillRecipient != b.Recipient {
			recipients = append(recipients, drillRecipient)
		}
		backups = &backupwf.Activities{Recipients: recipients, DatabaseURL: b.DatabaseURL, Bao: bao,
			DataDir: cfg.DataDir, Dir: b.Dir, Keep: b.Keep}
	}
	if drills != nil && backups == nil {
		// Nothing writes archives, so there is nothing to drill. Say so rather than
		// scheduling a drill that fails every Sunday with "no archive".
		slog.WarnContext(ctx, "restore drills are configured but backups are not; no drill will be scheduled")
		drills = nil
	}
	workerCtx, stopWorker := context.WithCancel(ctx)
	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		acts := &deploy.Activities{Store: st, Agents: agents, Builds: agents, Mail: mail}
		if bao != nil {
			// Notification channels' URLs live in OpenBao (ADR-0038).
			acts.Secrets = bao
		}
		deploy.RunWorker(workerCtx, temporal, "", acts)
	}()
	go func() {
		defer workers.Done()
		maintenance.Run(workerCtx, temporal, maintenance.Config{
			Backups:        backups,
			BackupSchedule: backupwf.Schedule{Cron: cfg.Backup.Schedule},
			Drills:         drills,
			DrillSchedule:  drillwf.Schedule{Cron: cfg.Drill.Schedule},
		})
	}()
	defer func() { stopWorker(); workers.Wait() }()

	// Streams (agents, watches, logs) never go idle, so Shutdown alone would wait for
	// them until its timeout: every handler's context ends when shutdown begins.
	streams, endStreams := context.WithCancel(context.WithoutCancel(ctx))
	defer endStreams()
	base := func(net.Listener) context.Context { return streams }
	srv := &http.Server{
		BaseContext: base,
		// ConnectRPC needs HTTP/2 for streaming; h2c serves it without TLS. TLS
		// termination is in front of Bohux (Caddy, ADR-0019) or added with mTLS in S4.
		Handler: h2c.NewHandler(newMux(st, (&api.Server{
			Store: st, Authn: &auth.Authenticator{Store: st}, PKI: authority,
			Deploys: &deploy.Starter{Client: temporal, HealthTimeout: cfg.DeployHealthTimeout},
			Agents:  agents, Secrets: secretBackend, MailRelay: mail.Enabled(),
		}).Mount), &http2.Server{}),
		ReadHeaderTimeout: 10 * time.Second,
	}
	agentTLS, err := agents.TLSConfig()
	if err != nil {
		return err
	}
	agentMux := http.NewServeMux()
	agentMux.Handle(agents.Handler(connect.WithInterceptors(api.NewLoggingInterceptor())))
	agentSrv := &http.Server{Handler: agentMux, TLSConfig: agentTLS, ReadHeaderTimeout: 10 * time.Second, BaseContext: base}

	errc := make(chan error, 2)
	go func() { errc <- srv.Serve(ln) }()
	go func() { errc <- agentSrv.ServeTLS(agentLn, "", "") }()
	slog.InfoContext(ctx, "control plane listening",
		"addr", ln.Addr().String(), "agent_addr", agentLn.Addr().String())

	// Wait for cancellation or for either server to fail, then stop both and collect
	// both results. A server that stopped for any reason but Shutdown is a failure.
	pending := 2
	var serveErr error
	select {
	case serveErr = <-errc:
		pending--
	case <-ctx.Done():
	}
	endStreams()
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	shutdownErr := errors.Join(srv.Shutdown(shutdownCtx), agentSrv.Shutdown(shutdownCtx))
	for ; pending > 0; pending-- {
		if err := <-errc; !errors.Is(err, http.ErrServerClosed) && serveErr == nil {
			serveErr = err
		}
	}
	if serveErr != nil {
		return fmt.Errorf("serve: %w", serveErr)
	}
	if shutdownErr != nil {
		return fmt.Errorf("shutdown: %w", shutdownErr)
	}
	slog.InfoContext(ctx, "control plane stopped")
	return nil
}

// openBao builds the OpenBao client from the token file. OpenBao may be down or
// sealed at start; calls fail until it answers, nothing else does.
func openBao(cfg Config) (*openbao.Client, error) {
	token := cfg.OpenBaoToken
	if cfg.OpenBaoTokenFile != "" {
		raw, err := os.ReadFile(cfg.OpenBaoTokenFile)
		if err != nil {
			return nil, fmt.Errorf("read OpenBao token file: %w", err)
		}
		token = strings.TrimSpace(string(raw))
	}
	if token == "" {
		return nil, errors.New("OpenBao token is empty")
	}
	return &openbao.Client{Addr: cfg.OpenBaoAddress, Token: token, Mount: cfg.OpenBaoMount}, nil
}

// pinger is the part of the store the health check needs.
type pinger interface {
	Ping(ctx context.Context) error
}

// newMux serves /healthz and whatever mount registers (the ConnectRPC services).
func newMux(db pinger, mount func(*http.ServeMux)) *http.ServeMux {
	mux := http.NewServeMux()
	mount(mux)
	// The web UI, a client of the same API (ADR-0012, ADR-0039).
	webui.Mount(mux)
	// Liveness and readiness in one: the control plane is useless without Postgres.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := db.Ping(ctx); err != nil {
			slog.WarnContext(ctx, "health check failed", "err", err)
			http.Error(w, "database unavailable", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ok\n"))
	})
	return mux
}
