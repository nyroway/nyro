// Package storage implements the persistence layer: the Storage interface and
// typed sub-stores (providers, models, model backends, api keys, logs, oauth
// credentials, settings), the idempotent migration framework, and the
// sqlite/postgres/mysql/memory backends.
//
// Ported from crates/nyro-core/src/db (migrations + DDL) and
// crates/nyro-core/src/storage (backends + traits).
package storage
