// registry.go — resolving an operation on another service to a concrete HTTP
// call, so a caller never hardcodes a peer's URL paths.
//
// Services reference operations by (serviceID, opID) — say "inventory" and
// "item.get" — and the registry resolves the URL and method from the platform's
// service manifest catalog. An API owner can then rename a path, or move a
// service to a different host, without breaking its callers.
//
// The registry fetches a catalog snapshot at startup, caches it, and refreshes
// in the background. A request issued before the first snapshot lands blocks on
// that initial fetch rather than failing, which keeps boot ordering forgiving.

package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/neokarl/sdk-go/contracts"
)

// Registry resolves (serviceId, opId) into HTTP calls. Safe for
// concurrent use. One per process is plenty.
type Registry struct {
	platformBaseURL string
	httpClient      *http.Client
	refreshEvery    time.Duration

	mu       sync.RWMutex
	catalog  map[string]contracts.ServiceManifest // serviceId → manifest
	loadedAt time.Time
	loadErr  error

	// `ready` is closed once the FIRST successful (or attempted) load
	// completes. Callers that hit Invoke before the boot fetch lands
	// wait on this so they don't see a synthetic "unknown service"
	// just because the registry hasn't initialised yet.
	readyOnce sync.Once
	ready     chan struct{}

	// stop cancels the background refresh loop. Closed by Close.
	stop     chan struct{}
	stopOnce sync.Once
}

// Call describes one operation invocation.
type Call struct {
	// Service is the target service's id (e.g. "inventory").
	Service string
	// Op is the operation id within that service (e.g. "asset.get").
	Op string
	// Path holds replacements for `{name}` placeholders in the
	// operation's path template. Missing keys cause an error rather
	// than producing literal `{name}` segments in the URL.
	Path map[string]string
	// Query is appended as URL query parameters. Empty values are
	// dropped so callers can pass through optional filters without
	// guarding each one.
	Query map[string]string
	// Body, when non-nil, is JSON-encoded and sent. The Content-Type
	// header is set to application/json.
	Body any
	// Out, when non-nil, receives the JSON-decoded response body.
	// `*[]byte` is a special case: it gets the raw response bytes,
	// useful for operations returning DOCX or other binaries.
	Out any
	// Headers are merged on top of the registry defaults.
	Headers map[string]string
}

// Option lets callers tweak registry defaults.
type Option func(*Registry)

// WithRefresh overrides the background refresh interval (default 60s).
func WithRefresh(d time.Duration) Option {
	return func(r *Registry) { r.refreshEvery = d }
}

// WithHTTPClient overrides the underlying http.Client.
func WithHTTPClient(c *http.Client) Option {
	return func(r *Registry) { r.httpClient = c }
}

// NewRegistry builds a Registry rooted at the platform's base URL. The first
// catalog fetch starts immediately in the background; callers that want to fail
// fast at boot rather than block on the first call can use
// [Registry.WaitReady].
//
// Call [Registry.Close] when done — the registry runs a refresh goroutine for
// its lifetime.
func NewRegistry(platformBaseURL string, opts ...Option) *Registry {
	r := &Registry{
		platformBaseURL: strings.TrimRight(platformBaseURL, "/"),
		httpClient:      &http.Client{Timeout: 10 * time.Second},
		refreshEvery:    60 * time.Second,
		catalog:         map[string]contracts.ServiceManifest{},
		ready:           make(chan struct{}),
		stop:            make(chan struct{}),
	}
	for _, opt := range opts {
		opt(r)
	}
	go r.runRefresh()
	return r
}

// Close stops the background catalog refresh. The registry stays usable
// afterwards, serving from the last snapshot it holds; it just stops updating.
// Safe to call more than once.
func (r *Registry) Close() error {
	r.stopOnce.Do(func() { close(r.stop) })
	return nil
}

// WaitReady blocks until the first catalog fetch completes (or the
// context is cancelled). Useful at boot when the caller wants to fail
// fast rather than block on first Invoke.
func (r *Registry) WaitReady(ctx context.Context) error {
	select {
	case <-r.ready:
		return r.LastLoadError()
	case <-ctx.Done():
		return ctx.Err()
	}
}

