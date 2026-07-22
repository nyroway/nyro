package proxy

import (
	"strings"
	"testing"
)

func TestNewCmdFlags(t *testing.T) {
	cmd := NewCmd()
	if addr, _ := cmd.Flags().GetString("listen"); addr != "0.0.0.0:19530" {
		t.Errorf("default listen = %q; want 0.0.0.0:19530", addr)
	}
	if cfg, _ := cmd.Flags().GetString("config"); cfg != "" {
		t.Errorf("default config = %q; want empty", cfg)
	}
	if cs, _ := cmd.Flags().GetString("server"); cs != "" {
		t.Errorf("default server = %q; want empty", cs)
	}
	if cmd.Use != "proxy" {
		t.Errorf("Use = %q; want proxy", cmd.Use)
	}
	// Pre-rename flag names must be gone (no compatibility period).
	for _, old := range []string{"config-file", "config-server", "config-tls-ca", "config-tls-cert", "config-tls-key"} {
		if cmd.Flags().Lookup(old) != nil {
			t.Errorf("--%s should have been renamed", old)
		}
	}
}

func TestRunE_RequiresExactlyOneConfigSource(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "neither source", args: nil},
		{name: "both sources", args: []string{"--config", "a.yaml", "--server", "host:1234"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := NewCmd()
			// ParseFlags binds the args to the flag set first. Calling RunE directly
			// skips cobra's parse step, so flags passed above would remain at their
			// defaults and the intended XOR branch would not be exercised.
			if err := cmd.ParseFlags(tt.args); err != nil {
				t.Fatalf("parse flags: %v", err)
			}
			err := cmd.RunE(cmd, nil)
			if err == nil || !strings.Contains(err.Error(), "exactly one") {
				t.Fatalf("expected exactly-one config source error; got %v", err)
			}
		})
	}
}

func TestRunE_RejectsConfigTLSFlagsWithConfigFile(t *testing.T) {
	tests := []struct {
		name string
		flag string
	}{
		{name: "TLS CA", flag: "--sync-tls-ca=ca.pem"},
		{name: "TLS certificate", flag: "--sync-tls-cert=cert.pem"},
		{name: "TLS key", flag: "--sync-tls-key=key.pem"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := NewCmd()
			if err := cmd.ParseFlags([]string{
				"--config=missing.yaml",
				tt.flag,
			}); err != nil {
				t.Fatalf("parse flags: %v", err)
			}

			err := cmd.RunE(cmd, nil)
			if err == nil {
				t.Fatal("RunE returned nil error, want config-file/TLS validation error")
			}
			if !strings.Contains(err.Error(), "--server") {
				t.Fatalf("RunE error = %q, want TLS flags require --server", err)
			}
		})
	}
}
