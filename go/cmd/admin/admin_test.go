package admin

import (
	"testing"
)

func TestNewCmdFlags(t *testing.T) {
	cmd := NewCmd()
	if addr, _ := cmd.Flags().GetString("addr"); addr != "127.0.0.1:19531" {
		t.Errorf("default addr = %q, want 127.0.0.1:19531", addr)
	}
	if cmd.Use != "admin" {
		t.Errorf("Use = %q, want admin", cmd.Use)
	}
}