// LastLoadError reports the most recent catalog-fetch error (nil on
// success). Safe to call before the first fetch — returns nil.
func (r *Registry) LastLoadError() error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.loadErr
}

// Catalog returns a snapshot of the current service manifests keyed by
// id. The returned map is a copy; mutating it is safe.
func (r *Registry) Catalog() map[string]contracts.ServiceManifest {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]contracts.ServiceManifest, len(r.catalog))
	for k, v := range r.catalog {
		out[k] = v
	}
	return out
}

// GRPCAddress resolves a service id to the gRPC address it advertises in its
// manifest, blocking on the first catalog load so peers booted in parallel
// don't see a spurious "not in catalog". Satisfies [Resolver].
func (r *Registry) GRPCAddress(serviceID string) (string, error) {
	<-r.ready
	r.mu.RLock()
	m, ok := r.catalog[serviceID]
	r.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("client: service %q not in catalog", serviceID)
	}
	return m.GRPCAddress, nil
}

// BaseURL resolves a service id to the APIBaseURL it advertises in its manifest,
// blocking on the first catalog load. Useful for endpoints outside the op catalog
// (e.g. an SSE stream) that a peer must reach by URL rather than (serviceId, opId).
func (r *Registry) BaseURL(serviceID string) (string, error) {
	<-r.ready
	r.mu.RLock()
	m, ok := r.catalog[serviceID]
	r.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("client: service %q not in catalog", serviceID)
	}
	if m.APIBaseURL == "" {
		return "", fmt.Errorf("client: service %q advertises no APIBaseURL", serviceID)
	}
	return m.APIBaseURL, nil
}

