package middleware

import (
	"log/slog"
	"net/http"
	"runtime/debug"

	"github.com/labstack/echo/v4"

	"github.com/neokarl/sdk-go/contracts"
	"github.com/neokarl/sdk-go/errors"
	"github.com/neokarl/sdk-go/observability"
)

// Recover catches panics, logs them with a stack trace, and returns the
// platform's standard 500 envelope. Replaces echo's stock recover so the
// response shape stays consistent with ErrorHandler.
func Recover() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) (err error) {
			defer func() {
				if r := recover(); r != nil {
					ctx := c.Request().Context()
					observability.LoggerFrom(ctx).LogAttrs(ctx, slog.LevelError, "panic",
						slog.Any("recover", r),
						slog.String("stack", string(debug.Stack())),
					)
					if !c.Response().Committed {
						_ = c.JSON(http.StatusInternalServerError,
							contracts.Fail(string(errors.CodeInternal), "internal server error", nil))
					}
				}
			}()
			return next(c)
		}
	}
}
