package admin

import (
	"strings"
	"testing"
	"time"

	"github.com/nyroway/nyro/go/internal/configsync/pki"
)

func TestResolveConfigSyncServerTLS_NoFlagsRefuses(t *testing.T) {
	_, err := resolveConfigSyncServerTLS("", "", "", false)
	if err == nil {
		t.Fatal("expected an error when no certs and no --config-insecure are given")
	}
}

func TestResolveConfigSyncServerTLS_InsecureAllowsPlaintext(t *testing.T) {
	cfg, err := resolveConfigSyncServerTLS("", "", "", true)
	if err != nil {
		t.Fatalf("resolveConfigSyncServerTLS: %v", err)
	}
	if cfg != nil {
		t.Fatal("expected a nil *tls.Config for the plaintext Tier 0 path")
	}
}

func TestResolveConfigSyncServerTLS_PartialFlagsRejected(t *testing.T) {
	if _, err := resolveConfigSyncServerTLS("ca.pem", "", "", false); err == nil {
		t.Fatal("expected an error when only --config-tls-ca is set")
	}
	if _, err := resolveConfigSyncServerTLS("ca.pem", "cert.pem", "", true); err == nil {
		t.Fatal("expected an error for a partial tls flag set even with --config-insecure also set")
	}
}

func TestResolveConfigSyncServerTLS_AllThreeLoadsMTLS(t *testing.T) {
	dir := t.TempDir()
	ca, err := pki.EnsureCA(dir, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	certPath, keyPath, err := ca.SignServer(dir, "admin", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	caPath := dir + "/ca.pem"

	cfg, err := resolveConfigSyncServerTLS(caPath, certPath, keyPath, false)
	if err != nil {
		t.Fatalf("resolveConfigSyncServerTLS: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected a non-nil *tls.Config when all three cert paths are given")
	}
}

// TestRunE_RefusesConfigSyncWithoutCertsOrInsecure proves the CLI gate fires
// before any storage/listener work — this is the default `nyro admin`
// invocation (config-listen defaults to 127.0.0.1:19532) with no
// --config-tls-* and no --config-insecure, which must now fail fast rather
// than silently starting config-sync in plaintext.
func TestRunE_RefusesConfigSyncWithoutCertsOrInsecure(t *testing.T) {
	cmd := NewCmd()
	if err := cmd.ParseFlags(nil); err != nil {
		t.Fatalf("parse flags: %v", err)
	}
	err := cmd.RunE(cmd, nil)
	if err == nil {
		t.Fatal("expected an error; config-sync defaults to enabled with no certs and no --config-insecure")
	}
	if !strings.Contains(err.Error(), "config-insecure") {
		t.Errorf("error = %q; want it to mention --config-insecure", err.Error())
	}
}
