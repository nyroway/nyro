package bootstrap

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
)

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
		{"mysql scheme rejected", "mysql://user:pass@host/db", "", "", true},
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

func TestRunManagedServersShutsDownInReverseDependencyOrder(t *testing.T) {
	var mu sync.Mutex
	var events []string
	appendEvent := func(event string) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, event)
	}
	firstDone := make(chan struct{})
	secondDone := make(chan struct{})
	wantErr := errors.New("listener failed")

	err := RunManagedServers(
		ManagedServer{
			Role: "observe",
			Serve: func() error {
				<-firstDone
				return nil
			},
			Shutdown: func(context.Context) error {
				appendEvent("shutdown-observe")
				close(firstDone)
				return nil
			},
			AfterShutdown: func() { appendEvent("after-observe") },
		},
		ManagedServer{
			Role: "data plane",
			Serve: func() error {
				close(secondDone)
				return wantErr
			},
			Shutdown: func(context.Context) error {
				appendEvent("shutdown-data")
				return nil
			},
			AfterShutdown: func() { appendEvent("after-data") },
		},
	)
	<-secondDone
	if !errors.Is(err, wantErr) {
		t.Fatalf("RunManagedServers() error = %v, want %v", err, wantErr)
	}
	if want := []string{"shutdown-data", "after-data", "shutdown-observe", "after-observe"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
}
