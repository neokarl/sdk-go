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

	"platform/sdk/transport"
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

// NewGRPCServer builds a gRPC server with the platform interceptors installed.
// Register service implementations via Register, then call Start.
func NewGRPCServer(log *slog.Logger, extra ...grpc.ServerOption) *GRPCServer {
	if log == nil {
		log = slog.Default()
	}
	opts := append([]grpc.ServerOption{
		grpc.StatsHandler(otelgrpc.NewServerHandler()),
		grpc.ChainUnaryInterceptor(transport.ServerUnary(log)),
		grpc.ChainStreamInterceptor(transport.ServerStream(log)),
	}, extra...)

	gs := grpc.NewServer(opts...)
	hs := health.NewServer()
	healthpb.RegisterHealthServer(gs, hs)
	reflection.Register(gs)

	return &GRPCServer{grpc: gs, health: hs, log: log}
}

// Register installs a service implementation, e.g.:
//
//	srv.Register(func(s *grpc.Server) { userpb.RegisterUserServiceServer(s, impl) })
func (s *GRPCServer) Register(fn func(*grpc.Server)) { fn(s.grpc) }

// SetServing marks a service name healthy/unhealthy for health checks. Empty
// name sets the overall server status.
func (s *GRPCServer) SetServing(service string, serving bool) {
	status := healthpb.HealthCheckResponse_NOT_SERVING
	if serving {
		status = healthpb.HealthCheckResponse_SERVING
	}
	s.health.SetServingStatus(service, status)
}

// Start binds addr and serves in a background goroutine, returning a stop
// function for graceful shutdown. Errors binding are returned synchronously.
func (s *GRPCServer) Start(addr string) (stop func(), err error) {
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("service: listen %s: %w", addr, err)
	}
	s.lis = lis
	s.SetServing("", true)
	go func() {
		s.log.LogAttrs(context.Background(), slog.LevelInfo, "grpc.listen", slog.String("addr", addr))
		if serveErr := s.grpc.Serve(lis); serveErr != nil {
			s.log.Error("grpc.serve", "error", serveErr)
		}
	}()
	return s.grpc.GracefulStop, nil
}
