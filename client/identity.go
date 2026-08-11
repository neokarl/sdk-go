package client

import (
	"context"
	"net/http"

	mw "platform/sdk/middleware"
	"platform/sdk/transport"
)

// HeaderUserID carries the calling subject across a REST hop. The tenant and
// request-id headers already have constants in platform/sdk/middleware; this one
// completes the set, matching the gRPC metadata key transport uses.
const HeaderUserID = "X-User-ID"

// injectIdentity copies the caller's identity from ctx onto an outgoing request,
// so a service-to-service REST call carries who is asking — the same thing
// transport.ClientUnary does for gRPC.
//
// It reads transport.Identity, which the SDK's auth middleware places on the
// request context after verifying a bearer. A call made with no identity on the
// context (a cron, a boot-time sweep) sends nothing and will be refused by a
// downstream service that authorizes — the correct default, since such a caller
// is acting for no one.
//
// Nothing here trusts these headers on the way *in*: a receiving service
// verifies the bearer itself. They are a convenience for attribution and a
// carrier for the token, not a claim.
func injectIdentity(ctx context.Context, h http.Header) {
	id, ok := transport.IdentityFrom(ctx)
	if !ok {
		return
	}
	if id.Token != "" {
		h.Set("Authorization", "Bearer "+id.Token)
	}
	if id.UserID != "" {
		h.Set(HeaderUserID, id.UserID)
	}
	if id.TenantID != "" {
		h.Set(mw.HeaderTenantID, id.TenantID)
	}
	if id.RequestID != "" {
		h.Set(mw.HeaderRequestID, id.RequestID)
	}
}
