package auth

import (
	"context"

	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
)

// PeerServiceFrom returns the CommonName of the verified client certificate on
// ctx — the authenticated identity of the *calling service*, as distinct from
// [IdentityFrom], which is the principal that service is acting for.
//
// It is only meaningful inside a gRPC handler served over mutual TLS (see
// service.WithMTLS). Over a plaintext or one-way-TLS channel there is no
// verified peer certificate and this reports false.
func PeerServiceFrom(ctx context.Context) (string, bool) {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return "", false
	}
	tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok {
		return "", false
	}
	chains := tlsInfo.State.VerifiedChains
	if len(chains) == 0 || len(chains[0]) == 0 {
		return "", false
	}
	return chains[0][0].Subject.CommonName, true
}
