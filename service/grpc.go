// grpc.go — the gRPC half of serving. A one-call gRPC server with the platform
// interceptor chain (identity extraction, logging, panic recovery), health, and
// reflection wired in. gRPC is backend-only (service↔service); the REST half of
// this package (service.go) fronts the browser.

package service

import (
	"context"
	"fmt"
	"log/slog"
	"net"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"

	"github.com/neokarl/sdk-go/internal/mtls"
	"github.com/neokarl/sdk-go/transport"
)

// GRPCServer wraps a *grpc.Server pre-wired with the platform interceptor chain,
// a health service, and reflection. (Named GRPCServer to distinguish it from the
// REST Service in this package.)
type GRPCServer struct {
	grpc   *grpc.Server
	health *health.Server
	log    *slog.Logger
	lis    net.Listener
}

type grpcOptions struct {
	log          *slog.Logger
	tls          *tlsFiles
	serverOpts   []grpc.ServerOption
	unaryChain   []grpc.UnaryServerInterceptor
	streamChain  []grpc.StreamServerInterceptor
	reflectionOn bool
}

type tlsFiles struct{ ca, cert, key string }

// GRPCOption configures [NewGRPCServer].
type GRPCOption func(*grpcOptions)

// WithGRPCLogger sets the logger used for serve and panic events.
func WithGRPCLogger(l *slog.Logger) GRPCOption {
	return func(o *grpcOptions) { o.log = l }
}

// WithMTLS turns on mutual TLS: the channel is encrypted and every caller must
// present a client certificate signed by caFile, which the server verifies.
// The peer's CommonName is then readable in handlers via auth.PeerServiceFrom.
//
//	srv, err := service.NewGRPCServer(
//	    service.WithMTLS("certs/ca.crt", "certs/server.crt", "certs/server.key"),
//	)
//
// Without this the gRPC plane is plaintext, which is fine for local development
// and not fine anywhere a caller can reach you over a network you don't own.
func WithMTLS(caFile, certFile, keyFile string) GRPCOption {
	return func(o *grpcOptions) { o.tls = &tlsFiles{ca: caFile, cert: certFile, key: keyFile} }
}

// WithGRPCInterceptors appends interceptors after the platform chain, so they
// see an identity-populated context.
func WithGRPCInterceptors(unary ...grpc.UnaryServerInterceptor) GRPCOption {
	return func(o *grpcOptions) { o.unaryChain = append(o.unaryChain, unary...) }
}

// WithGRPCStreamInterceptors appends stream interceptors after the platform chain.
func WithGRPCStreamInterceptors(stream ...grpc.StreamServerInterceptor) GRPCOption {
	return func(o *grpcOptions) { o.streamChain = append(o.streamChain, stream...) }
}

// WithGRPCServerOptions passes raw gRPC server options through — the escape
// hatch for anything this package does not model.
func WithGRPCServerOptions(opts ...grpc.ServerOption) GRPCOption {
	return func(o *grpcOptions) { o.serverOpts = append(o.serverOpts, opts...) }
}

// WithoutReflection disables the gRPC reflection service, which is registered
// by default so grpcurl and similar tools work against a service.
func WithoutReflection() GRPCOption {
	return func(o *grpcOptions) { o.reflectionOn = false }
}

// NewGRPCServer builds a gRPC server with the platform interceptors installed:
// identity extraction from call metadata, request logging, and panic recovery,
// plus a health service and (unless disabled) reflection.
//
// Register service implementations with [GRPCServer.Register], then [GRPCServer.Start].
// It returns an error only when an option cannot be satisfied — today, when
// [WithMTLS] certificates fail to load.
func NewGRPCServer(opts ...GRPCOption) (*GRPCServer, error) {
	o := grpcOptions{log: slog.Default(), reflectionOn: true}
	for _, fn := range opts {
		fn(&o)
	}
	if o.log == nil {
		o.log = slog.Default()
	}

	serverOpts := []grpc.ServerOption{
		grpc.StatsHandler(otelgrpc.NewServerHandler()),
		grpc.ChainUnaryInterceptor(append([]grpc.UnaryServerInterceptor{transport.ServerUnary(o.log)}, o.unaryChain...)...),
		grpc.ChainStreamInterceptor(append([]grpc.StreamServerInterceptor{transport.ServerStream(o.log)}, o.streamChain...)...),
	}
	if o.tls != nil {
		creds, err := mtls.ServerCreds(o.tls.ca, o.tls.cert, o.tls.key)
		if err != nil {
			return nil, err
		}
		serverOpts = append(serverOpts, grpc.Creds(creds))
	}
	serverOpts = append(serverOpts, o.serverOpts...)

	gs := grpc.NewServer(serverOpts...)
	hs := health.NewServer()
	healthpb.RegisterHealthServer(gs, hs)
	if o.reflectionOn {
		reflection.Register(gs)
	}

	return &GRPCServer{grpc: gs, health: hs, log: o.log}, nil
}

// Register installs a service implementation, e.g.:
//
//	srv.Register(func(s *grpc.Server) { userpb.RegisterUserServiceServer(s, impl) })
func (s *GRPCServer) Register(fn func(*grpc.Server)) { fn(s.grpc) }

// SetServing marks a service name healthy or unhealthy for gRPC health checks.
// An empty name sets the overall server status.
//
// [GRPCServer.Start] marks the overall status serving. Call this per registered
// service once its dependencies are ready, and again with false if a dependency
// fails, so load balancers and readiness probes see the truth.
func (s *GRPCServer) SetServing(service string, serving bool) {
	status := healthpb.HealthCheckResponse_NOT_SERVING
	if serving {
		status = healthpb.HealthCheckResponse_SERVING
	}
	s.health.SetServingStatus(service, status)
}

// Start binds addr and serves in a background goroutine. It does not block —
// errors binding are returned synchronously, and serve errors are logged. Pair
// it with [GRPCServer.Close].
func (s *GRPCServer) Start(addr string) error {
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("service: listen %s: %w", addr, err)
	}
	s.lis = lis
	s.SetServing("", true)
	go func() {
		s.log.LogAttrs(context.Background(), slog.LevelInfo, "grpc.listen", slog.String("addr", addr))
		if serveErr := s.grpc.Serve(lis); serveErr != nil {
			s.log.Error("grpc.serve", "error", serveErr)
		}
	}()
	return nil
}

// Close stops the server gracefully, waiting for in-flight RPCs to finish or
// for ctx to be done — whichever comes first, at which point it stops abruptly.
func (s *GRPCServer) Close(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		s.grpc.GracefulStop()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		s.grpc.Stop()
		return ctx.Err()
	}
}
