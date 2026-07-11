package pki

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

// spiffeTrustDomain is the fixed SPIFFE trust domain for every nyro
// deployment's config-sync PKI. Each --dir is its own independent CA/trust
// root, so a single fixed domain string is sufficient to namespace node
// identities within it; it does not need to vary per deployment.
const spiffeTrustDomain = "nyro"

// SignServer issues admin's server leaf certificate (ExtKeyUsage:
// ServerAuth), carrying the fixed identity AdminSPIFFEID as a SPIFFE URI SAN
// (spiffe://nyro/admin) — the same identity-based verification model as
// SignClient, so gateway's config-sync client checks "is this cert admin?"
// rather than "does this cert's SAN match the hostname I dialed?". That
// means admin's certificate stays valid no matter what address/hostname a
// gateway uses to reach it (direct, load balancer, k8s Service name, IP —
// all of it), with no per-deployment DNS/IP SAN list to maintain and no
// --config-server-name-style override needed on the client side.
//
// The resulting cert/key are written to <dir>/<name>.pem and
// <dir>/<name>-key.pem (0600) and their paths are returned. name is only the
// output file basename (see --out) — it does not affect the identity baked
// into the certificate, which is always AdminSPIFFEID.
func (ca *CA) SignServer(dir, name string, ttl time.Duration) (certPath, keyPath string, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", fmt.Errorf("pki: generate server key: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return "", "", err
	}

	spiffeURI := &url.URL{Scheme: "spiffe", Host: spiffeTrustDomain, Path: "/" + AdminSPIFFEID}

	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: AdminSPIFFEID},
		NotBefore:    now.Add(-5 * time.Minute),
		NotAfter:     now.Add(ttl),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		URIs:         []*url.URL{spiffeURI},
	}

	return ca.signAndWrite(dir, name, tmpl, key)
}

// SignClient issues a client leaf certificate (ExtKeyUsage: ClientAuth) for
// a gateway node, carrying its identity as a SPIFFE URI SAN
// (spiffe://nyro/gateway/<nodeID>). The resulting cert/key are written to
// <dir>/<name>.pem and <dir>/<name>-key.pem (0600) and their paths are
// returned.
func (ca *CA) SignClient(dir, name, nodeID string, ttl time.Duration) (certPath, keyPath string, err error) {
	if nodeID == "" {
		return "", "", fmt.Errorf("pki: SignClient requires a non-empty node-id")
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", fmt.Errorf("pki: generate client key: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return "", "", err
	}

	spiffeURI := &url.URL{Scheme: "spiffe", Host: spiffeTrustDomain, Path: "/gateway/" + nodeID}

	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: nodeID},
		NotBefore:    now.Add(-5 * time.Minute),
		NotAfter:     now.Add(ttl),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		URIs:         []*url.URL{spiffeURI},
	}

	return ca.signAndWrite(dir, name, tmpl, key)
}

// signAndWrite finalizes tmpl (signed by ca), writes the PEM-encoded
// certificate and private key to <dir>/<name>.pem / <dir>/<name>-key.pem
// (0600, dir created 0700 if missing), and returns their paths.
func (ca *CA) signAndWrite(dir, name string, tmpl *x509.Certificate, key *ecdsa.PrivateKey) (certPath, keyPath string, err error) {
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return "", "", fmt.Errorf("pki: create dir %s: %w", dir, err)
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.Cert, &key.PublicKey, ca.Key)
	if err != nil {
		return "", "", fmt.Errorf("pki: sign leaf certificate: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return "", "", fmt.Errorf("pki: parse signed leaf certificate: %w", err)
	}
	certPEM := encodeCertPEM(cert)
	keyPEM, err := encodeECKeyPEM(key)
	if err != nil {
		return "", "", err
	}

	certPath = filepath.Join(dir, name+".pem")
	keyPath = filepath.Join(dir, name+"-key.pem")
	if err := os.WriteFile(certPath, certPEM, filePerm); err != nil {
		return "", "", fmt.Errorf("pki: write %s: %w", certPath, err)
	}
	if err := os.WriteFile(keyPath, keyPEM, filePerm); err != nil {
		return "", "", fmt.Errorf("pki: write %s: %w", keyPath, err)
	}
	return certPath, keyPath, nil
}
