package pki

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
)

// LoadServerTLS builds the *tls.Config for admin's config-sync gRPC listener:
// it presents (certPath, keyPath) as its server certificate and requires +
// verifies every client certificate against caPath. All three paths are
// mandatory — callers (the admin CLI) are responsible for enforcing that
// --config-tls-ca/-cert/-key are given all together or not at all; this
// function has no directory-scanning fallback.
func LoadServerTLS(caPath, certPath, keyPath string) (*tls.Config, error) {
	pool, err := loadCertPool(caPath)
	if err != nil {
		return nil, err
	}
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, fmt.Errorf("pki: load server keypair: %w", err)
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientCAs:    pool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS13,
	}, nil
}

// LoadClientTLS builds the *tls.Config for a gateway's config-sync client: it
// trusts caPath as the root for verifying admin's server certificate and
// presents (certPath, keyPath) as its own client certificate.
//
// Verification is identity-based, not hostname-based: the default Go TLS
// client behavior (matching the server cert's DNS/IP SANs against the dial
// address) is replaced with VerifyConnection checking that the presented
// server certificate is signed by caPath, carries the ServerAuth extended
// key usage, and identifies as AdminSPIFFEID (see VerifyAdminIdentity) — the
// same check regardless of what address/hostname was dialed (direct, load
// balancer, k8s Service name, IP). This is why there is no
// --config-server-name-style override flag: identity verification is fully
// decoupled from network topology by design.
func LoadClientTLS(caPath, certPath, keyPath string) (*tls.Config, error) {
	pool, err := loadCertPool(caPath)
	if err != nil {
		return nil, err
	}
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, fmt.Errorf("pki: load client keypair: %w", err)
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      pool,
		MinVersion:   tls.VersionTLS13,
		// The default verifier also does hostname matching, which we don't
		// want (see doc comment above) — InsecureSkipVerify disables ALL
		// default verification (chain included), so VerifyConnection below
		// must redo chain verification itself, not just the identity check.
		InsecureSkipVerify: true,
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return fmt.Errorf("pki: server presented no certificate")
			}
			leaf := cs.PeerCertificates[0]
			opts := x509.VerifyOptions{
				Roots:         pool,
				Intermediates: x509.NewCertPool(),
				KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
			}
			for _, c := range cs.PeerCertificates[1:] {
				opts.Intermediates.AddCert(c)
			}
			if _, err := leaf.Verify(opts); err != nil {
				return fmt.Errorf("pki: verify server certificate chain: %w", err)
			}
			return VerifyAdminIdentity(leaf)
		},
	}, nil
}

func loadCertPool(caPath string) (*x509.CertPool, error) {
	pem, err := os.ReadFile(caPath)
	if err != nil {
		return nil, fmt.Errorf("pki: read CA cert %s: %w", caPath, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("pki: %s does not contain a valid CA certificate", caPath)
	}
	return pool, nil
}
