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

func TestNewCmdStorageFlagDefaults(t *testing.T) {
	cmd := NewCmd()
	if v, _ := cmd.Flags().GetString("storage"); v != "sqlite" {
		t.Errorf("default storage = %q, want sqlite", v)
	}
	if v, _ := cmd.Flags().GetString("db-dsn"); v != "" {
		t.Errorf("default db-dsn = %q, want empty (resolved at RunE time)", v)
	}
}

func TestRunE_RejectsMemoryStorage(t *testing.T) {
	cmd := NewCmd()
	if err := cmd.ParseFlags([]string{"--storage", "memory"}); err != nil {
		t.Fatalf("parse flags: %v", err)
	}
	err := cmd.RunE(cmd, nil)
	if err == nil {
		t.Fatal("expected an error rejecting --storage memory, got nil")
	}
}

func TestRunE_RejectsUnknownStorage(t *testing.T) {
	cmd := NewCmd()
	if err := cmd.ParseFlags([]string{"--storage", "bogus"}); err != nil {
		t.Fatalf("parse flags: %v", err)
	}
	err := cmd.RunE(cmd, nil)
	if err == nil {
		t.Fatal("expected an error rejecting --storage bogus, got nil")
	}
}
