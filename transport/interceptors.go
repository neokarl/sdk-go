package transport

import (
	"context"
	"log/slog"
	"runtime/debug"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// --- server side ---------------------------------------------------------

// serverUnary chains recovery → identity-extract → logging for unary calls.
func ServerUnary(log *slog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp any, err error) {
		start := time.Now()
		ctx = extractIdentity(ctx)
		defer func() {
			if r := recover(); r != nil {
				log.Error("grpc.panic", "method", info.FullMethod, "panic", r, "stack", string(debug.Stack()))
				err = status.Errorf(codes.Internal, "internal error")
			}
		}()
		resp, err = handler(ctx, req)
		log.LogAttrs(ctx, slog.LevelInfo, "grpc.call",
			slog.String("method", info.FullMethod),
			slog.String("code", status.Code(err).String()),
			slog.Duration("took", time.Since(start)))
		return resp, err
	}
}

// serverStream mirrors serverUnary for streaming calls.
func ServerStream(log *slog.Logger) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) (err error) {
		start := time.Now()
		ctx := extractIdentity(ss.Context())
		defer func() {
			if r := recover(); r != nil {
				log.Error("grpc.panic", "method", info.FullMethod, "panic", r, "stack", string(debug.Stack()))
				err = status.Errorf(codes.Internal, "internal error")
			}
		}()
		err = handler(srv, &identityStream{ServerStream: ss, ctx: ctx})
		log.LogAttrs(ctx, slog.LevelInfo, "grpc.stream",
			slog.String("method", info.FullMethod),
			slog.String("code", status.Code(err).String()),
			slog.Duration("took", time.Since(start)))
		return err
	}
}

// identityStream overrides Context() so stream handlers see the extracted identity.
type identityStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *identityStream) Context() context.Context { return s.ctx }

// extractIdentity reads caller metadata into an Identity on the context.
func extractIdentity(ctx context.Context) context.Context {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ctx
	}
	id := Identity{
		UserID:    first(md, mdUserID),
		TenantID:  first(md, mdTenantID),
		RequestID: first(md, mdRequestID),
		Token:     first(md, mdAuthz),
	}
	return WithIdentity(ctx, id)
}

func first(md metadata.MD, key string) string {
	if v := md.Get(key); len(v) > 0 {
		return v[0]
	}
	return ""
}

// --- client side ---------------------------------------------------------

// clientUnary injects the context identity into outgoing metadata.
func ClientUnary() grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		return invoker(injectIdentity(ctx), method, req, reply, cc, opts...)
	}
}

func ClientStream() grpc.StreamClientInterceptor {
	return func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
		return streamer(injectIdentity(ctx), desc, cc, method, opts...)
	}
}

func injectIdentity(ctx context.Context) context.Context {
	id, ok := IdentityFrom(ctx)
	if !ok {
		return ctx
	}
	pairs := make([]string, 0, 8)
	if id.UserID != "" {
		pairs = append(pairs, mdUserID, id.UserID)
	}
	if id.TenantID != "" {
		pairs = append(pairs, mdTenantID, id.TenantID)
	}
	if id.RequestID != "" {
		pairs = append(pairs, mdRequestID, id.RequestID)
	}
	if id.Token != "" {
		pairs = append(pairs, mdAuthz, id.Token)
	}
	if len(pairs) == 0 {
		return ctx
	}
	return metadata.AppendToOutgoingContext(ctx, pairs...)
}
