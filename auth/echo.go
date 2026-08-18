package auth

import (
	"context"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	platerr "github.com/neokarl/sdk-go/errors"
	mw "github.com/neokarl/sdk-go/middleware"
	"github.com/neokarl/sdk-go/tenancy"
	"github.com/neokarl/sdk-go/transport"
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

// Authenticate verifies the request's bearer token and returns a context
// carrying the caller's verified identity. It satisfies service.Authenticator,
// which is how [service.WithAuth] installs it.
//
// A missing token is rejected with an Unauthorized platform error, so scoped
// routes fail closed. An invalid token is always rejected, even when the caller
// tolerates anonymous requests — see [Verifier.AuthenticateOptional].
//
// On success the returned context carries the identity in every form the
// platform reads it: an [Identity] for handlers, the middleware keys (user id,
// tenant, roles) for existing code, the enforced tenancy scope, and a
// transport.Identity so an onward call to another service arrives attributed
// rather than anonymous.
func (v *Verifier) Authenticate(ctx context.Context, r *http.Request) (context.Context, error) {
	return v.authenticate(ctx, r, false)
}

// AuthenticateOptional is [Verifier.Authenticate] but lets a request carrying
// no token through unauthenticated, for endpoints that serve both anonymous and
// signed-in callers and decide for themselves. A token that is present but
// invalid is still rejected.
func (v *Verifier) AuthenticateOptional(ctx context.Context, r *http.Request) (context.Context, error) {
	return v.authenticate(ctx, r, true)
}

func (v *Verifier) authenticate(ctx context.Context, r *http.Request, optional bool) (context.Context, error) {
	raw := bearer(r.Header.Get(echo.HeaderAuthorization))
	if raw == "" {
		if optional {
			return ctx, nil
		}
		return ctx, platerr.New(platerr.CodeUnauthorized, "missing bearer token")
	}
	id, err := v.Verify(ctx, raw)
	if err != nil {
		return ctx, platerr.Wrap(err, platerr.CodeUnauthorized, "invalid token")
	}

	ctx = WithIdentity(ctx, id)
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
		ctx = tenancy.WithTenant(ctx, tenant)
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
	return ctx, nil
}

// EchoMiddleware wraps [Verifier.Authenticate] as echo middleware, for services
// that build their own echo server instead of using the service package.
//
// If you use service.New, prefer service.WithAuth — it installs this and the
// matching authorization check together.
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
			req := c.Request()
			ctx, err := v.authenticate(req.Context(), req, cfg.Optional)
			if err != nil {
				return err
			}
			c.SetRequest(req.WithContext(ctx))
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
