package transport

import "context"

// Identity is the caller context propagated across service-to-service gRPC
// calls. It is extracted from request metadata on the server side and injected
// from the Go context on the client side, so a downstream service sees who
// originated the call (for authorization and audit attribution).
type Identity struct {
	UserID    string
	TenantID  string
	RequestID string
	// Token is the raw bearer credential, forwarded so a downstream service can
	// re-verify the caller if it chooses (dev: trusted; prod: verify).
	Token string
}

// gRPC metadata keys (lowercase per the HTTP/2 + gRPC metadata convention).
const (
	mdUserID    = "x-user-id"
	mdTenantID  = "x-tenant-id"
	mdRequestID = "x-request-id"
	mdAuthz     = "authorization"
)

type identityKey struct{}

// WithIdentity returns a context carrying id.
func WithIdentity(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, identityKey{}, id)
}

// IdentityFrom returns the identity carried by ctx, if any.
func IdentityFrom(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(identityKey{}).(Identity)
	return id, ok
}
