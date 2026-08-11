package middleware

import (
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

const HeaderRequestID = "X-Request-ID"

// RequestID echoes the inbound X-Request-ID or mints one. The id is set on
// both the request context and the response header so callers can correlate
// logs across services.
func RequestID() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			id := c.Request().Header.Get(HeaderRequestID)
			if id == "" {
				id = uuid.NewString()
			}
			c.Response().Header().Set(HeaderRequestID, id)
			ctx := WithRequestID(c.Request().Context(), id)
			c.SetRequest(c.Request().WithContext(ctx))
			return next(c)
		}
	}
}
