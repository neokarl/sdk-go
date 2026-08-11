// Package logger provides a structured logger used throughout the platform.
// All services log via slog with consistent attribute names.
package logger

import (
	"context"
	"log/slog"
	"os"
)

type contextKey struct{}

var loggerKey = contextKey{}

// New builds the default platform logger. JSON output to stdout with attrs
// for service / version / environment.
func New(service, version, env string) *slog.Logger {
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})
	return slog.New(handler).With(
		slog.String("service", service),
		slog.String("version", version),
		slog.String("env", env),
	)
}

// With attaches a logger to ctx so downstream handlers/services can pull it
// without threading explicit logger args.
func With(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, loggerKey, l)
}

// From returns the logger stored on ctx; falls back to slog.Default().
func From(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(loggerKey).(*slog.Logger); ok && l != nil {
		return l
	}
	return slog.Default()
}
