package pki

import (
	"crypto/x509"
	"fmt"
	"strings"
)

// AdminSPIFFEID is the fixed identity every admin server certificate carries
// (spiffe://nyro/admin) and the identity gateway's config-sync client
// verifies admin's server certificate against — see VerifyAdminIdentity.
// Unlike gateway identities, it does not vary per instance: config-sync has
// exactly one logical admin peer, and pinning it to a constant means
// verification is fully decoupled from network topology (LB, k8s Service
// name, IP, hostname — none of it matters, only the identity in the cert
// does), so there is no --config-server-name-style override to maintain.
const AdminSPIFFEID = "admin"

// identityFromCert extracts the raw path (minus leading slash) from cert's
// spiffe://<trust-domain>/... URI SAN, e.g. "admin" or "gateway/<node-id>".
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

// GatewayNodeIDFromCert extracts the node identity from a gateway's client
// certificate's SPIFFE URI SAN (spiffe://nyro/gateway/<node-id>), returning
// just the <node-id> segment — the same shape as the gateway's
// self-reported node_id, so callers can substitute one for the other
// transparently. Returns an error if cert carries no spiffe:// URI SAN under
// the expected gateway path shape.
func GatewayNodeIDFromCert(cert *x509.Certificate) (string, error) {
	id, err := identityFromCert(cert)
	if err != nil {
		return "", err
	}
	nodeID, ok := strings.CutPrefix(id, "gateway/")
	if !ok || nodeID == "" {
		return "", fmt.Errorf("pki: certificate %q identity %q is not a spiffe://%s/gateway/<node-id> SAN", cert.Subject.CommonName, id, spiffeTrustDomain)
	}
	return nodeID, nil
}

// VerifyAdminIdentity reports whether cert's SPIFFE URI SAN identifies it as
// admin (spiffe://nyro/admin) — see AdminSPIFFEID.
func VerifyAdminIdentity(cert *x509.Certificate) error {
	id, err := identityFromCert(cert)
	if err != nil {
		return err
	}
	if id != AdminSPIFFEID {
		return fmt.Errorf("pki: certificate %q identity is %q, want %q", cert.Subject.CommonName, id, AdminSPIFFEID)
	}
	return nil
}
