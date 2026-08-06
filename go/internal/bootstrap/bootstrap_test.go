package bootstrap

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	infradatabase "github.com/nyroway/nyro/go/infra/database"
	dbsqlite "github.com/nyroway/nyro/go/infra/database/sqlite"
)

func TestOpenStorageFromDSN(t *testing.T) {
	t.Run("sqlite in-memory with auto-migrate migrates and serves", func(t *testing.T) {
		st, err := OpenStorageFromDSN(context.Background(), "sqlite://:memory:", true, false)
		if err != nil {
			t.Fatalf("sqlite: %v", err)
		}
		t.Cleanup(func() { _ = st.Close() })
		if got := st.connection.DB.Stats().MaxOpenConnections; got != 5 {
			t.Fatalf("config SQLite MaxOpenConnections = %d, want 5", got)
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
		if _, err := OpenStorageFromDSN(context.Background(), "sqlite://:memory:", false, false); err == nil {
			t.Error("expected schema-check error on an unmigrated in-memory db")
		}
	})
	t.Run("bad scheme errors", func(t *testing.T) {
		if _, err := OpenStorageFromDSN(context.Background(), "bogus://x", false, false); err == nil {
			t.Error("expected error for bogus scheme")
		}
	})
}

func TestOpenStorageFromDSNClosesConnectionWhenBootstrapFails(t *testing.T) {
	pool, err := dbsqlite.Open(context.Background(), dbsqlite.Options{Path: ":memory:"})
	if err != nil {
		t.Fatalf("open SQLite pool: %v", err)
	}
	opener := func(context.Context, string, infradatabase.Options) (*infradatabase.Connection, error) {
		return &infradatabase.Connection{Kind: infradatabase.KindSQLite, DB: pool}, nil
	}

	if _, err := openStorageFromDSN(context.Background(), "sqlite://:memory:", false, false, opener); err == nil {
		t.Fatal("openStorageFromDSN() error = nil")
	}
	if err := pool.Ping(); err == nil {
		t.Fatal("connection remains open after schema bootstrap failure")
	}
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
