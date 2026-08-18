package observability

import (
	"context"
	"log/slog"
	"os"
)

type loggerKeyType struct{}

var loggerKey loggerKeyType

// NewLogger builds the default platform logger: JSON to stdout, tagged with the
// service, version and environment so every line is attributable.
//
// Pass the result to [go.neokarl.com/sdk/service.WithLogger]. Handlers should
// prefer [LoggerFrom], which picks up the per-request logger the SDK's HTTP
// middleware installs (already carrying the request id).
func NewLogger(service, version, env string) *slog.Logger {
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})
	return slog.New(handler).With(
		slog.String("service", service),
		slog.String("version", version),
		slog.String("env", env),
	)
}

// WithLogger attaches a logger to ctx so downstream code can pull it without
// threading an explicit logger argument through every call.
func WithLogger(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, loggerKey, l)
}

// LoggerFrom returns the logger stored on ctx, falling back to slog.Default()
// when there is none. It never returns nil.
func LoggerFrom(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(loggerKey).(*slog.Logger); ok && l != nil {
		return l
	}
	return slog.Default()
}
