package client

import (
	"fmt"
	"log/slog"
	"sync"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"platform/sdk/transport"
)

// Resolver maps a service id to the gRPC address it serves on. The platform's
// serviceregistry.Registry satisfies this (resolving from the manifest catalog).
type Resolver interface {
	GRPCAddress(serviceID string) (string, error)
}

// Dialer hands out (cached) gRPC client connections to peer services by id,
// with the platform client interceptors installed: identity propagation plus
// resilience — a default per-call timeout, automatic retry with backoff on
// transient (UNAVAILABLE) failures, and a per-method circuit breaker that fails
// fast when a peer stays down.
//
//	conn, _ := dialer.Conn("user")
//	client := userpb.NewUserServiceClient(conn)
type Dialer struct {
	resolver Resolver
	log      *slog.Logger
	opts     []grpc.DialOption

	mu    sync.Mutex
	conns map[string]*grpc.ClientConn
}

// NewDialer builds a dialer over a resolver. extra dial options are appended to
// the platform defaults (insecure transport + identity-propagating interceptors).
func NewDialer(resolver Resolver, log *slog.Logger, extra ...grpc.DialOption) *Dialer {
	if log == nil {
		log = slog.Default()
	}
	opts := append([]grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithStatsHandler(otelgrpc.NewClientHandler()),
		// Retries live below the interceptors (in the transport), so a call that
		// succeeds on retry never reaches the breaker as a failure; the breaker
		// only counts calls that fail after retries are exhausted.
		grpc.WithDefaultServiceConfig(retryServiceConfig),
		grpc.WithChainUnaryInterceptor(
			transport.ClientUnary(), // identity first, so it's set for the whole call
			timeoutInterceptor(defaultCallTimeout),
			breakerInterceptor(),
		),
		grpc.WithChainStreamInterceptor(transport.ClientStream()),
	}, extra...)
	return &Dialer{resolver: resolver, log: log, opts: opts, conns: map[string]*grpc.ClientConn{}}
}

// Conn returns a cached connection to serviceID, resolving its address and
// dialing on first use. Connections are lazy (grpc.NewClient) — no network I/O
// happens until the first RPC.
func (d *Dialer) Conn(serviceID string) (*grpc.ClientConn, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if c, ok := d.conns[serviceID]; ok {
		return c, nil
	}
	addr, err := d.resolver.GRPCAddress(serviceID)
	if err != nil {
		return nil, err
	}
	if addr == "" {
		return nil, fmt.Errorf("client: service %q advertises no gRPC address", serviceID)
	}
	conn, err := grpc.NewClient(addr, d.opts...)
	if err != nil {
		return nil, fmt.Errorf("client: dial %s (%s): %w", serviceID, addr, err)
	}
	d.conns[serviceID] = conn
	return conn, nil
}

// Close tears down all cached connections.
func (d *Dialer) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, c := range d.conns {
		_ = c.Close()
	}
	d.conns = map[string]*grpc.ClientConn{}
	return nil
}
