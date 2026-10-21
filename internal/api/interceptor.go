package api

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"connectrpc.com/connect"

	"github.com/bohumix/bohux/internal/auth"
)

// Authenticator resolves a bearer token to a principal.
type Authenticator interface {
	AuthenticateAPIToken(ctx context.Context, raw string) (auth.Principal, error)
}

// NewAuthInterceptor authenticates every call, puts the principal in ctx, and applies
// the permission table. Tenant and actor come only from here (rule 10).
func NewAuthInterceptor(a Authenticator) connect.Interceptor {
	return &authInterceptor{authn: a, perms: permissions}
}

type authInterceptor struct {
	authn Authenticator
	perms map[string]Permission
}

func (i *authInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		ctx, err := i.check(ctx, req.Spec().Procedure, req.Header().Get("Authorization"))
		if err != nil {
			return nil, err
		}
		return next(ctx, req)
	}
}

// WrapStreamingClient is a no-op: this interceptor runs on the server.
func (i *authInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (i *authInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		ctx, err := i.check(ctx, conn.Spec().Procedure, conn.RequestHeader().Get("Authorization"))
		if err != nil {
			return err
		}
		return next(ctx, conn)
	}
}

func (i *authInterceptor) check(ctx context.Context, procedure, authorization string) (context.Context, error) {
	perm, ok := i.perms[procedure]
	if !ok {
		// Fail closed. The coverage test makes this unreachable for bohux.v1.
		slog.ErrorContext(ctx, "rpc has no permission entry", "procedure", procedure)
		return ctx, connect.NewError(connect.CodePermissionDenied, auth.ErrPermissionDenied)
	}
	raw, ok := strings.CutPrefix(authorization, "Bearer ")
	if !ok || raw == "" {
		return ctx, connect.NewError(connect.CodeUnauthenticated, auth.ErrUnauthenticated)
	}
	p, err := i.authn.AuthenticateAPIToken(ctx, raw)
	if errors.Is(err, auth.ErrUnauthenticated) {
		return ctx, connect.NewError(connect.CodeUnauthenticated, auth.ErrUnauthenticated)
	}
	if err != nil {
		// Not the caller's fault; the cause is logged, not returned.
		slog.ErrorContext(ctx, "authentication failed", "procedure", procedure, "err", err)
		return ctx, connect.NewError(connect.CodeUnavailable, errors.New("authentication unavailable"))
	}
	if p.Role < perm.MinRole || (perm.TenantWide && !p.TenantWide()) {
		return ctx, connect.NewError(connect.CodePermissionDenied, auth.ErrPermissionDenied)
	}
	return auth.WithPrincipal(ctx, p), nil
}
