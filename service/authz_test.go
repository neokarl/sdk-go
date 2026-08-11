package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"platform/sdk/contracts"
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

func manifestFor(t *testing.T) contracts.ServiceManifest {
	t.Helper()
	return contracts.ServiceManifest{ID: "web-tools", Name: "Web Tools", Version: "0.1.0"}
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
		svc.GET("scan.get", "/api/v1/scans/:id", func(c Context) error {
			return OK(c, map[string]string{"id": c.Param("id")})
		}, Requires("webtools.read"))

		if rec := call(svc, http.MethodGet, "/api/v1/scans/abc"); rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
		}
		if len(az.asked) != 1 || az.asked[0] != "webtools.read" {
			t.Errorf("authorizer asked %v", az.asked)
		}
	})

	t.Run("denied never reaches the handler", func(t *testing.T) {
		reached := false
		svc := New(manifestFor(t), WithAuthorizer(&stubAuthorizer{allow: false}))
		svc.DELETE("scan.delete", "/api/v1/scans/:id", func(c Context) error {
			reached = true
			return NoContent(c)
		}, Requires("webtools.write"))

		rec := call(svc, http.MethodDelete, "/api/v1/scans/abc")
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
		svc := New(manifestFor(t), WithAuthorizer(&stubAuthorizer{allow: true, err: errors.New("keycloak down")}))
		svc.POST("scan.create", "/api/v1/scans", func(c Context) error {
			reached = true
			return OK(c, nil)
		}, Requires("webtools.write"))

		rec := call(svc, http.MethodPost, "/api/v1/scans")
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
	svc.GET("tool.list", "/api/v1/tools", func(c Context) error { return OK(c, []string{}) })

	if rec := call(svc, http.MethodGet, "/api/v1/tools"); rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 for an unscoped route", rec.Code)
	}
}

// Local development has no identity provider. Declared scopes must then be
// inert rather than locking the developer out of their own service.
func TestNoAuthorizerServesDeclaredRoutes(t *testing.T) {
	svc := New(manifestFor(t))
	svc.GET("scan.get", "/api/v1/scans/:id", func(c Context) error { return OK(c, nil) },
		Requires("webtools.read"))

	if rec := call(svc, http.MethodGet, "/api/v1/scans/abc"); rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 with no authorizer configured", rec.Code)
	}
}

// The declared scope is introspectable, so a service can assert in its own tests
// that no operation ships unguarded.
func TestScopeForReportsTheDeclaration(t *testing.T) {
	svc := New(manifestFor(t))
	svc.GET("scan.get", "/api/v1/scans/:id", func(c Context) error { return nil }, Requires("webtools.read"))
	svc.GET("tool.list", "/api/v1/tools", func(c Context) error { return nil })

	if scope, ok := svc.ScopeFor("scan.get"); !ok || scope != "webtools.read" {
		t.Errorf("ScopeFor(scan.get) = %q, %v", scope, ok)
	}
	if _, ok := svc.ScopeFor("tool.list"); ok {
		t.Error("tool.list reported a scope it never declared")
	}
}

// Declaring a scope must not disturb the manifest catalog, which is how the
// platform and the frontend resolve operations.
func TestRequiresDoesNotDisturbTheManifest(t *testing.T) {
	svc := New(manifestFor(t))
	svc.GET("scan.get", "/api/v1/scans/:id", func(c Context) error { return nil }, Requires("webtools.read"))

	apis := svc.Manifest().APIs
	if len(apis) != 1 || apis[0].ID != "scan.get" || apis[0].Path != "/api/v1/scans/{id}" {
		t.Errorf("manifest APIs = %+v", apis)
	}
}
