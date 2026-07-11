package pki

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestEnsureCA_GeneratesThenReloads(t *testing.T) {
	dir := t.TempDir()

	ca1, err := EnsureCA(dir, time.Hour)
	if err != nil {
		t.Fatalf("EnsureCA (generate): %v", err)
	}
	if !ca1.Cert.IsCA {
		t.Fatalf("generated cert is not a CA")
	}
	for _, f := range []string{caCertFile, caKeyFile} {
		info, err := os.Stat(filepath.Join(dir, f))
		if err != nil {
			t.Fatalf("expected %s to exist: %v", f, err)
		}
		if info.Mode().Perm() != filePerm {
			t.Fatalf("%s perm = %v, want %v", f, info.Mode().Perm(), filePerm)
		}
	}

	ca2, err := EnsureCA(dir, time.Hour)
	if err != nil {
		t.Fatalf("EnsureCA (reload): %v", err)
	}
	if ca1.Cert.SerialNumber.Cmp(ca2.Cert.SerialNumber) != 0 {
		t.Fatalf("EnsureCA regenerated instead of reloading existing CA")
	}
}

func TestEnsureCA_IncompletePairErrors(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, caCertFile), []byte("not a real cert"), filePerm); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureCA(dir, time.Hour); err == nil {
		t.Fatal("expected error for incomplete CA pair, got nil")
	}
}

