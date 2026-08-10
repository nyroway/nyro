// Package database implements the shared SQL storage backend for SQLite and
// Postgres.
package database

import (
	"database/sql"
	"fmt"
	"os"

	sqlite "github.com/glebarez/sqlite"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	infradatabase "github.com/nyroway/nyro/go/internal/platform/database"
	"github.com/nyroway/nyro/go/internal/storage"
	"github.com/nyroway/nyro/go/internal/storage/model"
	"github.com/nyroway/nyro/go/internal/storage/query"
)

// Backend is the shared SQL backend.
type Backend struct {
	backend string
	db      *gorm.DB
	q       *query.Query
	// plaintextKeys, when true, stores the recoverable raw key alongside the
	// hash on creation so it can be retrieved later. Set once at startup via
	// SetPlaintextKeys; default false (hash-only).
	plaintextKeys bool
}

// New creates a shared SQL backend over a caller-owned connection pool.
func New(kind infradatabase.Kind, pool *sql.DB) (*Backend, error) {
	if pool == nil {
		return nil, fmt.Errorf("%s database connection is nil", kind)
	}

	var dialector gorm.Dialector
	switch kind {
	case infradatabase.KindSQLite:
		dialector = sqlite.Dialector{Conn: pool}
	case infradatabase.KindPostgres:
		dialector = postgres.New(postgres.Config{Conn: pool})
	default:
		return nil, fmt.Errorf("unsupported database kind %q", kind)
	}

	db, err := gorm.Open(dialector, &gorm.Config{Logger: newGormLogger(os.Stderr)})
	if err != nil {
		return nil, fmt.Errorf("initialize %s GORM backend: %w", kind, err)
	}
	return &Backend{backend: string(kind), db: db, q: query.Use(db)}, nil
}

// SetPlaintextKeys toggles recoverable plaintext key storage. It is set once
// at startup (from the admin's --raw-api-keys flag) before the backend
// serves any request.
func (b *Backend) SetPlaintextKeys(v bool) { b.plaintextKeys = v }

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
// `nyro tool migrate dump`/`diff`).
func (b *Backend) CheckSchema() error {
	for _, m := range model.All() {
		if !b.db.Migrator().HasTable(m) {
			return fmt.Errorf("%s database has no schema yet (missing table for %T) — initialize it with --auto-migrate, or apply the DDL from `nyro tool migrate dump`/`diff`", b.backend, m)
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
func (b *Backend) Consumers() storage.ConsumerStore {
	return consumerStore{q: b.q, plaintextKeys: b.plaintextKeys}
}

// Auth returns the config-schema inbound key-auth read path.
func (b *Backend) Auth() storage.KeyAuthStore { return keyAuthStore{q: b.q} }

// Settings returns the config-schema settings store (key column).
func (b *Backend) Settings() storage.SettingsStore { return coreSettingsStore{q: b.q} }

// Migrator returns the backend itself (it already implements Init/Migrate/Health).
func (b *Backend) Migrator() storage.Migrator { return b }

var _ storage.Storage = (*Backend)(nil)
