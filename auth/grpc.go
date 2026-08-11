package auth

import (
	"context"
	"strings"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// GRPCUnary is a server interceptor that verifies the caller's bearer token
// (from the "authorization" metadata) and binds the verified identity onto the
// context. Calls without a valid token are rejected with Unauthenticated —
// except gRPC's own health and reflection methods, which stay open so probes
// and tooling work. This is what closes the self-asserted-identity gap on the
// service plane: the server trusts the signed token, not the wire metadata.
func (v *Verifier) GRPCUnary() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if isInfraMethod(info.FullMethod) {
			return handler(ctx, req)
		}
		raw := bearerFromMD(ctx)
		if raw == "" {
			return nil, status.Error(codes.Unauthenticated, "missing bearer token")
		}
		id, err := v.Verify(ctx, raw)
		if err != nil {
			return nil, status.Errorf(codes.Unauthenticated, "invalid token: %v", err)
		}
		return handler(WithIdentity(ctx, id), req)
	}
}

// isInfraMethod reports whether a gRPC method belongs to health or reflection,
// which must stay reachable without a token.
func isInfraMethod(fullMethod string) bool {
	return strings.HasPrefix(fullMethod, "/grpc.health.") ||
		strings.HasPrefix(fullMethod, "/grpc.reflection.")
}

func bearerFromMD(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	vals := md.Get("authorization")
	if len(vals) == 0 {
		return ""
	}
	return bearer(vals[0])
}

// --- client side: service tokens (client-credentials) ----------------------

// ClientCredentials configures a service-to-service token source. A service
// authenticates as itself (an OAuth2 client) to obtain a short-lived,
// auto-refreshing access token for system calls that have no end user.
type ClientCredentials struct {
	TokenURL     string   // issuer token endpoint (…/protocol/openid-connect/token)
	ClientID     string   // the service's client id
	ClientSecret string   // the service's client secret
	Scopes       []string // optional scopes
}

// ServiceTokenSource returns a reusable, self-refreshing token source for the
// given client credentials. Tokens are cached and renewed before expiry.
func ServiceTokenSource(cc ClientCredentials) oauth2.TokenSource {
	conf := &clientcredentials.Config{
		ClientID:     cc.ClientID,
		ClientSecret: cc.ClientSecret,
		TokenURL:     cc.TokenURL,
		Scopes:       cc.Scopes,
	}
	return conf.TokenSource(context.Background())
}

// ServiceTokenUnary is a client interceptor that attaches a service token to
// outbound calls that don't already carry one. A call made on behalf of a user
// already forwards that user's token (via transport identity propagation), so
// this only fills in the system-call case — e.g. a boot-time audit record.
func ServiceTokenUnary(ts oauth2.TokenSource) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		if md, ok := metadata.FromOutgoingContext(ctx); ok && len(md.Get("authorization")) > 0 {
			return invoker(ctx, method, req, reply, cc, opts...)
		}
		tok, err := ts.Token()
		if err != nil {
			return status.Errorf(codes.Unauthenticated, "service token: %v", err)
		}
		ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+tok.AccessToken)
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}
