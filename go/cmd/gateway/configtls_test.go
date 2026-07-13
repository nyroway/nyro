package gateway

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/nyroway/nyro/go/internal/configsync/pki"
)

func TestResolveConfigSyncClientTLS_NoFlagsUsesPlaintextAndWarns(t *testing.T) {
	var logs bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })

	cfg, err := resolveConfigSyncClientTLS("", "", "")
	if err != nil {
		t.Fatalf("resolveConfigSyncClientTLS: %v", err)
	}
	if cfg != nil {
		t.Fatal("expected a nil *tls.Config for plaintext config-sync")
	}
	if got := logs.String(); !strings.Contains(got, "level=WARN") || !strings.Contains(got, "plaintext") {
		t.Fatalf("log = %q; want a plaintext security warning", got)
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

	cfg, err := resolveConfigSyncClientTLS(caPath, certPath, keyPath)
	if err != nil {
		t.Fatalf("resolveConfigSyncClientTLS: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected a non-nil *tls.Config when all three cert paths are given")
	}
}

func TestResolveConfigSyncClientTLS_PartialFlagsRejected(t *testing.T) {
	if _, err := resolveConfigSyncClientTLS("ca.pem", "", ""); err == nil {
		t.Fatal("expected an error when only --config-tls-ca is set")
	}
	if _, err := resolveConfigSyncClientTLS("ca.pem", "cert.pem", ""); err == nil {
		t.Fatal("expected an error when --config-tls-key is missing")
	}
}

func TestResolveConfigSyncClientTLS_LoadFailure(t *testing.T) {
	dir := t.TempDir()
	_, err := resolveConfigSyncClientTLS(
		dir+"/missing-ca.pem",
		dir+"/missing-cert.pem",
		dir+"/missing-key.pem",
	)
	if err == nil {
		t.Fatal("expected an error when the TLS files cannot be loaded")
	}
}

func TestConfigSyncInsecureFlagRemoved(t *testing.T) {
	if flag := NewCmd().Flags().Lookup("config-insecure"); flag != nil {
		t.Fatal("--config-insecure should no longer be registered")
	}
}
