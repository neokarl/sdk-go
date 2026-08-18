// authz.go — who may call an operation, and proving who is calling.
//
// This package deliberately knows nothing about Keycloak, tokens or roles. It
// takes an Authenticator and an Authorizer and asks them questions;
// github.com/neokarl/sdk-go/auth supplies an implementation of both, backed by the
// platform's OIDC issuer and permission model.

package service

import (
	"context"
	"fmt"
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/neokarl/sdk-go/errors"
)

// Authenticator establishes who is calling. It reads whatever credential the
// request carries, verifies it, and returns a context carrying the caller's
// identity — or an error, which is rendered to the client as-is (auth's
// implementation returns a 401).
//
// auth.Verifier implements this.
type Authenticator interface {
	Authenticate(ctx context.Context, r *http.Request) (context.Context, error)
}

// Authorizer answers whether the caller on ctx holds a permission scope.
//
// auth.Verifier implements this. A service can supply its own — a test double,
// or a different policy engine — without this package changing.
type Authorizer interface {
	Allowed(ctx context.Context, scope string) (bool, error)
}

// AuthOption tunes [WithAuth].
type AuthOption func(*authConfig)

type authConfig struct {
	skipPaths map[string]struct{}
	optional  bool
}

// SkipAuthPaths exempts routes from authentication, on top of the health and
// manifest endpoints which are always exempt.
func SkipAuthPaths(paths ...string) AuthOption {
	return func(c *authConfig) {
		for _, p := range paths {
			c.skipPaths[p] = struct{}{}
		}
	}
}

// OptionalAuth lets requests with no credential through unauthenticated, for a
// service that serves both anonymous and signed-in callers. A credential that
// is present but invalid is still rejected.
//
// Routes declaring [Requires] still fail closed: an anonymous caller holds no
// scopes, so the authorizer denies them.
func OptionalAuth() AuthOption {
	return func(c *authConfig) { c.optional = true }
}

// WithAuth turns on authentication and, when a is also an [Authorizer],
// authorization — the two halves that together make [Requires] mean something.
//
//	v, err := auth.New(ctx, auth.Config{IssuerURL: issuer, ResourceServer: "platform-api"})
//	if err != nil {
//	    return err
//	}
//	svc := service.New(manifest, service.WithAuth(v))
//	svc.GET("item.get", "/api/v1/items/:id", h.get, service.Requires("inventory.read"))
//
// Health probes and the served manifest are exempt automatically; add more with
// [SkipAuthPaths].
//
// If your authenticator and authorizer are different objects, combine this with
// [WithAuthorizer].
func WithAuth(a Authenticator, opts ...AuthOption) Option {
	cfg := authConfig{skipPaths: map[string]struct{}{}}
	for _, fn := range opts {
		fn(&cfg)
	}
	return func(o *options) {
		o.authenticator = a
		o.authConfig = cfg
		o.authDecided = true
		if authz, ok := a.(Authorizer); ok && o.authorizer == nil {
			o.authorizer = authz
		}
	}
}

// WithAuthorizer supplies only the authorization half — use it when your policy
// engine is separate from whatever authenticates the caller.
//
// On its own this does not authenticate anyone. An authorizer reads the
// identity from the request context, and only an [Authenticator] puts one
// there, so [WithAuthorizer] without [WithAuth] denies every scoped route.
func WithAuthorizer(a Authorizer) Option {
	return func(o *options) {
		o.authorizer = a
		o.authDecided = true
	}
}

// WithoutAuth states that this service runs unauthenticated, so routes
// declaring [Requires] are served unguarded.
//
// This exists so local development needs no identity provider. It is an
// explicit opt-out because the alternative — defaulting to it — means a service
// can declare scopes, enforce nothing, and look entirely correct. Never set it
// in an environment reachable by anyone you do not trust.
func WithoutAuth() Option {
	return func(o *options) {
		o.authorizer = nil
		o.authenticator = nil
		o.authDecided = true
		o.authWaived = true
	}
}

// RouteOption configures a single operation.
type RouteOption func(*routeConfig)

type routeConfig struct {
	scope string
}

// Requires declares the permission scope a caller must hold for this operation.
//
//	svc.GET("item.get", "/api/v1/items/:id", h.get, service.Requires("inventory.read"))
//
// The scope is declared beside the operation id, next to the manifest append
// that already keeps the declared API and the implementation in step — so the
// contract, the route and its permission are one line, not three places.
//
// Registering a route with Requires panics unless the service was built with
// [WithAuth], [WithAuthorizer] or [WithoutAuth]. A declared scope that silently
// enforces nothing is worse than declaring no scope at all, so the choice has
// to be made rather than defaulted.
func Requires(scope string) RouteOption {
	return func(rc *routeConfig) { rc.scope = scope }
}

// authMiddleware turns the configured Authenticator into echo middleware.
func authMiddleware(a Authenticator, cfg authConfig) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if _, skip := cfg.skipPaths[c.Path()]; skip {
				return next(c)
			}
			req := c.Request()
			ctx, err := a.Authenticate(req.Context(), req)
			if err != nil {
				// An optional-auth service tolerates *no* credential, never an
				// invalid one.
				if cfg.optional && req.Header.Get(echo.HeaderAuthorization) == "" {
					return next(c)
				}
				return err
			}
			c.SetRequest(req.WithContext(ctx))
			return next(c)
		}
	}
}

// guard wraps a handler with the scope check. It returns h untouched when the
// route declares no scope, or when authorization was explicitly waived.
func (s *Service) guard(h Handler, rc routeConfig) Handler {
	if rc.scope == "" || s.authorizer == nil {
		return h
	}
	return func(c Context) error {
		ok, err := s.authorizer.Allowed(c.Ctx(), rc.scope)
		if err != nil {
			// Fail closed. A policy engine that cannot answer is not a reason to
			// let the request through, and 503 says "ask again" rather than
			// implying the caller lacks the permission.
			s.log.Error("authz: decision failed", "scope", rc.scope, "error", err)
			return errors.New(errors.CodeUnavailable, "authorization is unavailable")
		}
		if !ok {
			return errors.New(errors.CodeForbidden, "requires "+rc.scope)
		}
		return h(c)
	}
}

// requireAuthDecision panics if a route declares a scope on a service that
// never said how it handles authorization. This is a programmer error caught at
// registration, before the service ever serves a request.
func (s *Service) requireAuthDecision(method, opID, path, scope string) {
	if s.authDecided {
		return
	}
	panic(fmt.Sprintf(
		"service: %s %s (op %q) declares Requires(%q) but this service has no authorization configured.\n"+
			"\tPass service.WithAuth(verifier) to enforce it, or service.WithoutAuth() to state\n"+
			"\texplicitly that this service runs unguarded.",
		method, path, opID, scope,
	))
}

// ScopeFor returns the scope an operation requires, and whether it declares one.
// Exported so a service can assert in a test that every route is guarded.
func (s *Service) ScopeFor(opID string) (string, bool) {
	scope, ok := s.scopes[opID]
	return scope, ok
}
