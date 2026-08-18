package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/neokarl/sdk-go/contracts"
)

// templatePath is what keeps a route's echo form and its advertised form in
// step. Every cross-service call resolves through the advertised form, so a
// mismatch here means a caller sends a literal ":id" and gets a 404 that looks
// like the peer is broken.
func TestTemplatePathConvertsEchoParams(t *testing.T) {
	cases := map[string]string{
		"/api/v1/items":                "/api/v1/items",
		"/api/v1/items/:id":            "/api/v1/items/{id}",
		"/api/v1/items/:id/tags/:tag":  "/api/v1/items/{id}/tags/{tag}",
		"/api/v1/reports:generate":     "/api/v1/reports:generate",
		"/api/v1/items/:id/reports:go": "/api/v1/items/{id}/reports:go",
		"/":                            "/",
	}
	for in, want := range cases {
		if got := templatePath(in); got != want {
			t.Errorf("templatePath(%q) = %q, want %q", in, got, want)
		}
	}
}

// Registering a route also declares it. The served manifest is how the platform
// and other services discover what this service offers, so the two must be one
// action rather than two the author has to remember.
func TestRegisteringARouteDeclaresItInTheManifest(t *testing.T) {
	svc := New(manifestFor(t), WithoutAuth())
	svc.GET("item.list", "/api/v1/items", func(c Context) error { return nil })
	svc.POST("item.create", "/api/v1/items", func(c Context) error { return nil })
	svc.DELETE("item.delete", "/api/v1/items/:id", func(c Context) error { return nil })

	apis := svc.Manifest().APIs
	if len(apis) != 3 {
		t.Fatalf("declared %d operations, want 3: %+v", len(apis), apis)
	}
	want := []contracts.APISpec{
		{ID: "item.list", Method: http.MethodGet, Path: "/api/v1/items"},
		{ID: "item.create", Method: http.MethodPost, Path: "/api/v1/items"},
		{ID: "item.delete", Method: http.MethodDelete, Path: "/api/v1/items/{id}"},
	}
	for i, w := range want {
		if apis[i].ID != w.ID || apis[i].Method != w.Method || apis[i].Path != w.Path {
			t.Errorf("api[%d] = %+v, want %+v", i, apis[i], w)
		}
	}
}

// The manifest endpoint is how an operator installs a service by pointing the
// platform at its backend, so it has to serve what the service actually does —
// including routes registered after New.
func TestServedManifestIncludesLaterRoutes(t *testing.T) {
	svc := New(manifestFor(t), WithoutAuth())
	svc.GET("item.list", "/api/v1/items", func(c Context) error { return nil })

	rec := call(svc, http.MethodGet, "/service.manifest.json")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var got contracts.ServiceManifest
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("manifest was not JSON: %s", rec.Body)
	}
	if got.ID != "inventory" {
		t.Errorf("id = %q", got.ID)
	}
	if len(got.APIs) != 1 || got.APIs[0].ID != "item.list" {
		t.Errorf("served APIs = %+v", got.APIs)
	}
}

func TestHealthEndpointsRespond(t *testing.T) {
	svc := New(manifestFor(t), WithoutAuth())
	for _, p := range []string{"/healthz", "/readyz"} {
		if rec := call(svc, http.MethodGet, p); rec.Code != http.StatusOK {
			t.Errorf("%s = %d, want 200", p, rec.Code)
		}
	}
}

// Handlers return errors; the SDK renders them. A handler that returns a
// platform error must never also have written a success body.
func TestHandlerErrorsRenderAsTheEnvelope(t *testing.T) {
	svc := New(manifestFor(t), WithoutAuth())
	svc.GET("item.get", "/api/v1/items/:id", func(c Context) error {
		return NotFound("no item with that id")
	})

	rec := call(svc, http.MethodGet, "/api/v1/items/nope")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body was not JSON: %s", rec.Body)
	}
	if body["success"] != false {
		t.Errorf("success = %v", body["success"])
	}
}

// Start must bind before returning, so a caller that starts a service and
// immediately calls it does not race the listener.
func TestStartBindsBeforeReturning(t *testing.T) {
	svc := New(manifestFor(t), WithoutAuth())
	svc.GET("item.list", "/api/v1/items", func(c Context) error { return OK(c, []string{}) })

	if err := svc.Start("127.0.0.1:0"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = svc.Shutdown(context.Background()) })

	if err := svc.Shutdown(context.Background()); err != nil {
		t.Errorf("Shutdown: %v", err)
	}
}

// A port already in use has to be reported synchronously. Logging it from the
// serve goroutine means the process starts, reports healthy, and serves nothing.
func TestStartReportsABindFailure(t *testing.T) {
	occupied := httptest.NewServer(http.NewServeMux())
	defer occupied.Close()

	svc := New(manifestFor(t), WithoutAuth())
	if err := svc.Start(occupied.Listener.Addr().String()); err == nil {
		_ = svc.Shutdown(context.Background())
		t.Fatal("binding an occupied port returned no error")
	}
}

// Shutdown on a service that never started is a no-op, so a boot path that
// fails halfway can defer it unconditionally.
func TestShutdownWithoutStartIsSafe(t *testing.T) {
	if err := New(manifestFor(t), WithoutAuth()).Shutdown(context.Background()); err != nil {
		t.Errorf("Shutdown before Start: %v", err)
	}
}

// Run returns when its context is done — the property that lets the caller own
// signal handling instead of the SDK.
func TestRunStopsWhenTheContextIsDone(t *testing.T) {
	svc := New(manifestFor(t), WithoutAuth())
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() { done <- svc.Run(ctx, "127.0.0.1:0") }()

	cancel()
	if err := <-done; err != nil {
		t.Errorf("Run returned %v", err)
	}
}
