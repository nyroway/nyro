package pki

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

const (
	caCertFile = "ca.pem"
	caKeyFile  = "ca-key.pem"

	dirPerm  = 0o700
	filePerm = 0o600
)

// CA is a loaded (or freshly generated) certificate authority: its
// certificate (used to build trust chains / ClientCAs / RootCAs pools) and
// private key (used to sign leaf certificates). The private key is only ever
// needed by the offline `nyro ca sign-*` commands, never by admin/gateway at
// runtime.
type CA struct {
	Cert *x509.Certificate
	Key  *ecdsa.PrivateKey

	// certPEM is the PEM encoding of Cert, cached so signers don't need to
	// re-marshal it for every leaf certificate they issue.
	certPEM []byte
}

// EnsureCA loads the CA at dir if both ca.pem and ca-key.pem already exist,
// or generates a fresh self-signed CA (valid for ttl) and writes it there
// otherwise. dir is created (0700) if missing; files are written 0600.
func EnsureCA(dir string, ttl time.Duration) (*CA, error) {
	certPath := filepath.Join(dir, caCertFile)
	keyPath := filepath.Join(dir, caKeyFile)

	_, certErr := os.Stat(certPath)
	_, keyErr := os.Stat(keyPath)
	switch {
	case certErr == nil && keyErr == nil:
		return LoadCA(dir)
	case certErr != nil && keyErr != nil:
		return generateCA(dir, ttl)
	default:
		// One file present without the other is an inconsistent/corrupt
		// state we should never silently paper over.
		return nil, fmt.Errorf("pki: incomplete CA at %s (one of ca.pem/ca-key.pem missing)", dir)
	}
}

// LoadCA loads an existing CA from dir. Returns an error if either file is
// missing or invalid.
func LoadCA(dir string) (*CA, error) {
	certPath := filepath.Join(dir, caCertFile)
	keyPath := filepath.Join(dir, caKeyFile)

	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, fmt.Errorf("pki: read CA cert: %w", err)
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("pki: read CA key: %w", err)
	}

	cert, err := parseCertPEM(certPEM)
	if err != nil {
		return nil, fmt.Errorf("pki: parse CA cert: %w", err)
	}
	key, err := parseECKeyPEM(keyPEM)
	if err != nil {
		return nil, fmt.Errorf("pki: parse CA key: %w", err)
	}
	if !cert.IsCA {
		return nil, errors.New("pki: ca.pem is not a CA certificate")
	}
	return &CA{Cert: cert, Key: key, certPEM: certPEM}, nil
}

// generateCA creates a fresh self-signed CA, writes it to dir, and returns it.
func generateCA(dir string, ttl time.Duration) (*CA, error) {
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return nil, fmt.Errorf("pki: create dir %s: %w", dir, err)
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("pki: generate CA key: %w", err)
	}

	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}

	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "nyro config-sync CA"},
		NotBefore:             now.Add(-5 * time.Minute), // small backdate to tolerate clock skew
		NotAfter:              now.Add(ttl),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("pki: create CA certificate: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("pki: parse generated CA certificate: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM, err := encodeECKeyPEM(key)
	if err != nil {
		return nil, err
	}

	if err := os.WriteFile(filepath.Join(dir, caCertFile), certPEM, filePerm); err != nil {
		return nil, fmt.Errorf("pki: write ca.pem: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, caKeyFile), keyPEM, filePerm); err != nil {
		return nil, fmt.Errorf("pki: write ca-key.pem: %w", err)
	}

	return &CA{Cert: cert, Key: key, certPEM: certPEM}, nil
}

func randomSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return nil, fmt.Errorf("pki: generate serial number: %w", err)
	}
	return serial, nil
}
