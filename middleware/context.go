// Package middleware bundles the Echo middleware stack used by the API server.
// The stack is "middleware-first" per BACKEND.md: handlers stay thin and
// cross-cutting concerns (request id, logger, tenant, auth, audit, error
// handling) live here.
package middleware

import (
	"context"

	"github.com/labstack/echo/v4"
)

type ctxKey string

const (
	keyRequestID ctxKey = "request_id"
	keyTenantID  ctxKey = "tenant_id"
	keyUserID    ctxKey = "user_id"
	keyUserPerms ctxKey = "user_permissions"
)

// RequestIDFrom returns the request id attached by RequestID middleware.
func RequestIDFrom(ctx context.Context) string {
	if v, ok := ctx.Value(keyRequestID).(string); ok {
		return v
	}
	return ""
}

// TenantIDFrom returns the tenant id resolved by Tenant middleware. Empty
// string means "no tenant scope" (admin or public endpoint).
func TenantIDFrom(ctx context.Context) string {
	if v, ok := ctx.Value(keyTenantID).(string); ok {
		return v
	}
	return ""
}

// UserIDFrom returns the authenticated user id, or empty if unauthenticated.
func UserIDFrom(ctx context.Context) string {
	if v, ok := ctx.Value(keyUserID).(string); ok {
		return v
	}
	return ""
}

// UserPermissionsFrom returns the authenticated user's permission set.
func UserPermissionsFrom(ctx context.Context) []string {
	if v, ok := ctx.Value(keyUserPerms).([]string); ok {
		return v
	}
	return nil
}

// WithRequestID/WithTenantID/etc. — exported because services may need to
// build derived contexts (e.g. for queued jobs).
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, keyRequestID, id)
}
func WithTenantID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, keyTenantID, id)
}
func WithUserID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, keyUserID, id)
}
func WithUserPermissions(ctx context.Context, perms []string) context.Context {
	return context.WithValue(ctx, keyUserPerms, perms)
}

// echoCtx extracts the *http.Request context from an Echo handler context.
func echoCtx(c echo.Context) context.Context { return c.Request().Context() }
