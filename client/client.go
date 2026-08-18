// Package client is how a service calls other services.
//
// It gives you two things over one connection to the platform: REST calls
// resolved by (serviceID, operationID) against the platform's service catalog,
// and pooled gRPC connections to peers by service id. Either way you never
// hardcode a peer's URL or address — the catalog resolves them, so a peer can
// move or rename a path without breaking you.
//
//	c, err := client.New(ctx, "http://platform-api:8080")
//	if err != nil {
//	    return err
//	}
//	defer c.Close()
//
//	item, err := client.InvokeData[Item](ctx, c, client.Call{
//	    Service: "inventory",
//	    Op:      "item.get",
//	    Path:    map[string]string{"id": id},
//	})
//
// A [Client] is safe for concurrent use and holds a background catalog refresh,
// so build one at startup and close it at shutdown. Building more than one is
// fine — connecting to a second platform, or isolating a test, costs nothing
// but the refresh goroutine.
package client

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// Client is a connection to the platform: a service catalog plus the REST and
// gRPC machinery to call peers through it.
type Client struct {
	registry *Registry
	dialer   *Dialer
}

type clientOptions struct {
	log        *slog.Logger
	creds      credentials.TransportCredentials
	registry   []Option
	dialOpts   []grpc.DialOption
	waitReady  bool
	readyOnErr bool
}

// ClientOption configures [New].
type ClientOption func(*clientOptions)

// WithLogger sets the logger used for dial and refresh events.
func WithLogger(l *slog.Logger) ClientOption {
	return func(o *clientOptions) { o.log = l }
}

// WithMTLS dials peers over mutual TLS using credentials from [MTLS].
// Without it, gRPC connections to peers are plaintext.
func WithMTLS(creds credentials.TransportCredentials) ClientOption {
	return func(o *clientOptions) { o.creds = creds }
}

// WithRegistryOptions passes options through to the underlying catalog
// registry, e.g. [WithRefresh] or [WithHTTPClient].
func WithRegistryOptions(opts ...Option) ClientOption {
	return func(o *clientOptions) { o.registry = append(o.registry, opts...) }
}

// WithDialOptions passes raw gRPC dial options through, appended after the
// platform defaults so they take precedence.
func WithDialOptions(opts ...grpc.DialOption) ClientOption {
	return func(o *clientOptions) { o.dialOpts = append(o.dialOpts, opts...) }
}

// WithWaitReady makes [New] block until the first catalog fetch completes,
// failing if it errors. Use it when a service calls peers during startup and
// should fail fast rather than block on its first call.
func WithWaitReady() ClientOption {
	return func(o *clientOptions) { o.waitReady = true }
}

// New connects to the platform at platformBaseURL and returns a client for
// calling peer services.
//
// The catalog fetch starts in the background; New returns immediately unless
// [WithWaitReady] is set. Call [Client.Close] at shutdown.
func New(ctx context.Context, platformBaseURL string, opts ...ClientOption) (*Client, error) {
	if platformBaseURL == "" {
		return nil, fmt.Errorf("client: platformBaseURL is required")
	}
	var o clientOptions
	for _, fn := range opts {
		fn(&o)
	}

	reg := NewRegistry(platformBaseURL, o.registry...)
	c := &Client{registry: reg, dialer: NewDialer(reg, o.log, o.creds, o.dialOpts...)}

	if o.waitReady {
		if err := reg.WaitReady(ctx); err != nil {
			_ = c.Close()
			return nil, fmt.Errorf("client: platform catalog not ready: %w", err)
		}
	}
	return c, nil
}

// Close releases the client's gRPC connections and stops the catalog refresh.
func (c *Client) Close() error {
	c.registry.Close()
	return c.dialer.Close()
}

// Registry exposes the underlying service catalog, for inspecting what the
// platform advertises. Most callers want [Client.Invoke] instead.
func (c *Client) Registry() *Registry { return c.registry }

// WaitReady blocks until the first catalog fetch completes, or ctx is done.
func (c *Client) WaitReady(ctx context.Context) error { return c.registry.WaitReady(ctx) }

// BaseURL resolves a peer's advertised APIBaseURL from the catalog — for
// reaching endpoints outside the operation catalog, such as an SSE stream, by
// URL.
func (c *Client) BaseURL(serviceID string) (string, error) { return c.registry.BaseURL(serviceID) }

// Invoke calls another service's REST operation by (serviceID, opID). The
// catalog resolves the URL and method, so callers never hardcode paths.
func (c *Client) Invoke(ctx context.Context, call Call) error {
	return c.registry.Invoke(ctx, call)
}

// Conn returns a pooled gRPC connection to a peer service by id, dialling on
// first use. Pass it to a generated client:
//
//	conn, err := c.Conn("inventory")
//	if err != nil {
//	    return err
//	}
//	items := inventorypb.NewInventoryClient(conn)
func (c *Client) Conn(serviceID string) (*grpc.ClientConn, error) {
	return c.dialer.Conn(serviceID)
}

// InvokeData runs a REST [Client.Invoke] and unwraps the platform's standard
// `{ success, data, error }` envelope, returning the typed data.
//
// This is the common case for calling a peer whose handlers use the platform
// envelope, and it removes the per-caller unwrapping boilerplate. Use
// [Client.Invoke] directly for binary responses (pass an *[]byte in Call.Out)
// or for endpoints that do not envelope.
//
// It is a function rather than a method because Go does not allow type
// parameters on methods.
func InvokeData[T any](ctx context.Context, c *Client, call Call) (T, error) {
	var zero T
	// Capture the raw body via the registry's *[]byte fast path, then decode the
	// envelope here so the registry stays envelope-agnostic.
	var raw []byte
	call.Out = &raw
	if err := c.Invoke(ctx, call); err != nil {
		return zero, err
	}
	var env struct {
		Success bool `json:"success"`
		Data    T    `json:"data"`
		Error   *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return zero, fmt.Errorf("client: decode envelope for %s.%s: %w", call.Service, call.Op, err)
	}
	if !env.Success {
		if env.Error != nil {
			return zero, fmt.Errorf("client: %s.%s failed: %s", call.Service, call.Op, env.Error.Message)
		}
		return zero, fmt.Errorf("client: %s.%s failed without error detail", call.Service, call.Op)
	}
	return env.Data, nil
}
