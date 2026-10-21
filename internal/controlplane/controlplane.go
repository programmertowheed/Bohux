// Package controlplane runs the Bohux control plane: the public API (bohux.v1) over
// ConnectRPC, the agent endpoint (ADR-0006, ADR-0007), and the deploy worker
// (ADR-0026).
package controlplane

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"connectrpc.com/connect"
	"go.temporal.io/sdk/client"
	tlog "go.temporal.io/sdk/log"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	"github.com/bohumix/bohux/internal/agentapi"
	"github.com/bohumix/bohux/internal/api"
	"github.com/bohumix/bohux/internal/auth"
	"github.com/bohumix/bohux/internal/pki"
	"github.com/bohumix/bohux/internal/store"
	"github.com/bohumix/bohux/internal/workflow/deploy"
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
	workerCtx, stopWorker := context.WithCancel(ctx)
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		deploy.RunWorker(workerCtx, temporal, &deploy.Activities{Store: st, Agents: agents})
	}()
	defer func() { stopWorker(); <-workerDone }()

	srv := &http.Server{
		// ConnectRPC needs HTTP/2 for streaming; h2c serves it without TLS. TLS
		// termination is in front of Bohux (Caddy, ADR-0019) or added with mTLS in S4.
		Handler: h2c.NewHandler(newMux(st, (&api.Server{
			Store: st, Authn: &auth.Authenticator{Store: st}, PKI: authority,
			Deploys: &deploy.Starter{Client: temporal},
		}).Mount), &http2.Server{}),
		ReadHeaderTimeout: 10 * time.Second,
	}
	agentTLS, err := agents.TLSConfig()
	if err != nil {
		return err
	}
	agentMux := http.NewServeMux()
	agentMux.Handle(agents.Handler(connect.WithInterceptors(api.NewLoggingInterceptor())))
	agentSrv := &http.Server{Handler: agentMux, TLSConfig: agentTLS, ReadHeaderTimeout: 10 * time.Second}

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

// pinger is the part of the store the health check needs.
type pinger interface {
	Ping(ctx context.Context) error
}

// newMux serves /healthz and whatever mount registers (the ConnectRPC services).
func newMux(db pinger, mount func(*http.ServeMux)) *http.ServeMux {
	mux := http.NewServeMux()
	mount(mux)
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
