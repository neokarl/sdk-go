package middleware

import (
	"log/slog"
	"time"

	"github.com/labstack/echo/v4"

	"go.neokarl.com/sdk/observability"
)

// Logger attaches a request-scoped slog to context (with request_id /
// tenant_id pre-bound) and emits one structured access log per request.
func Logger(base *slog.Logger) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			start := time.Now()
			req := c.Request()
			rid := RequestIDFrom(req.Context())
			tenant := TenantIDFrom(req.Context())

			l := base.With(
				slog.String("request_id", rid),
				slog.String("tenant_id", tenant),
				slog.String("method", req.Method),
				slog.String("path", req.URL.Path),
			)
			c.SetRequest(req.WithContext(observability.WithLogger(req.Context(), l)))

			err := next(c)

			status := c.Response().Status
			elapsed := time.Since(start)
			args := []any{
				slog.Int("status", status),
				slog.Duration("elapsed", elapsed),
				slog.Int64("bytes", c.Response().Size),
			}
			if err != nil {
				args = append(args, slog.String("error", err.Error()))
				l.LogAttrs(req.Context(), slog.LevelError, "request", attrs(args)...)
				return err
			}
			l.LogAttrs(req.Context(), slog.LevelInfo, "request", attrs(args)...)
			return nil
		}
	}
}

// attrs converts a heterogeneous []any (slog.X helpers) into []slog.Attr —
// LogAttrs requires Attrs, not the variadic any of the package-level helpers.
func attrs(vs []any) []slog.Attr {
	out := make([]slog.Attr, 0, len(vs))
	for _, v := range vs {
		if a, ok := v.(slog.Attr); ok {
			out = append(out, a)
		}
	}
	return out
}