func TestSignServer_AdminIdentity(t *testing.T) {
	dir := t.TempDir()
	ca, err := EnsureCA(dir, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	certPath, keyPath, err := ca.SignServer(dir, "admin", time.Hour)
	if err != nil {
		t.Fatalf("SignServer: %v", err)
	}

	cert, err := loadLeafCert(certPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyAdminIdentity(cert); err != nil {
		t.Errorf("VerifyAdminIdentity: %v", err)
	}
	if got := cert.ExtKeyUsage; len(got) != 1 || got[0] != x509.ExtKeyUsageServerAuth {
		t.Errorf("ExtKeyUsage = %v, want [ServerAuth]", got)
	}

	if _, err := os.Stat(keyPath); err != nil {
		t.Fatalf("key file missing: %v", err)
	}
}

func TestSignClient_SPIFFESAN(t *testing.T) {
	dir := t.TempDir()
	ca, err := EnsureCA(dir, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	certPath, _, err := ca.SignClient(dir, "gateway", "node-abcd", time.Hour)
	if err != nil {
		t.Fatalf("SignClient: %v", err)
	}
	cert, err := loadLeafCert(certPath)
	if err != nil {
		t.Fatal(err)
	}

	id, err := GatewayNodeIDFromCert(cert)
	if err != nil {
		t.Fatalf("GatewayNodeIDFromCert: %v", err)
	}
	if id != "node-abcd" {
		t.Errorf("identity = %q, want %q", id, "node-abcd")
	}
	if got := cert.ExtKeyUsage; len(got) != 1 || got[0] != x509.ExtKeyUsageClientAuth {
		t.Errorf("ExtKeyUsage = %v, want [ClientAuth]", got)
	}
	if err := VerifyAdminIdentity(cert); err == nil {
		t.Fatal("expected a gateway client cert to fail VerifyAdminIdentity")
	}
}

func TestGatewayNodeIDFromCert_RejectsAdminCert(t *testing.T) {
	dir := t.TempDir()
	ca, err := EnsureCA(dir, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	certPath, _, err := ca.SignServer(dir, "admin", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := loadLeafCert(certPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := GatewayNodeIDFromCert(cert); err == nil {
		t.Fatal("expected error extracting a gateway node-id from admin's own certificate")
	}
}

// TestMTLSRoundTrip exercises LoadServerTLS/LoadClientTLS end-to-end over a
// real TCP loopback listener: a server presenting an admin cert requiring
// client certs, and three client dial attempts (matching CA + cert, wrong
// CA, no cert) to confirm accept/reject behavior.
func TestMTLSRoundTrip(t *testing.T) {
	dir := t.TempDir()
	ca, err := EnsureCA(dir, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	serverCertPath, serverKeyPath, err := ca.SignServer(dir, "admin", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	clientCertPath, clientKeyPath, err := ca.SignClient(dir, "gateway", "node-1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	caPath := filepath.Join(dir, caCertFile)

	serverTLS, err := LoadServerTLS(caPath, serverCertPath, serverKeyPath)
	if err != nil {
		t.Fatalf("LoadServerTLS: %v", err)
	}

	lis, err := tls.Listen("tcp", "127.0.0.1:0", serverTLS)
	if err != nil {
		t.Fatalf("tls.Listen: %v", err)
	}
	defer func() { _ = lis.Close() }()

	acceptOnce := func() <-chan error {
		errCh := make(chan error, 1)
		go func() {
			conn, err := lis.Accept()
			if err != nil {
				errCh <- err
				return
			}
			defer func() { _ = conn.Close() }()
			tlsConn, ok := conn.(*tls.Conn)
			if !ok {
				errCh <- nil
				return
			}
			errCh <- tlsConn.Handshake()
		}()
		return errCh
	}

	t.Run("valid client cert succeeds", func(t *testing.T) {
		errCh := acceptOnce()
		clientTLS, err := LoadClientTLS(caPath, clientCertPath, clientKeyPath)
		if err != nil {
			t.Fatal(err)
		}
		conn, err := tls.Dial("tcp", lis.Addr().String(), clientTLS)
		if err != nil {
			t.Fatalf("client dial: %v", err)
		}
		_ = conn.Close()
		if err := <-errCh; err != nil {
			t.Fatalf("server-side handshake: %v", err)
		}
	})

	t.Run("wrong CA client cert rejected", func(t *testing.T) {
		otherDir := t.TempDir()
		otherCA, err := EnsureCA(otherDir, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		otherCertPath, otherKeyPath, err := otherCA.SignClient(otherDir, "gateway", "node-1", time.Hour)
		if err != nil {
			t.Fatal(err)
		}

		errCh := acceptOnce()
		clientTLS, err := LoadClientTLS(caPath, otherCertPath, otherKeyPath)
		if err != nil {
			t.Fatal(err)
		}
		conn, dialErr := tls.Dial("tcp", lis.Addr().String(), clientTLS)
		if dialErr == nil {
			_ = conn.Close()
		}
		serverErr := <-errCh
		if dialErr == nil && serverErr == nil {
			t.Fatal("expected handshake to fail for a client cert signed by a different CA")
		}
	})

	t.Run("no client cert rejected", func(t *testing.T) {
		errCh := acceptOnce()
		// Bypass identity verification here (InsecureSkipVerify) so the
		// handshake actually reaches the point where the server enforces
		// RequireAndVerifyClientCert — this subtest is specifically about
		// the server rejecting a missing client cert, not about client-side
		// verification of admin's identity.
		conn, dialErr := tls.Dial("tcp", lis.Addr().String(), &tls.Config{InsecureSkipVerify: true})
		if dialErr == nil {
			_ = conn.Close()
		}
		serverErr := <-errCh
		if dialErr == nil && serverErr == nil {
			t.Fatal("expected handshake to fail with no client certificate presented")
		}
	})
}

// TestLoadClientTLS_RejectsNonAdminServerIdentity proves LoadClientTLS's
// VerifyConnection checks identity, not just chain validity: a certificate
// signed by the trusted CA but not carrying the AdminSPIFFEID identity (here,
// a gateway's own client cert, reused as a server cert) must be rejected
// even though the chain itself verifies fine.
func TestLoadClientTLS_RejectsNonAdminServerIdentity(t *testing.T) {
	dir := t.TempDir()
	ca, err := EnsureCA(dir, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	// SignClient's cert has no ServerAuth EKU and identifies as
	// "gateway/impersonator", not "admin" — either alone is grounds for
	// LoadClientTLS to reject it.
	notAdminCertPath, notAdminKeyPath, err := ca.SignClient(dir, "not-admin", "impersonator", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	clientCertPath, clientKeyPath, err := ca.SignClient(dir, "gateway", "node-1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	caPath := filepath.Join(dir, caCertFile)

	// tls.Listen doesn't itself enforce the serving cert's EKU, so this is a
	// faithful stand-in for "some entity presents a CA-signed cert that
	// isn't admin's" regardless of which specific property makes it invalid.
	rawServerTLS := &tls.Config{MinVersion: tls.VersionTLS12}
	tlsCert, err := tls.LoadX509KeyPair(notAdminCertPath, notAdminKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	rawServerTLS.Certificates = []tls.Certificate{tlsCert}

	lis, err := tls.Listen("tcp", "127.0.0.1:0", rawServerTLS)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lis.Close() }()
	go func() {
		conn, err := lis.Accept()
		if err == nil {
			_ = conn.Close()
		}
	}()

	clientTLS, err := LoadClientTLS(caPath, clientCertPath, clientKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	conn, dialErr := tls.Dial("tcp", lis.Addr().String(), clientTLS)
	if dialErr == nil {
		_ = conn.Close()
		t.Fatal("expected the client to reject a server certificate that isn't identified as admin")
	}
}

func TestExpiringSoon(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name     string
		notAfter time.Time
		want     bool
	}{
		{"far future", now.Add(365 * 24 * time.Hour), false},
		{"within window", now.Add(10 * 24 * time.Hour), true},
		{"already expired", now.Add(-time.Hour), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ExpiringSoon(tc.notAfter, now); got != tc.want {
				t.Errorf("ExpiringSoon(%v) = %v, want %v", tc.notAfter, got, tc.want)
			}
		})
	}
}

func TestLeafNotAfter(t *testing.T) {
	dir := t.TempDir()
	ca, err := EnsureCA(dir, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	certPath, keyPath, err := ca.SignServer(dir, "admin", 2*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	tlsCert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	notAfter, err := LeafNotAfter(tlsCert)
	if err != nil {
		t.Fatalf("LeafNotAfter: %v", err)
	}
	if time.Until(notAfter) < time.Hour || time.Until(notAfter) > 2*time.Hour+time.Minute {
		t.Errorf("notAfter = %v, want ~2h from now", notAfter)
	}
}

func TestWatchExpiry_WarnsImmediatelyForSoonExpiringCert(t *testing.T) {
	dir := t.TempDir()
	ca, err := EnsureCA(dir, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	// Valid for less than the warning window: should trigger a warning on
	// the very first (synchronous, pre-ticker) check.
	certPath, keyPath, err := ca.SignServer(dir, "admin", 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	tlsCert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &tls.Config{Certificates: []tls.Certificate{tlsCert}}

	var warned atomic.Bool
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	WatchExpiry(ctx, cfg, time.Hour, func(time.Time) { warned.Store(true) })

	if !warned.Load() {
		t.Fatal("expected an immediate warning for a certificate expiring within the window")
	}
}

func TestWatchExpiry_NoWarningForFreshCert(t *testing.T) {
	dir := t.TempDir()
	ca, err := EnsureCA(dir, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	certPath, keyPath, err := ca.SignServer(dir, "admin", 8760*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	tlsCert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &tls.Config{Certificates: []tls.Certificate{tlsCert}}

	var warned atomic.Bool
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	WatchExpiry(ctx, cfg, time.Hour, func(time.Time) { warned.Store(true) })

	if warned.Load() {
		t.Fatal("did not expect a warning for a freshly issued 1y certificate")
	}
}

func TestWatchExpiry_NilConfigIsNoop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Must not panic and must never invoke warn.
	WatchExpiry(ctx, nil, time.Hour, func(time.Time) { t.Fatal("warn should not be called for a nil *tls.Config") })
}

func loadLeafCert(path string) (*x509.Certificate, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parseCertPEM(data)
}
