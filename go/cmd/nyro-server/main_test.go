package main

import (
	"testing"

	"github.com/nyroway/nyro/go/internal/auth"
)

// TestRegisterDrivers asserts every outbound OAuth driver is registered under
// its canonical key — including Vertex, whose SA-JSON flow must resolve at
// runtime (cutover blocker B3).
func TestRegisterDrivers(t *testing.T) {
	reg := auth.NewRegistry()
	registerDrivers(reg)

	for _, key := range []string{"claude-code", "codex", "vertexai"} {
		if _, ok := reg.Get(key); !ok {
			t.Errorf("registerDrivers: driver %q not registered", key)
		}
	}
}

// TestNewStorage covers backend selection. SQLite must open, migrate, and
// serve — wiring the persistent backend the cutover requires (blocker B1).
func TestNewStorage(t *testing.T) {
	t.Run("memory default", func(t *testing.T) {
		st, err := newStorage("", "")
		if err != nil {
			t.Fatalf("newStorage(memory): %v", err)
		}
		if st == nil {
			t.Fatal("newStorage(memory): nil storage")
		}
		h, _ := st.Bootstrap().Health()
		if h.Backend != "memory" {
			t.Errorf("backend = %q, want memory", h.Backend)
		}
	})

	t.Run("sqlite in-memory migrates and serves", func(t *testing.T) {
		st, err := newStorage("sqlite", ":memory:")
		if err != nil {
			t.Fatalf("newStorage(sqlite): %v", err)
		}
		h, _ := st.Bootstrap().Health()
		if h.Backend != "sqlite" {
			t.Errorf("backend = %q, want sqlite", h.Backend)
		}
		// Migrated schema must allow a real query.
		if _, err := st.Providers().List(); err != nil {
			t.Errorf("Providers().List after migrate: %v", err)
		}
	})

	t.Run("unknown backend errors", func(t *testing.T) {
		if _, err := newStorage("bogus", ""); err == nil {
			t.Error("newStorage(bogus): expected error, got nil")
		}
	})
}
