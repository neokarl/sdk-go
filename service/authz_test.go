package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/neokarl/sdk-go/contracts"
)

type stubAuthorizer struct {
	allow bool
	err   error
	asked []string
}

func (s *stubAuthorizer) Allowed(_ context.Context, scope string) (bool, error) {
	s.asked = append(s.asked, scope)
	return s.allow, s.err
}

// stubAuth implements both halves, the way auth.Verifier does.
type stubAuth struct {
	stubAuthorizer
	authenticated bool
}

func (s *stubAuth) Authenticate(ctx context.Context, _ *http.Request) (context.Context, error) {
	s.authenticated = true
	return ctx, nil
}

func manifestFor(t *testing.T) contracts.ServiceManifest {
	t.Helper()
	return contracts.ServiceManifest{ID: "inventory", Name: "Inventory", Version: "0.1.0"}
}

func call(svc *Service, method, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

func TestRequiresAllowsAndDenies(t *testing.T) {
	t.Run("allowed reaches the handler", func(t *testing.T) {
		az := &stubAuthorizer{allow: true}
		svc := New(manifestFor(t), WithAuthorizer(az))
		svc.GET("item.get", "/api/v1/items/:id", func(c Context) error {
			return OK(c, map[string]string{"id": c.Param("id")})
		}, Requires("inventory.read"))

		if rec := call(svc, http.MethodGet, "/api/v1/items/abc"); rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
		}
		if len(az.asked) != 1 || az.asked[0] != "inventory.read" {
			t.Errorf("authorizer asked %v", az.asked)
		}
	})

	t.Run("denied never reaches the handler", func(t *testing.T) {
		reached := false
		svc := New(manifestFor(t), WithAuthorizer(&stubAuthorizer{allow: false}))
		svc.DELETE("item.delete", "/api/v1/items/:id", func(c Context) error {
			reached = true
			return NoContent(c)
		}, Requires("inventory.write"))

		rec := call(svc, http.MethodDelete, "/api/v1/items/abc")
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403; body %s", rec.Code, rec.Body)
		}
		if reached {
			t.Error("the handler ran for a caller that was refused")
		}
	})

	// The property that matters: a policy engine that cannot answer must not be
	// treated as an answer. 503 also says "ask again" rather than implying the
	// caller lacks the permission.
	t.Run("an authorizer error fails closed", func(t *testing.T) {
		reached := false
		svc := New(manifestFor(t), WithAuthorizer(&stubAuthorizer{allow: true, err: errors.New("policy engine down")}))
		svc.POST("item.create", "/api/v1/items", func(c Context) error {
			reached = true
			return OK(c, nil)
		}, Requires("inventory.write"))

		rec := call(svc, http.MethodPost, "/api/v1/items")
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503; body %s", rec.Code, rec.Body)
		}
		if reached {
			t.Error("the handler ran despite an unanswerable policy decision")
		}
	})
}

// A route with no declared scope is unaffected — health, manifest, and anything
// deliberately public.
func TestRoutesWithoutAScopeAreNotGuarded(t *testing.T) {
	svc := New(manifestFor(t), WithAuthorizer(&stubAuthorizer{allow: false}))
	svc.GET("item.list", "/api/v1/items", func(c Context) error { return OK(c, []string{}) })

	if rec := call(svc, http.MethodGet, "/api/v1/items"); rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 for an unscoped route", rec.Code)
	}
}

// WithAuth is the one call that turns on both halves. Authorization without
// authentication is the trap this replaces: an authorizer reads the identity
// from the context, and only the authenticator puts one there.
func TestWithAuthInstallsBothHalves(t *testing.T) {
	a := &stubAuth{stubAuthorizer: stubAuthorizer{allow: true}}
	svc := New(manifestFor(t), WithAuth(a))
	svc.GET("item.get", "/api/v1/items/:id", func(c Context) error { return OK(c, nil) },
		Requires("inventory.read"))

	if rec := call(svc, http.MethodGet, "/api/v1/items/abc"); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	if !a.authenticated {
		t.Error("the request was authorized without ever being authenticated")
	}
	if len(a.asked) != 1 || a.asked[0] != "inventory.read" {
		t.Errorf("authorizer asked %v", a.asked)
	}
}

