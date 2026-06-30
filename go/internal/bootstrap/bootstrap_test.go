package bootstrap

import (
	"testing"

	"github.com/nyroway/nyro/go/internal/auth"
)

func TestRegisterDrivers(t *testing.T) {
	reg := auth.NewRegistry()
	RegisterDrivers(reg)
	for _, key := range []string{"claude-code", "codex", "vertexai"} {
		if _, ok := reg.Get(key); !ok {
			t.Errorf("RegisterDrivers: driver %q not registered", key)
		}
	}
}

func TestOpenStorage(t *testing.T) {
	t.Run("memory default", func(t *testing.T) {
		st, err := OpenStorage("", "")
		if err != nil {
			t.Fatalf("memory: %v", err)
		}
		h, _ := st.Bootstrap().Health()
		if h.Backend != "memory" {
			t.Errorf("backend = %q, want memory", h.Backend)
		}
	})
	t.Run("sqlite in-memory migrates and serves", func(t *testing.T) {
		st, err := OpenStorage("sqlite", ":memory:")
		if err != nil {
			t.Fatalf("sqlite: %v", err)
		}
		h, _ := st.Bootstrap().Health()
		if h.Backend != "sqlite" {
			t.Errorf("backend = %q, want sqlite", h.Backend)
		}
		if _, err := st.Providers().List(); err != nil {
			t.Errorf("Providers().List after migrate: %v", err)
		}
	})
	t.Run("unknown backend errors", func(t *testing.T) {
		if _, err := OpenStorage("bogus", ""); err == nil {
			t.Error("expected error for bogus backend")
		}
	})
}
