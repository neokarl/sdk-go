// entitlements.go — what the bearer is permitted to do, as opposed to who they
// are (auth.go).
//
// A plugin cannot answer that from the access token alone. The token carries
// realm ROLES; the platform's permission vocabulary is Authorization Services
// SCOPES (e.g. "inventory.read"), and the mapping from one to the other lives in
// Keycloak. Resolving it means asking Keycloak, which is what this does.

package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// umaGrant is the UMA "requesting party token" grant: exchange an access token
// for an RPT describing what its holder may do on a resource server.
const umaGrant = "urn:ietf:params:oauth:grant-type:uma-ticket"

// entitlementTTL bounds how long a resolved scope set is reused.
//
// Short on purpose: a revoked role should stop working in seconds, not minutes.
// Long enough that a burst of requests from one caller costs a single exchange
// rather than one per request.
const entitlementTTL = 30 * time.Second

// maxEntitlementEntries bounds the cache so a stream of distinct tokens cannot
// grow it without limit.
const maxEntitlementEntries = 1024

type entitlementEntry struct {
	scopes  []string
	expires time.Time
}

// entitlementCache memoizes scope sets by token.
type entitlementCache struct {
	mu      sync.Mutex
	entries map[string]entitlementEntry
}

// key hashes the token: the cache is keyed by credential, and a credential
// should not sit in a map key in the clear.
func (c *entitlementCache) key(token string) string {
	sum := sha256.Sum256([]byte(token))
	return base64.RawStdEncoding.EncodeToString(sum[:])
}

func (c *entitlementCache) get(token string, now time.Time) ([]string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[c.key(token)]
	if !ok || now.After(e.expires) {
		return nil, false
	}
	return e.scopes, true
}

func (c *entitlementCache) put(token string, scopes []string, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[string]entitlementEntry{}
	}
	if len(c.entries) >= maxEntitlementEntries {
		// Cheap reclaim: drop what has already expired, and if that frees
		// nothing, clear. A permission cache is not worth an LRU.
		for k, e := range c.entries {
			if now.After(e.expires) {
				delete(c.entries, k)
			}
		}
		if len(c.entries) >= maxEntitlementEntries {
			c.entries = map[string]entitlementEntry{}
		}
	}
	c.entries[c.key(token)] = entitlementEntry{scopes: scopes, expires: now.Add(entitlementTTL)}
}

// Entitlements returns the scopes this resource server grants the bearer of
// rawToken, resolved by Keycloak against the caller's roles.
//
// An empty result is a valid answer — "granted nothing" — not an error:
// Keycloak reports it as 403 access_denied.
//
// Requires Config.ResourceServer. Without it there is no resource server to ask
// about, and the call fails rather than quietly returning no permissions, which
// a caller would be within their rights to read as "denied".
func (v *Verifier) Entitlements(ctx context.Context, rawToken string) ([]string, error) {
	if v.resourceServer == "" {
		return nil, fmt.Errorf("auth: entitlements need Config.ResourceServer (the Authorization Services client id)")
	}
	if rawToken == "" {
		return nil, fmt.Errorf("auth: entitlements need a token")
	}
	now := time.Now()
	if scopes, ok := v.entitlements.get(rawToken, now); ok {
		return scopes, nil
	}

	form := url.Values{}
	form.Set("grant_type", umaGrant)
	form.Set("audience", v.resourceServer)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(v.issuer, "/")+"/protocol/openid-connect/token",
		strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Bearer "+rawToken)

	resp, err := v.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("auth: rpt request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusForbidden {
		v.entitlements.put(rawToken, []string{}, now)
		return []string{}, nil
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("auth: rpt status %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var out struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("auth: decode rpt: %w", err)
	}
	scopes, err := scopesFromRPT(out.AccessToken)
	if err != nil {
		return nil, err
	}
	v.entitlements.put(rawToken, scopes, now)
	return scopes, nil
}

// Allowed reports whether the identity on ctx holds scope.
//
// It satisfies the authorizer interface the service package's route options
// expect, so a plugin wires the verifier once and declares scopes per route.
func (v *Verifier) Allowed(ctx context.Context, scope string) (bool, error) {
	id, ok := IdentityFrom(ctx)
	if !ok || id.Token == "" {
		return false, nil // unauthenticated: not an error, just not allowed
	}
	scopes, err := v.Entitlements(ctx, id.Token)
	if err != nil {
		return false, err
	}
	for _, s := range scopes {
		if s == scope {
			return true, nil
		}
	}
	return false, nil
}

// scopesFromRPT pulls the granted scopes out of an RPT's authorization claim.
//
// The RPT is a signed JWT Keycloak issued moments ago in response to this very
// request, so its payload is read but not re-verified — the transport and the
// exchange are the trust, exactly as the host's BFF reads its own tokens.
func scopesFromRPT(rpt string) ([]string, error) {
	parts := strings.Split(rpt, ".")
	if len(parts) < 2 {
		return nil, fmt.Errorf("auth: malformed rpt")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("auth: decode rpt payload: %w", err)
	}
	var claims struct {
		Authorization struct {
			Permissions []struct {
				Rsname string   `json:"rsname"`
				Scopes []string `json:"scopes"`
			} `json:"permissions"`
		} `json:"authorization"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, fmt.Errorf("auth: unmarshal rpt: %w", err)
	}
	seen := map[string]struct{}{}
	scopes := make([]string, 0)
	for _, p := range claims.Authorization.Permissions {
		for _, s := range p.Scopes {
			if _, dup := seen[s]; dup {
				continue
			}
			seen[s] = struct{}{}
			scopes = append(scopes, s)
		}
	}
	return scopes, nil
}
