package gateway

import (
	"testing"
	"time"

	"github.com/nyroway/nyro/go/internal/configsync/pki"
)

func TestResolveConfigSyncClientTLS_NoFlagsRefuses(t *testing.T) {
	_, err := resolveConfigSyncClientTLS("", "", "", false)
	if err == nil {
		t.Fatal("expected an error when no certs and no --config-insecure are given")
	}
}

func TestResolveConfigSyncClientTLS_InsecureAllowsPlaintext(t *testing.T) {
	cfg, err := resolveConfigSyncClientTLS("", "", "", true)
	if err != nil {
		t.Fatalf("resolveConfigSyncClientTLS: %v", err)
	}
	if cfg != nil {
		t.Fatal("expected a nil *tls.Config for the plaintext Tier 0 path")
	}
}

func TestResolveConfigSyncClientTLS_PartialFlagsRejected(t *testing.T) {
	if _, err := resolveConfigSyncClientTLS("ca.pem", "cert.pem", "", true); err == nil {
		t.Fatal("expected an error for a partial tls flag set even with --config-insecure also set")
	}
}

func TestResolveConfigSyncClientTLS_AllThreeLoadsMTLS(t *testing.T) {
	dir := t.TempDir()
	ca, err := pki.EnsureCA(dir, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	certPath, keyPath, err := ca.SignClient(dir, "gateway", "node-1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	caPath := dir + "/ca.pem"

	cfg, err := resolveConfigSyncClientTLS(caPath, certPath, keyPath, false)
	if err != nil {
		t.Fatalf("resolveConfigSyncClientTLS: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected a non-nil *tls.Config when all three cert paths are given")
	}
}

// TestRunE_RefusesConfigServerWithoutCertsOrInsecure proves the gateway CLI
// gate fires as soon as --config-server is given without --config-tls-* or
// --config-insecure, before touching the network.
func TestRunE_RefusesConfigServerWithoutCertsOrInsecure(t *testing.T) {
	cmd := NewCmd()
	if err := cmd.ParseFlags([]string{"--config-server", "127.0.0.1:19532"}); err != nil {
		t.Fatalf("parse flags: %v", err)
	}
	err := cmd.RunE(cmd, nil)
	if err == nil {
		t.Fatal("expected an error when --config-server is set without certs or --config-insecure")
	}
}
