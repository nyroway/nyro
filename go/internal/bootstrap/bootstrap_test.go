package bootstrap

import "testing"

func TestParseDSN(t *testing.T) {
	tests := []struct {
		name        string
		dsn         string
		wantBackend string
		wantDriver  string
		wantErr     bool
	}{
		{"sqlite absolute path", "sqlite:///abs/x.db", "sqlite", "/abs/x.db", false},
		{"sqlite relative path", "sqlite://./x.db", "sqlite", "./x.db", false},
		{"sqlite memory", "sqlite://:memory:", "sqlite", ":memory:", false},
		{"postgres passthrough", "postgres://user:pass@host:5432/db?sslmode=disable", "postgres", "postgres://user:pass@host:5432/db?sslmode=disable", false},
		{"postgresql alias rejected", "postgresql://user:pass@host:5432/db", "", "", true},
		{"mysql url to gorm dsn", "mysql://user:pass@host:3307/db?parseTime=true", "mysql", "user:pass@tcp(host:3307)/db?parseTime=true", false},
		{"mysql url default port", "mysql://user:pass@host/db", "mysql", "user:pass@tcp(host:3306)/db", false},
		{"memory scheme rejected", "memory://", "", "", true},
		{"bad scheme", "redis://host:6379", "", "", true},
		{"empty dsn rejected", "", "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend, driver, err := ParseDSN(tt.dsn)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseDSN(%q): expected error, got backend=%q driver=%q", tt.dsn, backend, driver)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseDSN(%q): unexpected error: %v", tt.dsn, err)
			}
			if backend != tt.wantBackend {
				t.Errorf("ParseDSN(%q) backend = %q, want %q", tt.dsn, backend, tt.wantBackend)
			}
			if driver != tt.wantDriver {
				t.Errorf("ParseDSN(%q) driver = %q, want %q", tt.dsn, driver, tt.wantDriver)
			}
		})
	}
}

func TestOpenStorageFromDSN(t *testing.T) {
	t.Run("sqlite in-memory with auto-migrate migrates and serves", func(t *testing.T) {
		st, err := OpenStorageFromDSN("sqlite://:memory:", true, false)
		if err != nil {
			t.Fatalf("sqlite: %v", err)
		}
		h, _ := st.Migrator().Health()
		if h.Backend != "sqlite" {
			t.Errorf("backend = %q, want sqlite", h.Backend)
		}
		if _, err := st.Upstreams().List(); err != nil {
			t.Errorf("Upstreams().List after migrate: %v", err)
		}
	})
	t.Run("sqlite in-memory without auto-migrate fails schema check", func(t *testing.T) {
		if _, err := OpenStorageFromDSN("sqlite://:memory:", false, false); err == nil {
			t.Error("expected schema-check error on an unmigrated in-memory db")
		}
	})
	t.Run("bad scheme errors", func(t *testing.T) {
		if _, err := OpenStorageFromDSN("bogus://x", false, false); err == nil {
			t.Error("expected error for bogus scheme")
		}
	})
}
