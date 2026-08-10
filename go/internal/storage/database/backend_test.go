package database

import (
	"context"
	"testing"

	infradatabase "github.com/nyroway/nyro/go/internal/platform/database"
	dbsqlite "github.com/nyroway/nyro/go/internal/platform/database/sqlite"
)

func TestNewUsesCallerOwnedConnection(t *testing.T) {
	pool, err := dbsqlite.Open(context.Background(), dbsqlite.Options{Path: ":memory:"})
	if err != nil {
		t.Fatalf("open sqlite pool: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })

	b, err := New(infradatabase.KindSQLite, pool)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	gotPool, err := b.DB().DB()
	if err != nil {
		t.Fatalf("GORM DB(): %v", err)
	}
	if gotPool != pool {
		t.Fatal("Backend does not use the caller-owned connection pool")
	}
}

func TestNewRejectsInvalidConnection(t *testing.T) {
	t.Parallel()

	if _, err := New(infradatabase.KindSQLite, nil); err == nil {
		t.Fatal("New(sqlite, nil) error = nil")
	}
	if _, err := New(infradatabase.Kind("unknown"), nil); err == nil {
		t.Fatal("New(unknown, nil) error = nil")
	}
}

func newSQLiteBackend(t *testing.T) *Backend {
	t.Helper()
	pool, err := dbsqlite.Open(context.Background(), dbsqlite.Options{Path: ":memory:"})
	if err != nil {
		t.Fatalf("open SQLite pool: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	b, err := New(infradatabase.KindSQLite, pool)
	if err != nil {
		t.Fatalf("New(): %v", err)
	}
	return b
}

func TestSQLiteBackendMigratesNewConfigSchema(t *testing.T) {
	b := newSQLiteBackend(t)
	if err := b.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	for _, table := range []string{"upstreams", "routes", "route_upstreams", "consumers", "consumer_keys", "consumer_routes", "consumer_quotas", "settings"} {
		if !b.DB().Migrator().HasTable(table) {
			t.Fatalf("missing table %s", table)
		}
	}

	h, err := b.Health()
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	if !h.CanConnect || !h.SchemaCompatible || !h.Writable || h.Backend != "sqlite" {
		t.Fatalf("unexpected health: %+v", h)
	}
}

func TestCheckSchema(t *testing.T) {
	b := newSQLiteBackend(t)

	// Fresh database: canonical tables missing → CheckSchema fails.
	if err := b.CheckSchema(); err == nil {
		t.Fatal("fresh database: want error from CheckSchema, got nil")
	}

	// After Migrate: every canonical table exists → CheckSchema passes.
	if err := b.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := b.CheckSchema(); err != nil {
		t.Fatalf("after migrate: CheckSchema: %v", err)
	}
}
