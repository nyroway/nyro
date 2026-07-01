// Package database implements the shared SQL storage backend for SQLite,
// MySQL, and Postgres.
package database

import (
	sqlite "github.com/glebarez/sqlite"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"github.com/nyroway/nyro/go/internal/storage"
	"github.com/nyroway/nyro/go/internal/storage/model"
)

// Backend is the shared SQL backend.
type Backend struct {
	backend string
	db      *gorm.DB
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
	return &Backend{backend: "sqlite", db: db}, nil
}

// NewMySQL opens a MySQL database and returns a shared SQL backend.
func NewMySQL(dsn string) (*Backend, error) {
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	if err != nil {
		return nil, err
	}
	return &Backend{backend: "mysql", db: db}, nil
}

// NewPostgres opens a Postgres database and returns a shared SQL backend.
func NewPostgres(dsn string) (*Backend, error) {
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		return nil, err
	}
	return &Backend{backend: "postgres", db: db}, nil
}

// DB exposes the underlying GORM database for tests and advanced callers.
func (b *Backend) DB() *gorm.DB { return b.db }

// Init performs backend initialization.
func (b *Backend) Init() error { return nil }

// Migrate initializes the new Go config schema.
func (b *Backend) Migrate() error {
	return b.db.AutoMigrate(model.All()...)
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
