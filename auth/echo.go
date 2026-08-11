package auth

import (
	"strings"

	"github.com/labstack/echo/v4"

	platerr "platform/sdk/errors"
	mw "platform/sdk/middleware"
	"platform/sdk/tenancy"
	"platform/sdk/transport"
)

// MiddlewareConfig tunes the echo authentication middleware.
type MiddlewareConfig struct {
	// SkipPaths bypass verification entirely (health probes, public docs).
	SkipPaths []string
	// Optional, when true, lets a request with NO token through
	// unauthenticated (a downstream handler / RBAC decides). A token that is
	// present but invalid is always rejected with 401.
	Optional bool
}

// EchoMiddleware verifies the inbound bearer token and, on success, binds the
// verified identity onto the request context — both as an auth.Identity and via
// the platform middleware keys (user id / tenant / roles) so existing handlers
// read it unchanged.
func (v *Verifier) EchoMiddleware(cfg MiddlewareConfig) echo.MiddlewareFunc {
	skips := make(map[string]struct{}, len(cfg.SkipPaths))
	for _, p := range cfg.SkipPaths {
		skips[p] = struct{}{}
	}
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if _, skip := skips[c.Path()]; skip {
				return next(c)
			}
			raw := bearer(c.Request().Header.Get(echo.HeaderAuthorization))
			if raw == "" {
				if cfg.Optional {
					return next(c)
				}
				return platerr.New(platerr.CodeUnauthorized, "missing bearer token")
			}
			id, err := v.Verify(c.Request().Context(), raw)
			if err != nil {
				return platerr.Wrap(err, platerr.CodeUnauthorized, "invalid token")
			}
			ctx := WithIdentity(c.Request().Context(), id)
			ctx = mw.WithUserID(ctx, id.Subject)
			if id.TenantID != "" {
				ctx = mw.WithTenantID(ctx, id.TenantID)
			}
			// The enforced tenant, which is not the same thing as the line above:
			// mw.TenantID is the header shim, and id.TenantID is only set when the
			// issuer emits a tenant claim (Keycloak doesn't, without a mapper).
			// TenantOf resolves it properly and caches it.
			//
			// A failure here is not a 401. An endpoint that touches no tenant data
			// shouldn't die because the user service was slow; one that does will
			// fail loudly at tenancy.Scoped with ErrNoTenant, which is where the
			// problem is legible.
			if tenant, terr := v.TenantOf(ctx); terr == nil {
				ctx = tenancy.With(ctx, tenant)
			}
			if len(id.Roles) > 0 {
				ctx = mw.WithUserPermissions(ctx, id.Roles)
			}
			// Also as a transport.Identity, so an onward call — gRPC via
			// transport.ClientUnary, REST via the service registry — carries the
			// caller rather than arriving anonymous at a service that authorizes.
			ctx = transport.WithIdentity(ctx, transport.Identity{
				UserID:    id.Subject,
				TenantID:  id.TenantID,
				RequestID: mw.RequestIDFrom(ctx),
				Token:     raw,
			})
			c.SetRequest(c.Request().WithContext(ctx))
			return next(c)
		}
	}
}

// bearer pulls the token out of an "Authorization: Bearer <token>" header.
func bearer(h string) string {
	const prefix = "Bearer "
	if strings.HasPrefix(h, prefix) {
		return strings.TrimSpace(h[len(prefix):])
	}
	return ""
}
