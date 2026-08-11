package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"
)

func rptWith(t *testing.T, perms []map[string]any) string {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"authorization": map[string]any{"permissions": perms},
	})
	if err != nil {
		t.Fatal(err)
	}
	return "hdr." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

func TestScopesFromRPT(t *testing.T) {
	t.Run("flattens and dedupes across resources", func(t *testing.T) {
		rpt := rptWith(t, []map[string]any{
			{"rsname": "web-tools", "scopes": []string{"webtools.read", "webtools.write"}},
			{"rsname": "platform", "scopes": []string{"platform.admin", "webtools.read"}},
		})
		got, err := scopesFromRPT(rpt)
		if err != nil {
			t.Fatalf("scopesFromRPT: %v", err)
		}
		want := map[string]bool{"webtools.read": true, "webtools.write": true, "platform.admin": true}
		if len(got) != len(want) {
			t.Fatalf("got %v, want %d distinct scopes", got, len(want))
		}
		for _, s := range got {
			if !want[s] {
				t.Errorf("unexpected scope %q", s)
			}
		}
	})

	// A grant of nothing must read as an empty set, never as an error a caller
	// might mistake for "try again".
	t.Run("no permissions is an empty set", func(t *testing.T) {
		got, err := scopesFromRPT(rptWith(t, nil))
		if err != nil {
			t.Fatalf("scopesFromRPT: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("got %v, want none", got)
		}
	})

	t.Run("malformed is an error, not silence", func(t *testing.T) {
		if _, err := scopesFromRPT("not-a-jwt"); err == nil {
			t.Error("a malformed RPT must not resolve to zero scopes — that reads as a deny")
		}
	})
}

func TestEntitlementCache(t *testing.T) {
	var c entitlementCache
	now := time.Now()

	c.put("token-a", []string{"webtools.read"}, now)
	if got, ok := c.get("token-a", now); !ok || len(got) != 1 || got[0] != "webtools.read" {
		t.Fatalf("hit = %v, %v", got, ok)
	}

	// Distinct callers must never share a decision.
	if _, ok := c.get("token-b", now); ok {
		t.Error("a different token read another token's entitlements")
	}

	// A revoked role has to stop working promptly, so entries expire.
	if _, ok := c.get("token-a", now.Add(entitlementTTL+time.Second)); ok {
		t.Error("entry outlived its TTL")
	}
	if entitlementTTL > time.Minute {
		t.Errorf("TTL of %v is too long for a permission decision", entitlementTTL)
	}
}

// A stream of distinct tokens must not grow the cache without bound.
func TestEntitlementCacheIsBounded(t *testing.T) {
	var c entitlementCache
	now := time.Now()
	for i := 0; i < maxEntitlementEntries*2; i++ {
		c.put(string(rune(i))+"-token", []string{"s"}, now)
	}
	c.mu.Lock()
	n := len(c.entries)
	c.mu.Unlock()
	if n > maxEntitlementEntries {
		t.Errorf("cache grew to %d, past the %d bound", n, maxEntitlementEntries)
	}
}

// Without a resource server there is nothing to ask about. That must be an
// error, not an empty set — an empty set means "denied", and a misconfiguration
// should not look like a decision.
func TestEntitlementsNeedAResourceServer(t *testing.T) {
	v := &Verifier{issuer: "http://localhost/realms/platform"}
	if _, err := v.Entitlements(context.Background(), "tok"); err == nil {
		t.Error("expected an error when ResourceServer is unset")
	}
}

// An unauthenticated caller is simply not allowed; it is not an error the
// handler has to distinguish.
func TestAllowedIsFalseWithoutAnIdentity(t *testing.T) {
	v := &Verifier{issuer: "http://localhost/realms/platform", resourceServer: "platform"}
	ok, err := v.Allowed(context.Background(), "webtools.read")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Error("an anonymous caller was allowed")
	}
}
