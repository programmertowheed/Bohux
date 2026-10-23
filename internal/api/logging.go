package api

import (
	"context"
	"log/slog"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
)

// callRecord collects what the access log needs from inner interceptors. The logging
// interceptor runs outermost, so it cannot see context values set further in; the auth
// interceptor fills this in instead.
type callRecord struct {
	principal uuid.UUID
}

type callRecordKey struct{}

func recordPrincipal(ctx context.Context, id uuid.UUID) {
	if r, ok := ctx.Value(callRecordKey{}).(*callRecord); ok {
		r.principal = id
	}
}

// NewLoggingInterceptor writes one access-log line per call: procedure, outcome code,
// duration, and principal ID. It never logs a request or response body, so no secret
// can reach the log through it (rule 3). Messages that must be logged elsewhere go
// through secrets.Message.
func NewLoggingInterceptor() connect.Interceptor { return loggingInterceptor{} }

type loggingInterceptor struct{}

func (loggingInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		rec := &callRecord{}
		start := time.Now()
		resp, err := next(context.WithValue(ctx, callRecordKey{}, rec), req)
		logCall(ctx, req.Spec().Procedure, rec, start, err)
		return resp, err
	}
}

func (loggingInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (loggingInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		rec := &callRecord{}
		start := time.Now()
		err := next(context.WithValue(ctx, callRecordKey{}, rec), conn)
		logCall(ctx, conn.Spec().Procedure, rec, start, err)
		return err
	}
}

func logCall(ctx context.Context, procedure string, rec *callRecord, start time.Time, err error) {
	code := "ok"
	level := slog.LevelInfo
	if err != nil {
		c := connect.CodeOf(err)
		code = c.String()
		if c == connect.CodeInternal || c == connect.CodeUnknown || c == connect.CodeUnavailable {
			level = slog.LevelWarn
		}
	}
	attrs := []slog.Attr{
		slog.String("procedure", procedure),
		slog.String("code", code),
		slog.Duration("duration", time.Since(start)),
	}
	if rec.principal != uuid.Nil {
		attrs = append(attrs, slog.String("principal", rec.principal.String()))
	}
	slog.LogAttrs(ctx, level, "rpc", attrs...)
}
