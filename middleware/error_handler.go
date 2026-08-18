package middleware

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/labstack/echo/v4"

	"go.neokarl.com/sdk/contracts"
	platerr "go.neokarl.com/sdk/errors"
	"go.neokarl.com/sdk/observability"
)

// ErrorHandler is Echo's central HTTPErrorHandler. It turns every error into
// the platform's response envelope and logs the internal cause separately —
// the client never sees stack traces or wrapped error chains.
func ErrorHandler() echo.HTTPErrorHandler {
	return func(err error, c echo.Context) {
		if c.Response().Committed {
			return
		}
		ctx := c.Request().Context()

		// PlatformError — render exactly as declared.
		if pe, ok := platerr.As(err); ok {
			observability.LoggerFrom(ctx).LogAttrs(ctx, levelFor(pe.Status), "handler_error",
				slog.String("code", string(pe.Code)),
				slog.Int("status", pe.Status),
				slog.String("message", pe.Message),
				slog.Any("cause", pe.Cause),
			)
			_ = c.JSON(pe.Status, contracts.Fail(string(pe.Code), pe.Message, pe.Details))
			return
		}

		// Echo's own HTTPError — preserve status, generic body.
		var he *echo.HTTPError
		if errors.As(err, &he) {
			msg := http.StatusText(he.Code)
			if m, ok := he.Message.(string); ok && m != "" {
				msg = m
			}
			observability.LoggerFrom(ctx).LogAttrs(ctx, levelFor(he.Code), "echo_error",
				slog.Int("status", he.Code), slog.String("message", msg))
			_ = c.JSON(he.Code, contracts.Fail(codeFor(he.Code), msg, nil))
			return
		}

		// Unclassified — log full error, return 500 with generic message.
		observability.LoggerFrom(ctx).LogAttrs(ctx, slog.LevelError, "unhandled_error",
			slog.String("error", err.Error()))
		_ = c.JSON(http.StatusInternalServerError,
			contracts.Fail(string(platerr.CodeInternal), "internal server error", nil))
	}
}

func levelFor(status int) slog.Level {
	switch {
	case status >= 500:
		return slog.LevelError
	case status >= 400:
		return slog.LevelWarn
	default:
		return slog.LevelInfo
	}
}

func codeFor(status int) string {
	switch status {
	case http.StatusBadRequest:
		return string(platerr.CodeBadRequest)
	case http.StatusUnauthorized:
		return string(platerr.CodeUnauthorized)
	case http.StatusForbidden:
		return string(platerr.CodeForbidden)
	case http.StatusNotFound:
		return string(platerr.CodeNotFound)
	case http.StatusConflict:
		return string(platerr.CodeConflict)
	case http.StatusTooManyRequests:
		return string(platerr.CodeRateLimited)
	case http.StatusServiceUnavailable:
		return string(platerr.CodeUnavailable)
	case http.StatusNotImplemented:
		return string(platerr.CodeNotImplemented)
	default:
		return string(platerr.CodeInternal)
	}
}
