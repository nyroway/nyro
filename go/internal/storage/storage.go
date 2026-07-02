package storage

import (
	"errors"
)

// ErrNotFound is returned by Update/Delete when no row matches the id.
var ErrNotFound = errors.New("storage: not found")

// StorageHealth describes a backend's runtime status.
type StorageHealth struct {
	Backend          string // "sqlite" | "postgres" | "mysql" | "memory"
	CanConnect       bool
	SchemaCompatible bool
	Writable         bool
}

// Bootstrap handles schema initialization, migration, and health.
type Bootstrap interface {
	Init() error
	Migrate() error
	Health() (StorageHealth, error)
}
