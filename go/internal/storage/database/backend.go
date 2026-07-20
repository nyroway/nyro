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

// CheckSchema is the read-only alternative to Migrate: it performs no DDL, and
// is used at startup when --auto-migrate is not set (see
// internal/bootstrap.bootstrapSQL). It confirms every canonical table
// (model.All()) exists — a lightweight existence check for all backends. It
// does not verify column-level drift; keeping the schema in sync with the
// models is the operator's job (run with --auto-migrate, or apply the DDL from
// `nyro migrate dump`/`diff`).
func (b *Backend) CheckSchema() error {
	for _, m := range model.All() {
		if !b.db.Migrator().HasTable(m) {
			return fmt.Errorf("%s database has no schema yet (missing table for %T) — initialize it with --auto-migrate, or apply the DDL from `nyro migrate dump`/`diff`", b.backend, m)
		}
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
