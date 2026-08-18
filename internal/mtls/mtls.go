// Package mtls builds mutual-TLS credentials for the gRPC service plane, so a
// call is both encrypted and authenticated at the channel: the server verifies
// the client's certificate and vice-versa. Combined with token verification in
// the auth package, a call carries two authenticated facts — which *service* is
// calling (the peer certificate) and which *principal* it acts for (the JWT).
//
// This is internal. It is reached through service.WithMTLS on the serving side,
// client.WithMTLS on the dialling side, and auth.PeerServiceFrom for reading
// the verified peer.
package mtls

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"

	"google.golang.org/grpc/credentials"
)

// ServerCreds builds mutual-TLS server credentials: the server presents
// cert/key and *requires and verifies* a client certificate signed by caFile.
func ServerCreds(caFile, certFile, keyFile string) (credentials.TransportCredentials, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("mtls: load server keypair: %w", err)
	}
	pool, err := loadCAPool(caFile)
	if err != nil {
		return nil, err
	}
	return credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    pool,
		MinVersion:   tls.VersionTLS13,
	}), nil
}

// ClientCreds builds mutual-TLS client credentials: the client presents
// cert/key and verifies the server certificate against caFile. serverName, when
// set, overrides the hostname checked against the server cert's SANs (leave
// empty to use the dial target's authority).
func ClientCreds(caFile, certFile, keyFile, serverName string) (credentials.TransportCredentials, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("mtls: load client keypair: %w", err)
	}
	pool, err := loadCAPool(caFile)
	if err != nil {
		return nil, err
	}
	return credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      pool,
		ServerName:   serverName,
		MinVersion:   tls.VersionTLS13,
	}), nil
}

func loadCAPool(caFile string) (*x509.CertPool, error) {
	pem, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("mtls: read CA %q: %w", caFile, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("mtls: no certificates parsed from %q", caFile)
	}
	return pool, nil
}