// Invoke resolves the call to a URL + method, executes the HTTP
// request, and decodes the response. If the registry hasn't loaded
// the initial catalog yet, this blocks on it (subject to ctx).
func (r *Registry) Invoke(ctx context.Context, call Call) error {
	// Block on the first fetch so peers booted in parallel don't see
	// flake-y "unknown service" errors during initial discovery.
	select {
	case <-r.ready:
	case <-ctx.Done():
		return ctx.Err()
	}

	method, fullURL, err := r.Resolve(call)
	if err != nil {
		return err
	}

	var body io.Reader
	if call.Body != nil {
		raw, jerr := json.Marshal(call.Body)
		if jerr != nil {
			return fmt.Errorf("client: marshal body: %w", jerr)
		}
		body = bytes.NewReader(raw)
	}

	req, err := http.NewRequestWithContext(ctx, method, fullURL, body)
	if err != nil {
		return fmt.Errorf("client: build request: %w", err)
	}
	if call.Body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	// Identity travels with the call, exactly as it does over gRPC
	// (transport.ClientUnary). A downstream service that authorizes needs to know
	// who is asking, and "the plugin that called me" is not an answer — an agent
	// acting for a user must be limited to what that user may do.
	injectIdentity(ctx, req.Header)
	// Caller-supplied headers win, so an explicit Authorization is never
	// overwritten by the ambient one.
	for k, v := range call.Headers {
		req.Header.Set(k, v)
	}

	resp, err := r.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("client: %s %s: %w", method, fullURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		return &HTTPError{
			Service:    call.Service,
			Op:         call.Op,
			Method:     method,
			URL:        fullURL,
			StatusCode: resp.StatusCode,
			Body:       string(raw),
		}
	}

	if call.Out == nil {
		// Drain so the connection can be reused.
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	// Binary out: caller passed *[]byte — copy verbatim.
	if pb, ok := call.Out.(*[]byte); ok {
		raw, rerr := io.ReadAll(resp.Body)
		if rerr != nil {
			return fmt.Errorf("client: read body: %w", rerr)
		}
		*pb = raw
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(call.Out); err != nil {
		return fmt.Errorf("client: decode response: %w", err)
	}
	return nil
}

// Resolve builds the method + absolute URL for a call without
// executing it. Exposed so callers that need to construct a request
// manually (e.g. multipart upload) can still benefit from the
// indirection.
func (r *Registry) Resolve(call Call) (string, string, error) {
	r.mu.RLock()
	manifest, ok := r.catalog[call.Service]
	r.mu.RUnlock()
	if !ok {
		return "", "", fmt.Errorf("client: service %q not in catalog", call.Service)
	}
	if manifest.APIBaseURL == "" {
		return "", "", fmt.Errorf("client: service %q has no apiBaseUrl", call.Service)
	}
	var op *contracts.APISpec
	for i := range manifest.APIs {
		if manifest.APIs[i].ID == call.Op {
			op = &manifest.APIs[i]
			break
		}
	}
	if op == nil {
		return "", "", fmt.Errorf("client: service %q has no operation %q", call.Service, call.Op)
	}

	path, err := substitutePath(op.Path, call.Path)
	if err != nil {
		return "", "", fmt.Errorf("client: %s.%s: %w", call.Service, call.Op, err)
	}

	fullURL := strings.TrimRight(manifest.APIBaseURL, "/") + path
	if len(call.Query) > 0 {
		q := url.Values{}
		for k, v := range call.Query {
			if v == "" {
				continue
			}
			q.Set(k, v)
		}
		if encoded := q.Encode(); encoded != "" {
			fullURL += "?" + encoded
		}
	}
	return op.Method, fullURL, nil
}

// substitutePath replaces `{name}` segments in `template` with values
// from `params`. A missing key returns an error rather than leaving
// the literal placeholder — silently producing `/api/v1/items/{id}`
// would be a confusing 404.
func substitutePath(template string, params map[string]string) (string, error) {
	if !strings.Contains(template, "{") {
		return template, nil
	}
	var b strings.Builder
	i := 0
	for i < len(template) {
		c := template[i]
		if c != '{' {
			b.WriteByte(c)
			i++
			continue
		}
		end := strings.IndexByte(template[i:], '}')
		if end < 0 {
			return "", fmt.Errorf("unterminated path placeholder at %d", i)
		}
		name := template[i+1 : i+end]
		val, ok := params[name]
		if !ok {
			return "", fmt.Errorf("missing path param %q", name)
		}
		b.WriteString(url.PathEscape(val))
		i += end + 1
	}
	return b.String(), nil
}

func (r *Registry) runRefresh() {
	r.refresh()
	r.readyOnce.Do(func() { close(r.ready) })

	ticker := time.NewTicker(r.refreshEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			r.refresh()
		case <-r.stop:
			return
		}
	}
}

// refresh pulls the manifest catalog from the platform. Failures don't
// poison the existing snapshot — we keep the last good catalog and
// surface the error via `LastLoadError`. This is intentional: a brief
// platform outage shouldn't take down peer services mid-call.
func (r *Registry) refresh() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	manifests, err := r.fetchManifests(ctx)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.loadErr = err
	if err == nil {
		next := make(map[string]contracts.ServiceManifest, len(manifests))
		for _, m := range manifests {
			next[m.ID] = m
		}
		r.catalog = next
		r.loadedAt = time.Now()
	}
}

func (r *Registry) fetchManifests(ctx context.Context) ([]contracts.ServiceManifest, error) {
	url := r.platformBaseURL + "/api/v1/services/manifests"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := r.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("platform manifests fetch: %d %s", resp.StatusCode, string(raw))
	}
	// `/api/v1/services/manifests` returns the bare ManifestRegistry
	// shape (no envelope) for backward compatibility with the static
	// development.json the frontend loader used to consume.
	var registry contracts.ManifestRegistry
	if err := json.NewDecoder(resp.Body).Decode(&registry); err != nil {
		return nil, fmt.Errorf("decode manifests: %w", err)
	}
	return registry.Services, nil
}

// HTTPError carries the upstream response details so callers can
// distinguish e.g. 404 from 500 without parsing strings.
type HTTPError struct {
	Service    string
	Op         string
	Method     string
	URL        string
	StatusCode int
	Body       string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("serviceregistry %s.%s: %s %s -> %d: %s",
		e.Service, e.Op, e.Method, e.URL, e.StatusCode, e.Body)
}

// IsNotFound is a convenience for `errors.As`-style 404 detection.
func IsNotFound(err error) bool {
	var he *HTTPError
	return errors.As(err, &he) && he.StatusCode == http.StatusNotFound
}
