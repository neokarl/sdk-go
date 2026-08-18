package client

import (
	"context"
	"net/http"
	"testing"

	mw "github.com/neokarl/sdk-go/middleware"
	"github.com/neokarl/sdk-go/transport"
)

// A service-to-service call must carry the caller. Without this, a downstream
// service that authorizes sees an anonymous request and refuses it — which is
// how enabling authorization anywhere would break every plugin that calls a peer.
func TestInjectIdentityForwardsTheCaller(t *testing.T) {
	ctx := transport.WithIdentity(context.Background(), transport.Identity{
		UserID:    "user-1",
		TenantID:  "tenant-1",
		RequestID: "req-1",
		Token:     "abc.def.ghi",
	})

	h := http.Header{}
	injectIdentity(ctx, h)

	if got := h.Get("Authorization"); got != "Bearer abc.def.ghi" {
		t.Errorf("Authorization = %q", got)
	}
	if got := h.Get(HeaderUserID); got != "user-1" {
		t.Errorf("%s = %q", HeaderUserID, got)
	}
	if got := h.Get(mw.HeaderTenantID); got != "tenant-1" {
		t.Errorf("%s = %q", mw.HeaderTenantID, got)
	}
	if got := h.Get(mw.HeaderRequestID); got != "req-1" {
		t.Errorf("%s = %q", mw.HeaderRequestID, got)
	}
}

// A call made with no identity — a cron, a boot-time sweep — must send nothing
// rather than something forged. It will be refused downstream, which is correct:
// it is acting for no one.
func TestInjectIdentityAddsNothingWhenAnonymous(t *testing.T) {
	h := http.Header{}
	injectIdentity(context.Background(), h)
	if len(h) != 0 {
		t.Errorf("anonymous call sent headers: %v", h)
	}
}

// A partial identity must not produce empty headers, which a downstream service
// could mistake for a present-but-blank subject.
func TestInjectIdentitySkipsEmptyFields(t *testing.T) {
	ctx := transport.WithIdentity(context.Background(), transport.Identity{UserID: "user-1"})
	h := http.Header{}
	injectIdentity(ctx, h)

	if _, ok := h["Authorization"]; ok {
		t.Error("sent an Authorization header with no token")
	}
	if _, ok := h[mw.HeaderTenantID]; ok {
		t.Error("sent a tenant header with no tenant")
	}
	if h.Get(HeaderUserID) != "user-1" {
		t.Error("dropped the one field that was set")
	}
}
