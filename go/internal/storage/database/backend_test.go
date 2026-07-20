package database

import "testing"

func TestSQLiteBackendMigratesNewConfigSchema(t *testing.T) {
	b, err := NewSQLite(":memory:")
	if err != nil {
		t.Fatalf("new sqlite: %v", err)
	}
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
	b, err := NewSQLite(":memory:")
	if err != nil {
		t.Fatalf("new sqlite: %v", err)
	}

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
