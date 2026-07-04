// Package logger centralises structured logging via zerolog. zerolog is
// zero-allocation on the hot path, which matters for the high-throughput
// ingestion workers. Loggers are cheap value types and are carried on the
// request/message context so scoped fields travel with the work.
package logger

import (
	"context"
	"io"
	"os"
	"strings"

	"github.com/rs/zerolog"
)

type ctxKey struct{}

// New builds a JSON zerolog.Logger tagged with the service name. The level is
// one of debug, info, warn or error.
func New(service, level string) zerolog.Logger {
	return build(os.Stdout, service, level)
}

func build(w io.Writer, service, level string) zerolog.Logger {
	return zerolog.New(w).
		Level(parseLevel(level)).
		With().
		Timestamp().
		Str("service", service).
		Logger()
}

func parseLevel(s string) zerolog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return zerolog.DebugLevel
	case "warn", "warning":
		return zerolog.WarnLevel
	case "error":
		return zerolog.ErrorLevel
	default:
		return zerolog.InfoLevel
	}
}

// Into returns a copy of ctx carrying the logger so that request-scoped fields
// (trace id, document id) travel with the context instead of being threaded by
// hand.
func Into(ctx context.Context, l zerolog.Logger) context.Context {
	return context.WithValue(ctx, ctxKey{}, l)
}

// From extracts the logger stored by Into, or a stderr fallback when absent. It
// returns a pointer so callers can chain event methods directly on the result.
func From(ctx context.Context) *zerolog.Logger {
	if l, ok := ctx.Value(ctxKey{}).(zerolog.Logger); ok {
		return &l
	}
	fb := build(os.Stderr, "unknown", "info")
	return &fb
}
