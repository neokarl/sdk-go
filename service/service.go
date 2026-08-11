// Package service is the ergonomic entry point for building a platform plugin's
// Go backend. You create a Manifest, register API operations, and Run — the
// package owns the webserver (echo, hidden), the platform middleware chain,
// health endpoints, the served manifest, and graceful shutdown.
//
//	func main() {
//	    svc := service.New(manifest)
//	    svc.GET("item.list",   "/api/v1/items", listItems)
//	    svc.POST("item.create","/api/v1/items", createItem)
//	    svc.Run(":8090")
//	}
package service

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/labstack/echo/v4"
	"go.opentelemetry.io/contrib/instrumentation/github.com/labstack/echo/otelecho"

	"platform/sdk/contracts"
	mw "platform/sdk/middleware"
)

// Manifest is the service manifest a plugin declares. Re-exported so plugins
// import only this package for the common case.
type Manifest = contracts.ServiceManifest

// Handler is a plugin HTTP handler. It receives a framework-agnostic Context and
// returns an error (nil, or one of the Error helpers) which the SDK renders as
// the platform error envelope.
type Handler func(c Context) error

// Service is a running (or about-to-run) plugin backend.
type Service struct {
	manifest contracts.ServiceManifest
	e        *echo.Echo
	log      *slog.Logger

	// authorizer is optional; nil serves Requires-declared routes unguarded.
	authorizer Authorizer
	// scopes records the scope each operation declares, for introspection.
	scopes map[string]string
}

type options struct {
	tenant     string
	cors       []string
	log        *slog.Logger
	authorizer Authorizer
}

// Option configures New.
type Option func(*options)

// WithTenant sets the default tenant used when a request omits X-Tenant-ID.
func WithTenant(t string) Option { return func(o *options) { o.tenant = t } }

// WithCORS sets the allowed CORS origins (the shell calls plugin backends
// cross-origin). Defaults to "*" for local development.
func WithCORS(origins ...string) Option { return func(o *options) { o.cors = origins } }

// WithLogger sets the structured logger.
func WithLogger(l *slog.Logger) Option { return func(o *options) { o.log = l } }

// New builds a service around a manifest with the platform conventions baked in:
// the middleware chain (request id, tenant, logger, recover, CORS), the error
// envelope, /healthz, /readyz, and /service.manifest.json.
func New(manifest contracts.ServiceManifest, opts ...Option) *Service {
	o := options{tenant: "default", cors: []string{"*"}, log: slog.Default()}
	for _, fn := range opts {
		fn(&o)
	}

	e := echo.New()
	e.HideBanner = true
	e.HTTPErrorHandler = mw.ErrorHandler()
	e.Use(
		otelecho.Middleware(manifest.ID), // outermost: one server span per request
		mw.RequestID(),
		mw.Tenant(o.tenant),
		mw.Logger(o.log),
		mw.Recover(),
		mw.CORS(o.cors),
	)

	s := &Service{manifest: manifest, e: e, log: o.log, authorizer: o.authorizer, scopes: map[string]string{}}
	if o.authorizer == nil {
		o.log.Warn("authorization is OFF — routes declaring a scope are served unguarded", "service", manifest.ID)
	}
	e.GET("/healthz", func(c echo.Context) error { return c.String(http.StatusOK, "ok") })
	e.GET("/readyz", func(c echo.Context) error { return c.String(http.StatusOK, "ok") })
	// The manifest is the source of truth and is served here so the platform (or
	// an operator) can install this service by pointing at its backend.
	e.GET("/service.manifest.json", func(c echo.Context) error { return c.JSON(http.StatusOK, s.manifest) })
	return s
}

// GET/POST/PUT/PATCH/DELETE register an API operation. Each call both wires the
// handler AND appends the operation to the served manifest's api catalog, so the
// declared contract and the implementation can never drift.
// A trailing Requires(...) declares the permission scope the operation needs.
func (s *Service) GET(opID, path string, h Handler, opts ...RouteOption) {
	s.add(http.MethodGet, opID, path, h, opts...)
}
func (s *Service) POST(opID, path string, h Handler, opts ...RouteOption) {
	s.add(http.MethodPost, opID, path, h, opts...)
}
func (s *Service) PUT(opID, path string, h Handler, opts ...RouteOption) {
	s.add(http.MethodPut, opID, path, h, opts...)
}
func (s *Service) PATCH(opID, path string, h Handler, opts ...RouteOption) {
	s.add(http.MethodPatch, opID, path, h, opts...)
}
func (s *Service) DELETE(opID, path string, h Handler, opts ...RouteOption) {
	s.add(http.MethodDelete, opID, path, h, opts...)
}

func (s *Service) add(method, opID, path string, h Handler, opts ...RouteOption) {
	var rc routeConfig
	for _, fn := range opts {
		fn(&rc)
	}
	if rc.scope != "" {
		s.scopes[opID] = rc.scope
	}
	h = s.guard(h, rc)
	// Echo routes on ":name" params, but the served APIs catalog uses the
	// "{name}" template convention that callers substitute (the Go registry and
	// the frontend serviceRegistry both replace "{name}"). Record the template
	// form so a consumer's `path: {id}` resolves instead of sending a literal
	// ":id".
	s.manifest.APIs = append(s.manifest.APIs, contracts.APISpec{ID: opID, Method: method, Path: templatePath(path)})
	s.e.Add(method, path, func(ec echo.Context) error { return h(&echoContext{ec}) })
}

// templatePath converts echo-style ":name" path segments to "{name}" so the
// manifest's api catalog matches the (serviceId, opId) resolution convention.
// A colon that isn't a segment prefix (e.g. "reports:generate") is left alone.
func templatePath(p string) string {
	segs := strings.Split(p, "/")
	for i, seg := range segs {
		if strings.HasPrefix(seg, ":") {
			segs[i] = "{" + seg[1:] + "}"
		}
	}
	return strings.Join(segs, "/")
}

// Manifest returns the current manifest (including operations registered so far).
func (s *Service) Manifest() contracts.ServiceManifest { return s.manifest }

// Handler returns the underlying http.Handler. Useful for tests (httptest) or
// for embedding the service in a larger server; Run is the normal entry point.
func (s *Service) Handler() http.Handler { return s.e }

// Echo returns the underlying *echo.Echo — an explicit escape hatch for mature
// services that need capabilities the thin Handler/Context don't cover (route
// groups, custom/auth middleware, streaming). Greenfield plugins should prefer
// GET/POST/... with service.Context; reach for Echo() when migrating an
// existing echo service or when you genuinely need echo directly.
func (s *Service) Echo() *echo.Echo { return s.e }

// Run starts the server and blocks until SIGINT/SIGTERM, then shuts down
// gracefully with a bounded timeout.
func (s *Service) Run(addr string) error {
	srv := &http.Server{Addr: addr, Handler: s.e, ReadHeaderTimeout: 5 * time.Second}

	go func() {
		s.log.Info("service.listen", "addr", addr, "service", s.manifest.ID)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.log.Error("service.error", "err", err)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}
