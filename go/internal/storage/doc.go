// Package storage defines the persistence layer for the Nyro gateway: the
// config-schema data model (upstreams / routes / consumers / consumer keys /
// quotas / settings), the storage aggregate interface and its typed
// sub-stores, and schema bootstrap via the Migrator interface.
//
// Backends live in subpackages: memory/ (standalone mode and tests) and
// database/ (one GORM backend shared by SQLite, MySQL, and Postgres; it is
// the only package allowed to import the generated query/ code).
package storage
