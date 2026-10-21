// Package controlplane runs the Bohux control plane: the public API (bohux.v1) over
// ConnectRPC and, later, the agent endpoint (ADR-0006, ADR-0007).
package controlplane

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	"github.com/bohumix/bohux/internal/store"
)

// Config is everything the control plane needs at start. See docs/reference/config.md.
type Config struct {
	// ListenAddr is the TCP address the API listens on.
	ListenAddr string
	// DatabaseURL is the Postgres DSN. Its role must own the schema and must not bypass
	// row-level security (ADR-0021, data-model.md).
	DatabaseURL string
}

// shutdownTimeout bounds how long in-flight requests get after the context ends.
const shutdownTimeout = 10 * time.Second

// Run migrates the database, then serves until ctx is cancelled. A clean shutdown
// returns nil.
func Run(ctx context.Context, cfg Config) error {
	if cfg.DatabaseURL == "" {
		return errors.New("controlplane: database URL is required")
	}
	ln, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.ListenAddr, err)
	}
	return Serve(ctx, ln, cfg)
}

// Serve is Run on an existing listener, which it closes.
func Serve(ctx context.Context, ln net.Listener, cfg Config) error {
	defer func() { _ = ln.Close() }()
	if err := store.MigrateDSN(ctx, cfg.DatabaseURL); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	st, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()

	srv := &http.Server{
		// ConnectRPC needs HTTP/2 for streaming; h2c serves it without TLS. TLS
		// termination is in front of Bohux (Caddy, ADR-0019) or added with mTLS in S4.
		Handler:           h2c.NewHandler(newMux(st), &http2.Server{}),
		ReadHeaderTimeout: 10 * time.Second,
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	slog.InfoContext(ctx, "control plane listening", "addr", ln.Addr().String())

	select {
	case err := <-errc:
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	if err := <-errc; !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve: %w", err)
	}
	slog.InfoContext(ctx, "control plane stopped")
	return nil
}

// pinger is the part of the store the health check needs.
type pinger interface {
	Ping(ctx context.Context) error
}

func newMux(db pinger) *http.ServeMux {
	mux := http.NewServeMux()
	// Liveness and readiness in one: the control plane is useless without Postgres.
	// ConnectRPC service handlers are mounted here as they are written (S3c onward).
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
