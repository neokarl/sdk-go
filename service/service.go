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
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
	"go.opentelemetry.io/contrib/instrumentation/github.com/labstack/echo/otelecho"
	"go.opentelemetry.io/otel/trace"

	"go.neokarl.com/sdk/contracts"
	mw "go.neokarl.com/sdk/middleware"
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
	srv      *http.Server

	// authorizer is nil when authorization was waived with WithoutAuth.
	authorizer Authorizer
	// authDecided records that the caller chose how authorization works.
	authDecided bool
	// authWaived records that the choice was WithoutAuth.
	authWaived bool
	// scopes records the scope each operation declares, for introspection.
	scopes map[string]string
}

type options struct {
	tenant        string
	cors          []string
	log           *slog.Logger
	tracing       trace.TracerProvider
	authenticator Authenticator
	authorizer    Authorizer
	authConfig    authConfig
	authDecided   bool
	authWaived    bool
}

// Option configures New.
type Option func(*options)

// WithTenant sets the default tenant used when a request omits X-Tenant-ID.
func WithTenant(t string) Option { return func(o *options) { o.tenant = t } }

// WithCORS allows browser requests from the given origins — the platform shell
// calls plugin backends cross-origin, so it needs the shell's origin here.
//
// Without this, no cross-origin request is allowed. Passing "*" allows any
// origin and is a development convenience, not a production setting.
func WithCORS(origins ...string) Option { return func(o *options) { o.cors = origins } }

// WithLogger sets the structured logger. Defaults to slog.Default().
func WithLogger(l *slog.Logger) Option { return func(o *options) { o.log = l } }

// WithTracing instruments the HTTP server with the given tracer provider,
// typically from observability.NewTracing.
//
// Without it the server uses whatever provider is installed globally, which for
// a service that never configured OpenTelemetry means no spans are recorded.
func WithTracing(tp trace.TracerProvider) Option {
	return func(o *options) { o.tracing = tp }
}

// healthPaths are served by every service and never require authentication:
// a probe that must authenticate is a probe that reports the identity provider's
// health, not yours.
var healthPaths = []string{"/healthz", "/readyz", "/service.manifest.json"}

// New builds a service around a manifest with the platform conventions baked in:
// the middleware chain (request id, tenant, logger, recover, CORS, and
// authentication when configured), the error envelope, /healthz, /readyz, and
// /service.manifest.json.
func New(manifest contracts.ServiceManifest, opts ...Option) *Service {
	o := options{tenant: "default", log: slog.Default()}
	for _, fn := range opts {
		fn(&o)
	}
	if o.log == nil {
		o.log = slog.Default()
	}

	otelOpts := []otelecho.Option{}
	if o.tracing != nil {
		otelOpts = append(otelOpts, otelecho.WithTracerProvider(o.tracing))
	}

	e := echo.New()
	e.HideBanner = true
	e.HTTPErrorHandler = mw.ErrorHandler()
	e.Use(
		otelecho.Middleware(manifest.ID, otelOpts...), // outermost: one server span per request
		mw.RequestID(),
		mw.Tenant(o.tenant),
		mw.Logger(o.log),
		mw.Recover(),
		mw.CORS(o.cors),
	)
	if o.authenticator != nil {
		// Probes and the manifest are always exempt; a caller's SkipAuthPaths
		// add to this rather than replacing it.
		for _, p := range healthPaths {
			o.authConfig.skipPaths[p] = struct{}{}
		}
		e.Use(authMiddleware(o.authenticator, o.authConfig))
	}

	s := &Service{
		manifest:    manifest,
		e:           e,
		log:         o.log,
		authorizer:  o.authorizer,
		authDecided: o.authDecided,
		authWaived:  o.authWaived,
		scopes:      map[string]string{},
	}
	if o.authWaived {
		o.log.Warn("authorization is OFF — routes declaring a scope are served unguarded", "service", manifest.ID)
	}
	e.GET("/healthz", func(c echo.Context) error { return c.String(http.StatusOK, "ok") })
	e.GET("/readyz", func(c echo.Context) error { return c.String(http.StatusOK, "ok") })
	// The manifest is the source of truth and is served here so the platform (or
	// an operator) can install this service by pointing at its backend.
	e.GET("/service.manifest.json", func(c echo.Context) error { return c.JSON(http.StatusOK, s.manifest) })
	return s
}

// GET registers an API operation. It wires the handler and appends the
// operation to the served manifest's api catalog, so the declared contract and
// the implementation cannot drift. A trailing [Requires] declares the
// permission scope the operation needs.
func (s *Service) GET(opID, path string, h Handler, opts ...RouteOption) {
	s.add(http.MethodGet, opID, path, h, opts...)
}

// POST registers an API operation. See [Service.GET].
func (s *Service) POST(opID, path string, h Handler, opts ...RouteOption) {
	s.add(http.MethodPost, opID, path, h, opts...)
}

// PUT registers an API operation. See [Service.GET].
func (s *Service) PUT(opID, path string, h Handler, opts ...RouteOption) {
	s.add(http.MethodPut, opID, path, h, opts...)
}

// PATCH registers an API operation. See [Service.GET].
func (s *Service) PATCH(opID, path string, h Handler, opts ...RouteOption) {
	s.add(http.MethodPatch, opID, path, h, opts...)
}

// DELETE registers an API operation. See [Service.GET].
func (s *Service) DELETE(opID, path string, h Handler, opts ...RouteOption) {
	s.add(http.MethodDelete, opID, path, h, opts...)
}

func (s *Service) add(method, opID, path string, h Handler, opts ...RouteOption) {
	var rc routeConfig
	for _, fn := range opts {
		fn(&rc)
	}
	if rc.scope != "" {
		s.requireAuthDecision(method, opID, path, rc.scope)
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

// Run serves on addr and blocks until ctx is done, then shuts down gracefully
// with a bounded timeout.
//
// The caller owns the signal handling, so a service can be embedded in a
// process that already handles signals:
//
//	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
//	defer stop()
//	if err := svc.Run(ctx, ":8090"); err != nil {
//	    log.Fatal(err)
//	}
//
// Use [Service.Start] and [Service.Shutdown] instead when the service is one of
// several things a process runs.
func (s *Service) Run(ctx context.Context, addr string) error {
	if err := s.Start(addr); err != nil {
		return err
	}
	<-ctx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	return s.Shutdown(shutdownCtx)
}

// shutdownGrace bounds how long Run waits for in-flight requests to finish.
const shutdownGrace = 10 * time.Second

// Start serves on addr in a background goroutine and returns immediately.
// Errors binding the port are returned synchronously; errors while serving are
// logged. Pair it with [Service.Shutdown].
func (s *Service) Start(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("service: listen %s: %w", addr, err)
	}
	s.srv = &http.Server{Handler: s.e, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		s.log.Info("service.listen", "addr", ln.Addr().String(), "service", s.manifest.ID)
		if err := s.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.log.Error("service.error", "err", err)
		}
	}()
	return nil
}

// Shutdown stops the server gracefully, waiting for in-flight requests to
// finish or for ctx to be done. It is safe to call on a service that never
// started.
func (s *Service) Shutdown(ctx context.Context) error {
	if s.srv == nil {
		return nil
	}
	return s.srv.Shutdown(ctx)
}