// Health probes must not require a credential — a probe that authenticates
// reports the identity provider's health, not the service's.
func TestHealthEndpointsSkipAuthentication(t *testing.T) {
	a := &failingAuthenticator{}
	svc := New(manifestFor(t), WithAuth(a))

	for _, path := range []string{"/healthz", "/readyz", "/service.manifest.json"} {
		if rec := call(svc, http.MethodGet, path); rec.Code != http.StatusOK {
			t.Errorf("%s = %d, want 200", path, rec.Code)
		}
	}
	if a.called {
		t.Error("a health probe was sent through authentication")
	}
}

type failingAuthenticator struct{ called bool }

func (f *failingAuthenticator) Authenticate(ctx context.Context, _ *http.Request) (context.Context, error) {
	f.called = true
	return ctx, errors.New("no credential")
}

// The core safety property of the redesign: a service cannot declare a scope and
// silently enforce nothing. Either you configure authorization or you say out
// loud that you are not.
func TestRequiresWithoutAnAuthDecisionPanics(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("registering a scoped route with no auth decision did not panic")
		}
		msg, _ := r.(string)
		// The message has to name the route and both ways out, because the
		// person reading it is seeing this for the first time.
		for _, want := range []string{"/api/v1/items/:id", "inventory.read", "WithAuth", "WithoutAuth"} {
			if !strings.Contains(msg, want) {
				t.Errorf("panic message does not mention %q:\n%s", want, msg)
			}
		}
	}()

	svc := New(manifestFor(t))
	svc.GET("item.get", "/api/v1/items/:id", func(c Context) error { return OK(c, nil) },
		Requires("inventory.read"))
}

// Local development has no identity provider. WithoutAuth is how you say so —
// declared scopes then go inert rather than locking a developer out of their
// own service.
func TestWithoutAuthServesDeclaredRoutes(t *testing.T) {
	svc := New(manifestFor(t), WithoutAuth())
	svc.GET("item.get", "/api/v1/items/:id", func(c Context) error { return OK(c, nil) },
		Requires("inventory.read"))

	if rec := call(svc, http.MethodGet, "/api/v1/items/abc"); rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 with authorization explicitly waived", rec.Code)
	}
}

// The declared scope is introspectable, so a service can assert in its own tests
// that no operation ships unguarded.
func TestScopeForReportsTheDeclaration(t *testing.T) {
	svc := New(manifestFor(t), WithoutAuth())
	svc.GET("item.get", "/api/v1/items/:id", func(c Context) error { return nil }, Requires("inventory.read"))
	svc.GET("item.list", "/api/v1/items", func(c Context) error { return nil })

	if scope, ok := svc.ScopeFor("item.get"); !ok || scope != "inventory.read" {
		t.Errorf("ScopeFor(item.get) = %q, %v", scope, ok)
	}
	if _, ok := svc.ScopeFor("item.list"); ok {
		t.Error("item.list reported a scope it never declared")
	}
}

// Declaring a scope must not disturb the manifest catalog, which is how the
// platform and the frontend resolve operations.
func TestRequiresDoesNotDisturbTheManifest(t *testing.T) {
	svc := New(manifestFor(t), WithoutAuth())
	svc.GET("item.get", "/api/v1/items/:id", func(c Context) error { return nil }, Requires("inventory.read"))

	apis := svc.Manifest().APIs
	if len(apis) != 1 || apis[0].ID != "item.get" || apis[0].Path != "/api/v1/items/{id}" {
		t.Errorf("manifest APIs = %+v", apis)
	}
}
