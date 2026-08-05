// Package sqlite opens consistently configured, pure-Go SQLite connection
// pools for reusable infrastructure modules.
//
// The package owns connection policy only. Callers own the returned database
// handle as well as their schemas, migrations, queries, and lifecycle.
package sqlite
