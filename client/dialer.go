package client

import (
	"fmt"
	"log/slog"
	"sync"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/neokarl/sdk-go/internal/mtls"
	"github.com/neokarl/sdk-go/transport"
)

// MTLS builds mutual-TLS credentials for dialling peer services: this client
// presents cert/key, and verifies the server's certificate against caFile.
// serverName, when non-empty, overrides the hostname checked against the
// server certificate's SANs.
//
//	creds, err := client.MTLS("certs/ca.crt", "certs/client.crt", "certs/client.key", "")
//	if err != nil {
//	    return err
//	}
//	c, err := client.New(ctx, platformURL, client.WithMTLS(creds))
func MTLS(caFile, certFile, keyFile, serverName string) (credentials.TransportCredentials, error) {
	return mtls.ClientCreds(caFile, certFile, keyFile, serverName)
}

// Resolver maps a service id to the gRPC address it serves on. [Registry]
// satisfies this, resolving from the platform's manifest catalog.
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

// NewDialer builds a dialer over a resolver.
//
// The transport is plaintext unless creds are supplied — pass the credentials
// built by [WithMTLS] to authenticate both ends. extra dial options are
// appended last, so they win over the platform defaults.
func NewDialer(resolver Resolver, log *slog.Logger, creds credentials.TransportCredentials, extra ...grpc.DialOption) *Dialer {
	if log == nil {
		log = slog.Default()
	}
	if creds == nil {
		creds = insecure.NewCredentials()
	}
	opts := append([]grpc.DialOption{
		grpc.WithTransportCredentials(creds),
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
