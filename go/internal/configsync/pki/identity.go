package pki

import (
	"crypto/x509"
	"fmt"
	"strings"
)

// ServerSPIFFEID is the fixed identity every config-sync server certificate
// carries (spiffe://nyro/server) and the identity each proxy's config-sync
// client verifies — see VerifyServerIdentity. Unlike proxy identities, it does
// not vary per instance: config-sync has exactly one logical server peer, and
// pinning it to a constant means
// verification is fully decoupled from network topology (LB, k8s Service
// name, IP, hostname — none of it matters, only the identity in the cert
// does), so there is no TLS server-name override to maintain.
const ServerSPIFFEID = "server"

// identityFromCert extracts the raw path (minus leading slash) from cert's
// spiffe://<trust-domain>/... URI SAN, e.g. "server" or "proxy/<node-id>".
// Returns an error if cert carries no such SAN.
func identityFromCert(cert *x509.Certificate) (string, error) {
	for _, u := range cert.URIs {
		if u.Scheme != "spiffe" || u.Host != spiffeTrustDomain {
			continue
		}
		id := strings.TrimPrefix(u.Path, "/")
		if id == "" {
			continue
		}
		return id, nil
	}
	return "", fmt.Errorf("pki: certificate %q has no spiffe://%s/... URI SAN", cert.Subject.CommonName, spiffeTrustDomain)
}

// ProxyNodeIDFromCert extracts the node identity from a proxy's client
// certificate's SPIFFE URI SAN (spiffe://nyro/proxy/<node-id>), returning
// just the <node-id> segment — the same shape as the proxy's
// self-reported node_id, so callers can substitute one for the other
// transparently. Returns an error if cert carries no spiffe:// URI SAN under
// the expected proxy path shape.
func ProxyNodeIDFromCert(cert *x509.Certificate) (string, error) {
	id, err := identityFromCert(cert)
	if err != nil {
		return "", err
	}
	nodeID, ok := strings.CutPrefix(id, "proxy/")
	if !ok || nodeID == "" {
		return "", fmt.Errorf("pki: certificate %q identity %q is not a spiffe://%s/proxy/<node-id> SAN", cert.Subject.CommonName, id, spiffeTrustDomain)
	}
	return nodeID, nil
}

// VerifyServerIdentity reports whether cert's SPIFFE URI SAN identifies it as
// the config-sync server (spiffe://nyro/server) — see ServerSPIFFEID.
func VerifyServerIdentity(cert *x509.Certificate) error {
	id, err := identityFromCert(cert)
	if err != nil {
		return err
	}
	if id != ServerSPIFFEID {
		return fmt.Errorf("pki: certificate %q identity is %q, want %q", cert.Subject.CommonName, id, ServerSPIFFEID)
	}
	return nil
}
