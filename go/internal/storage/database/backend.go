// Package database implements the shared SQL storage backend for SQLite,
// MySQL, and Postgres.
package database

import (
	"fmt"

	sqlite "github.com/glebarez/sqlite"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"github.com/nyroway/nyro/go/internal/storage"
	"github.com/nyroway/nyro/go/internal/storage/model"
	"github.com/nyroway/nyro/go/internal/storage/query"
)

// Backend is the shared SQL backend.
type Backend struct {
	backend string
	db      *gorm.DB
	q       *query.Query
}

// NewSQLite opens a SQLite database and returns a shared SQL backend.
func NewSQLite(path string) (*Backend, error) {
	if path == "" {
		path = ":memory:"
	}
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		return nil, err
	}
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(5)
	db.Exec("PRAGMA journal_mode=WAL")
	db.Exec("PRAGMA busy_timeout=5000")
	return &Backend{backend: "sqlite", db: db, q: query.Use(db)}, nil
}

// NewMySQL opens a MySQL database and returns a shared SQL backend.
func NewMySQL(dsn string) (*Backend, error) {
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	if err != nil {
		return nil, err
	}
	return &Backend{backend: "mysql", db: db, q: query.Use(db)}, nil
}

// NewPostgres opens a Postgres database and returns a shared SQL backend.
func NewPostgres(dsn string) (*Backend, error) {
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		return nil, err
	}
	return &Backend{backend: "postgres", db: db, q: query.Use(db)}, nil
}

// DB exposes the underlying GORM database for tests and advanced callers.
func (b *Backend) DB() *gorm.DB { return b.db }

// Init performs backend initialization.
func (b *Backend) Init() error { return nil }

// Migrate initializes the new Go config schema.
func (b *Backend) Migrate() error {
	return b.db.AutoMigrate(model.All()...)
}

// CheckSchemaVersion is the read-only alternative to Migrate: it performs no
// DDL, and is used at startup when --auto-migrate is not set (see
// internal/bootstrap.bootstrapSQL).
//
// sqlite has no versioned migrations (go/migrations/ only covers
// mysql/postgres), so for sqlite this just confirms the canonical tables
// exist; expected is ignored and may be empty.
//
// For mysql/postgres it compares expected (the latest version embedded in
// go/migrations/<dialect>/, see the migrations package) against the version
// recorded in atlas_schema_revisions — the bookkeeping table `atlas migrate
// apply` creates when a DBA applies the migration files (see go/atlas.hcl;
// applying the same SQL via a raw mysql/psql client instead skips this
// bookkeeping, so this check would still report it as unmigrated). A
// missing table, no applied row, or a version mismatch all fail with a
// message pointing at the pending migration files.
func (b *Backend) CheckSchemaVersion(expected string) error {
	if b.backend == "sqlite" {
		for _, m := range model.All() {
			if !b.db.Migrator().HasTable(m) {
				return fmt.Errorf("sqlite database has no schema yet (missing table for %T) — pass --auto-migrate to initialize it", m)
			}
		}
		return nil
	}
	// atlas's revisions bookkeeping table lives at "atlas_schema_revisions"
	// in mysql (same database as the data), but postgres puts it in its own
	// dedicated schema of the same name — must be schema-qualified there or
	// it resolves against search_path (typically "public") and 404s.
	revisionsTable := "atlas_schema_revisions"
	if b.backend == "postgres" {
		revisionsTable = "atlas_schema_revisions.atlas_schema_revisions"
	}
	var version string
	if err := b.db.Raw(fmt.Sprintf("SELECT version FROM %s ORDER BY version DESC LIMIT 1", revisionsTable)).Row().Scan(&version); err != nil {
		return fmt.Errorf("no migrations applied yet on this %s database (expected version %s): ask your DBA to run `atlas migrate apply` on go/migrations/%s/, or pass --auto-migrate if this account has DDL rights (%w)", b.backend, expected, b.backend, err)
	}
	if version != expected {
		return fmt.Errorf("%s schema version mismatch: database is at %s, this binary expects %s — ask your DBA to run `atlas migrate apply` for the pending files under go/migrations/%s/", b.backend, version, expected, b.backend)
	}
	return nil
}

// Health reports SQL backend health.
func (b *Backend) Health() (storage.StorageHealth, error) {
	h := storage.StorageHealth{Backend: b.backend}
	sqlDB, err := b.db.DB()
	if err != nil {
		return h, err
	}
	if err := sqlDB.Ping(); err != nil {
		return h, nil
	}
	h.CanConnect = true
	h.SchemaCompatible = true
	h.Writable = true
	return h, nil
}

// Upstreams returns the config-schema upstream store.
func (b *Backend) Upstreams() storage.UpstreamStore { return upstreamStore{q: b.q} }

// Routes returns the config-schema route store.
func (b *Backend) Routes() storage.RouteStore { return routeStore{q: b.q} }

// Consumers returns the config-schema consumer store.
func (b *Backend) Consumers() storage.ConsumerStore { return consumerStore{q: b.q} }

// Auth returns the config-schema inbound key-auth read path.
func (b *Backend) Auth() storage.KeyAuthStore { return keyAuthStore{q: b.q} }

// Settings returns the config-schema settings store (key column).
func (b *Backend) Settings() storage.SettingsStore { return coreSettingsStore{q: b.q} }

// Migrator returns the backend itself (it already implements Init/Migrate/Health).
func (b *Backend) Migrator() storage.Migrator { return b }

var _ storage.Storage = (*Backend)(nil)
