// Command certgen mints a dev PKI for the mutual-TLS service plane: a self-
// signed CA, one server certificate (with localhost SANs), and a client
// certificate per service (CN = service name, used as its authenticated
// identity). Output is PEM files under -out (default ./certs).
//
//	go run platform/sdk/cmd/certgen                 # ca + server + client-{engagements,platform}
//	go run platform/sdk/cmd/certgen -clients a,b    # custom service clients
//
// Dev only — real deployments use their PKI or a service mesh. Never commit the
// generated keys.
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"flag"
	"log"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	out := flag.String("out", "certs", "output directory for PEM files")
	clients := flag.String("clients", "engagements,platform", "comma-separated service client names")
	hosts := flag.String("hosts", "localhost,127.0.0.1,::1", "comma-separated server SANs (DNS or IP)")
	flag.Parse()

	if err := os.MkdirAll(*out, 0o755); err != nil {
		log.Fatal(err)
	}

	caCert, caKey := mustCA()
	writePair(*out, "ca", caCert, nil) // CA cert only (its key stays alongside for signing dev certs)
	writeKey(*out, "ca", caKey)

	// Server certificate with the requested SANs.
	serverCert, serverKey := mustLeaf("platform-grpc", splitHosts(*hosts), false, caCert, caKey)
	writePair(*out, "server", serverCert, caCert)
	writeKey(*out, "server", serverKey)

	// One client certificate per service; CN is the authenticated service id.
	for _, name := range strings.Split(*clients, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		cert, key := mustLeaf(name, nil, true, caCert, caKey)
		writePair(*out, "client-"+name, cert, caCert)
		writeKey(*out, "client-"+name, key)
	}
	log.Printf("certgen: wrote CA, server, and client certs to %s/", *out)
}

func mustCA() (*x509.Certificate, *ecdsa.PrivateKey) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		log.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial(),
		Subject:               pkix.Name{CommonName: "platform-dev-ca", Organization: []string{"platform-dev"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		log.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	return cert, key
}

// mustLeaf signs a leaf cert. client toggles clientAuth vs serverAuth EKU.
func mustLeaf(cn string, hosts []string, client bool, ca *x509.Certificate, caKey *ecdsa.PrivateKey) (*x509.Certificate, *ecdsa.PrivateKey) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		log.Fatal(err)
	}
	eku := x509.ExtKeyUsageServerAuth
	if client {
		eku = x509.ExtKeyUsageClientAuth
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial(),
		Subject:      pkix.Name{CommonName: cn, Organization: []string{"platform-dev"}},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(10, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{eku},
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		log.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	return cert, key
}

func serial() *big.Int {
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		log.Fatal(err)
	}
	return n
}

func splitHosts(s string) []string {
	var out []string
	for _, h := range strings.Split(s, ",") {
		if h = strings.TrimSpace(h); h != "" {
			out = append(out, h)
		}
	}
	return out
}

// writePair writes <name>.crt (the leaf, followed by the CA for chain building
// when a CA is supplied).
func writePair(dir, name string, cert, ca *x509.Certificate) {
	path := filepath.Join(dir, name+".crt")
	f, err := os.Create(path)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	_ = pem.Encode(f, &pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
	if ca != nil {
		_ = pem.Encode(f, &pem.Block{Type: "CERTIFICATE", Bytes: ca.Raw})
	}
}

func writeKey(dir, name string, key *ecdsa.PrivateKey) {
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		log.Fatal(err)
	}
	path := filepath.Join(dir, name+".key")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	_ = pem.Encode(f, &pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
}
