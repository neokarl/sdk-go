package auth

import (
	"context"
	"errors"
	"testing"
)

func ctxWith(id Identity) context.Context { return WithIdentity(context.Background(), &id) }

// A tenant that cannot be established must be a refusal. Falling back to a default
// would make every boundary built on it decorative.
func TestTenantOfNeverGuesses(t *testing.T) {
	v := &Verifier{}

	if _, err := v.TenantOf(context.Background()); !errors.Is(err, ErrNoTenant) {
		t.Errorf("no identity: err = %v, want ErrNoTenant", err)
	}
	if _, err := v.TenantOf(ctxWith(Identity{Subject: "u1"})); !errors.Is(err, ErrNoTenant) {
		t.Errorf("no claim and no resolver: err = %v, want ErrNoTenant", err)
	}

	v2 := &Verifier{tenantResolver: func(context.Context, string) (string, error) { return "", nil }}
	if _, err := v2.TenantOf(ctxWith(Identity{Subject: "u1"})); !errors.Is(err, ErrNoTenant) {
		t.Errorf("resolver returned nothing: err = %v, want ErrNoTenant", err)
	}
}

func TestTenantOfPrefersTheClaim(t *testing.T) {
	called := false
	v := &Verifier{tenantResolver: func(context.Context, string) (string, error) {
		called = true
		return "from-resolver", nil
	}}
	got, err := v.TenantOf(ctxWith(Identity{Subject: "u1", TenantID: "from-claim"}))
	if err != nil || got != "from-claim" {
		t.Fatalf("TenantOf = %q, %v", got, err)
	}
	if called {
		t.Error("the resolver ran even though the token carried a tenant")
	}
}

// The fallback is a network call per request without a cache, so prove it caches
// and that two subjects never share an answer.
func TestTenantOfCachesPerSubject(t *testing.T) {
	calls := map[string]int{}
	v := &Verifier{tenantResolver: func(_ context.Context, sub string) (string, error) {
		calls[sub]++
		return "tenant-of-" + sub, nil
	}}

	for i := 0; i < 3; i++ {
		if got, err := v.TenantOf(ctxWith(Identity{Subject: "u1"})); err != nil || got != "tenant-of-u1" {
			t.Fatalf("TenantOf = %q, %v", got, err)
		}
	}
	if calls["u1"] != 1 {
		t.Errorf("resolver called %d times, want 1", calls["u1"])
	}
	if got, _ := v.TenantOf(ctxWith(Identity{Subject: "u2"})); got != "tenant-of-u2" {
		t.Errorf("second subject got %q — a cached answer leaked across subjects", got)
	}
}

func TestTenantOfPropagatesResolverFailure(t *testing.T) {
	boom := errors.New("user service down")
	v := &Verifier{tenantResolver: func(context.Context, string) (string, error) { return "", boom }}
	if _, err := v.TenantOf(ctxWith(Identity{Subject: "u1"})); !errors.Is(err, boom) {
		t.Errorf("err = %v, want the resolver's error", err)
	}
}
