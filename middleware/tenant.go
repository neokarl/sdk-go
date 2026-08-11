package middleware

import (
	"github.com/labstack/echo/v4"
)

const HeaderTenantID = "X-Tenant-ID"

// Tenant resolves the tenant scope for the request.
//
// For local dev we accept an X-Tenant-ID header and fall back to the
// configured default — so the demo works without every caller setting it.
// Pass an empty defaultTenant to require the header explicitly.
//
// NEVER ENFORCE ON THIS VALUE. A caller supplies the header, so a caller chooses
// the answer; anything gating access on it can be bypassed with curl. It exists
// for correlation and for development convenience.
//
// The enforceable tenant comes from the verified token: auth.Verifier.TenantOf,
// with platform/sdk/tenancy pushing it into the database so a forgotten filter
// cannot leak. Two things named "tenant" is a trap, which is why this says so.
func Tenant(defaultTenant string) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			tenant := c.Request().Header.Get(HeaderTenantID)
			if tenant == "" {
				tenant = defaultTenant
			}
			ctx := WithTenantID(c.Request().Context(), tenant)
			c.SetRequest(c.Request().WithContext(ctx))
			return next(c)
		}
	}
}
