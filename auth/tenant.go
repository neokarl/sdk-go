// tenant.go — which tenant the caller belongs to.
//
// This is deliberately not middleware.TenantIDFrom, which reads the X-Tenant-ID
// header: a caller supplies that, so it can choose it. Anything enforcing a
// boundary must use the value below, which comes from a verified token.

package auth

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// ErrNoTenant means the caller's tenant could not be established. Treat it as a
// refusal, never as "no scoping": a tenant that cannot be resolved must not fall
// back to a default, or the boundary is decoration.
var ErrNoTenant = errors.New("auth: no tenant for the caller")

// tenantTTL bounds how long a resolved tenant is reused. Longer than the
// entitlement cache because tenant membership changes far less often than a role.
const tenantTTL = time.Minute

type tenantEntry struct {
	tenant  string
	expires time.Time
}

type tenantCache struct {
	mu      sync.Mutex
	entries map[string]tenantEntry
}

func (c *tenantCache) get(subject string, now time.Time) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[subject]
	if !ok || now.After(e.expires) {
		return "", false
	}
	return e.tenant, true
}

func (c *tenantCache) put(subject, tenant string, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[string]tenantEntry{}
	}
	c.entries[subject] = tenantEntry{tenant: tenant, expires: now.Add(tenantTTL)}
}

// TenantOf returns the tenant of the caller on ctx.
//
// It prefers the token's `tenant` claim. When the issuer does not emit one — the
// common case, since it needs a protocol mapper — it falls back to Config's
// TenantResolver, cached per subject.
//
// It never guesses. A caller with no verified identity, or a subject whose tenant
// cannot be resolved, yields ErrNoTenant.
func (v *Verifier) TenantOf(ctx context.Context) (string, error) {
	id, ok := FromContext(ctx)
	if !ok {
		return "", ErrNoTenant
	}
	if id.TenantID != "" {
		return id.TenantID, nil
	}
	if v.tenantResolver == nil {
		return "", fmt.Errorf("%w: the issuer emits no tenant claim and no TenantResolver is configured", ErrNoTenant)
	}
	if id.Subject == "" {
		return "", ErrNoTenant
	}

	now := time.Now()
	if t, hit := v.tenants.get(id.Subject, now); hit {
		return t, nil
	}
	tenant, err := v.tenantResolver(ctx, id.Subject)
	if err != nil {
		return "", fmt.Errorf("auth: resolve tenant for %q: %w", id.Subject, err)
	}
	if tenant == "" {
		return "", fmt.Errorf("%w: subject %q belongs to no tenant", ErrNoTenant, id.Subject)
	}
	v.tenants.put(id.Subject, tenant, now)
	return tenant, nil
}
