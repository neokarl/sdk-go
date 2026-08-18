// Package auth is the framework's authentication surface: it verifies OIDC/JWT
// bearer tokens against an issuer's JWKS, for both the HTTP edge (echo
// middleware) and the service plane (gRPC interceptors). Every service —
// platform or plugin — verifies tokens the same way through this package, so a
// forged or expired token is rejected identically everywhere.
//
// The identity source of truth is the *verified* token: handlers and audit read
// claims that were cryptographically checked, never self-asserted metadata.
// That is the fix for the old gap where a caller could put any x-user-id on the
// wire and be believed.
package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
)

// Identity is a verified principal — the validated claims of a bearer token. A
// principal is either a human user or a service account (client-credentials).
type Identity struct {
	Subject   string   // token `sub`
	Username  string   // preferred_username
	Name      string   // full name (`name` claim)
	Email     string   // email claim
	TenantID  string   // custom `tenant` claim, if the issuer emits one
	Roles     []string // realm roles
	Scopes    []string // token `scope`, space-split
	IsService bool     // true for a service-account (client-credentials) token
	Token     string   // the raw token, so a hop can forward it downstream
}

// Verifier validates bearer tokens against an OIDC issuer's JWKS. Safe for
// concurrent use; construct once at boot. The underlying go-oidc verifier
// fetches and caches JWKS and handles key rotation, so verification is local
// (no per-call round-trip to the issuer).
type Verifier struct {
	verifier *oidc.IDTokenVerifier
	issuer   string

	// resourceServer is the Authorization Services client id entitlements are
	// resolved against; empty disables Entitlements/Allowed.
	resourceServer string
	http           *http.Client
	entitlements   entitlementCache

	// tenantResolver fills in the tenant when the issuer emits no claim. Supplied
	// by the service, because resolving it means calling the platform's user
	// service — and the framework must not depend on a particular service.
	tenantResolver func(ctx context.Context, subject string) (string, error)
	tenants        tenantCache
}

// Config configures a Verifier.
type Config struct {
	// IssuerURL is the OIDC issuer, e.g.
	// "http://localhost:8081/realms/platform". OIDC discovery
	// (/.well-known/openid-configuration → jwks_uri) hangs off it.
	IssuerURL string
	// ResourceServer is the Authorization Services client id that owns this
	// platform's permission model (the RPT `audience`). Set it to use
	// Entitlements/Allowed; without it the Verifier authenticates only.
	ResourceServer string
	// TenantResolver resolves a subject's tenant when the token carries no
	// `tenant` claim — typically a lookup against the platform's user service,
	// which this package deliberately does not depend on. Without it, TenantOf
	// fails rather than guessing.
	TenantResolver func(ctx context.Context, subject string) (string, error)
	// Audience must appear in the token's `aud` claim. Required unless
	// SkipAudienceCheck is set.
	//
	// Without an audience check, a token minted for a *different* service by the
	// same issuer verifies here — its signature, issuer and expiry are all
	// genuine. That is the whole point of the claim.
	Audience string
	// SkipAudienceCheck accepts any audience. Signature, issuer and expiry are
	// still enforced.
	//
	// This is for development against an issuer whose access tokens carry a
	// coarse audience (Keycloak's default, without an audience mapper). It is
	// an explicit field because it used to be what happened when you left
	// Audience empty — a security property you could lose by omission.
	SkipAudienceCheck bool
}

// New builds a Verifier by performing OIDC discovery against cfg.IssuerURL. It
// fails if the issuer is unreachable, so call it at boot with a real context
// (e.g. after the IdP is up). Returns nil, nil-safe error otherwise.
func New(ctx context.Context, cfg Config) (*Verifier, error) {
	if cfg.IssuerURL == "" {
		return nil, errors.New("auth: IssuerURL is required")
	}
	if cfg.Audience == "" && !cfg.SkipAudienceCheck {
		return nil, errors.New("auth: Audience is required — set it to this service's expected " +
			"token audience, or set SkipAudienceCheck to accept any audience")
	}
	provider, err := oidc.NewProvider(ctx, cfg.IssuerURL)
	if err != nil {
		return nil, fmt.Errorf("auth: OIDC discovery %q: %w", cfg.IssuerURL, err)
	}
	oc := &oidc.Config{ClientID: cfg.Audience, SkipClientIDCheck: cfg.SkipAudienceCheck}
	return &Verifier{
		verifier:       provider.Verifier(oc),
		issuer:         cfg.IssuerURL,
		resourceServer: cfg.ResourceServer,
		tenantResolver: cfg.TenantResolver,
		http:           &http.Client{Timeout: 10 * time.Second},
	}, nil
}

// tokenClaims mirrors the subset of Keycloak token fields lifted into Identity.
type tokenClaims struct {
	Sub               string `json:"sub"`
	PreferredUsername string `json:"preferred_username"`
	Name              string `json:"name"`
	Email             string `json:"email"`
	Tenant            string `json:"tenant"`
	Scope             string `json:"scope"`
	RealmAccess       struct {
		Roles []string `json:"roles"`
	} `json:"realm_access"`
}

// Verify checks a raw bearer token's signature, issuer, and expiry against the
// issuer's JWKS and returns the verified Identity. Any failure (bad signature,
// wrong issuer, expired, malformed) is an error — the token is not trusted.
func (v *Verifier) Verify(ctx context.Context, raw string) (*Identity, error) {
	tok, err := v.verifier.Verify(ctx, raw)
	if err != nil {
		return nil, fmt.Errorf("auth: verify: %w", err)
	}
	var c tokenClaims
	if err := tok.Claims(&c); err != nil {
		return nil, fmt.Errorf("auth: claims: %w", err)
	}
	id := &Identity{
		Subject:  c.Sub,
		Username: c.PreferredUsername,
		Name:     c.Name,
		Email:    c.Email,
		TenantID: c.Tenant,
		Roles:    c.RealmAccess.Roles,
		Token:    raw,
	}
	if c.Scope != "" {
		id.Scopes = strings.Fields(c.Scope)
	}
	// Keycloak service-account tokens carry username "service-account-<client>".
	id.IsService = strings.HasPrefix(c.PreferredUsername, "service-account-")
	return id, nil
}

// --- context propagation ---------------------------------------------------

type ctxKey struct{}

// WithIdentity returns a context carrying the verified identity.
func WithIdentity(ctx context.Context, id *Identity) context.Context {
	return context.WithValue(ctx, ctxKey{}, id)
}

// IdentityFrom returns the verified identity bound to ctx, if any.
//
// This is the caller the service is acting for, established from a verified
// token. It is not middleware.UserIDFrom, which reads a header the caller
// supplies and so must never be used for a decision.
func IdentityFrom(ctx context.Context) (*Identity, bool) {
	id, ok := ctx.Value(ctxKey{}).(*Identity)
	return id, ok
}
