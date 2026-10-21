package api

import (
	"context"
	"errors"
	"log/slog"

	"connectrpc.com/connect"

	bohuxv1 "github.com/bohumix/bohux/gen/bohux/v1"
	"github.com/bohumix/bohux/internal/auth"
	"github.com/bohumix/bohux/internal/store"
)

// toConnect maps an internal error to a Connect error. Known errors keep a fixed,
// value-free message; anything else is logged and returned as a generic internal
// error, so no internal detail (or secret value) reaches the client (rule 3).
func toConnect(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	var ce *connect.Error
	if errors.As(err, &ce) {
		return ce
	}
	switch {
	case errors.Is(err, store.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, store.ErrNotFound)
	case errors.Is(err, store.ErrConflict):
		return connect.NewError(connect.CodeAborted, store.ErrConflict)
	case errors.Is(err, store.ErrAlreadyExists):
		return connect.NewError(connect.CodeAlreadyExists, store.ErrAlreadyExists)
	case errors.Is(err, auth.ErrPermissionDenied):
		return connect.NewError(connect.CodePermissionDenied, auth.ErrPermissionDenied)
	case errors.Is(err, auth.ErrUnauthenticated):
		return connect.NewError(connect.CodeUnauthenticated, auth.ErrUnauthenticated)
	}
	slog.ErrorContext(ctx, "request failed", "err", err)
	return connect.NewError(connect.CodeInternal, errors.New("internal error"))
}

// invalidArgument reports a bad request field, with a typed ErrorDetail (convention 7).
func invalidArgument(field, reason, msg string) error {
	e := connect.NewError(connect.CodeInvalidArgument, errors.New(msg))
	if d, err := connect.NewErrorDetail(&bohuxv1.ErrorDetail{Reason: reason, Field: field}); err == nil {
		e.AddDetail(d)
	}
	return e
}
