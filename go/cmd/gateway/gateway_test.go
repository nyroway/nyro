package gateway

import (
	"testing"
)

func TestNewCmdFlags(t *testing.T) {
	cmd := NewCmd()
	if addr, _ := cmd.Flags().GetString("addr"); addr != "127.0.0.1:19530" {
		t.Errorf("default addr = %q, want 127.0.0.1:19530", addr)
	}
	if cfg, _ := cmd.Flags().GetString("config"); cfg != "" {
		t.Errorf("default config = %q, want empty", cfg)
	}
	if cmd.Use != "gateway" {
		t.Errorf("Use = %q, want gateway", cmd.Use)
	}
}
