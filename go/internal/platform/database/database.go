// Package database opens SQL connection pools shared by Nyro infrastructure.
//
// Layer: 0 (foundation). It may import the standard library, database drivers,
// and its own driver subpackages, but no other Nyro domain.
package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/nyroway/nyro/go/internal/platform/database/postgres"
	"github.com/nyroway/nyro/go/internal/platform/database/sqlite"
)

// Kind identifies a supported SQL database engine.
type Kind string

const (
	KindSQLite   Kind = "sqlite"
	KindPostgres Kind = "postgres"
)

// Options contains driver-specific connection settings.
type Options struct {
	SQLite   sqlite.Options
	Postgres postgres.Options
}

// Connection owns a verified SQL connection pool.
type Connection struct {
	Kind Kind
	DB   *sql.DB

	closeOnce sync.Once
	closeErr  error
}

// Close closes the pool. It is safe to call more than once.
func (c *Connection) Close() error {
	if c == nil {
		return nil
	}
	c.closeOnce.Do(func() {
		if c.DB != nil {
			c.closeErr = c.DB.Close()
		}
	})
	return c.closeErr
}

// ParseDSN identifies a supported DSN and returns its driver-native value.
func ParseDSN(dsn string) (Kind, string, error) {
	switch {
	case strings.HasPrefix(dsn, "sqlite://"):
		return KindSQLite, strings.TrimPrefix(dsn, "sqlite://"), nil
	case strings.HasPrefix(dsn, "postgres://"):
		return KindPostgres, dsn, nil
	case dsn == "":
		return "", "", errors.New("--dsn is empty (want sqlite:// or postgres://)")
	default:
		scheme, _, found := strings.Cut(dsn, "://")
		if !found {
			scheme = "<missing>"
		}
		return "", "", fmt.Errorf("unrecognized --dsn scheme %q (want sqlite:// or postgres://)", scheme)
	}
}

// Open parses dsn, opens the selected driver, and returns an owned connection.
func Open(ctx context.Context, dsn string, opts Options) (*Connection, error) {
	kind, nativeDSN, err := ParseDSN(dsn)
	if err != nil {
		return nil, err
	}

	var db *sql.DB
	switch kind {
	case KindSQLite:
		sqliteOptions := opts.SQLite
		sqliteOptions.Path = nativeDSN
		db, err = sqlite.Open(ctx, sqliteOptions)
	case KindPostgres:
		postgresOptions := opts.Postgres
		postgresOptions.DSN = nativeDSN
		db, err = postgres.Open(ctx, postgresOptions)
	default:
		return nil, fmt.Errorf("unsupported database kind %q", kind)
	}
	if err != nil {
		return nil, fmt.Errorf("open %s database: %w", kind, err)
	}
	return &Connection{Kind: kind, DB: db}, nil
}
