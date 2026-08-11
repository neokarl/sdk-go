// authz.go — declaring, on the route, what a caller must be permitted to do.
//
// This package deliberately knows nothing about Keycloak, tokens or roles. It
// takes an Authorizer and asks it a yes/no question; platform/sdk/auth supplies
// an implementation backed by the platform's permission model.

package service

import (
	"context"

	"platform/sdk/errors"
)

// Authorizer answers whether the caller on ctx holds a permission scope.
//
// platform/sdk/auth's Verifier implements it. A service can supply its own — a
// test double, or a different policy engine — without this package changing.
type Authorizer interface {
	Allowed(ctx context.Context, scope string) (bool, error)
}

// WithAuthorizer enables scope enforcement for routes declared with Requires.
//
// Without it those routes are served unguarded, which is what makes local
// development work with no identity provider running. That state is logged at
// startup: "authorization is off" should never be something you discover from a
// penetration test.
func WithAuthorizer(a Authorizer) Option { return func(o *options) { o.authorizer = a } }

// RouteOption configures a single operation.
type RouteOption func(*routeConfig)

type routeConfig struct {
	scope string
}

// Requires declares the permission scope a caller must hold for this operation.
//
//	svc.GET("scan.get", "/api/v1/scans/:id", h.getScan, service.Requires("webtools.read"))
//
// The scope is declared beside the operation id, next to the manifest append
// that already keeps the declared API and the implementation in step — so the
// contract, the route and its permission are one line, not three places.
func Requires(scope string) RouteOption {
	return func(rc *routeConfig) { rc.scope = scope }
}

// guard wraps a handler with the scope check. It returns h untouched when the
// route declares no scope, or when no Authorizer is configured.
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

// ScopeFor returns the scope an operation requires, and whether it declares one.
// Exported so a service can assert in a test that every route is guarded.
func (s *Service) ScopeFor(opID string) (string, bool) {
	scope, ok := s.scopes[opID]
	return scope, ok
}
