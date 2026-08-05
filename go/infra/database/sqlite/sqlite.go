// Package sqlite opens consistently configured, pure-Go SQLite connection
// pools for reusable infrastructure modules.
//
// The package owns connection policy only. Callers own the returned database
// handle as well as their schemas, migrations, queries, and lifecycle.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite"
)

var memoryDatabaseID atomic.Uint64

const (
	defaultBusyTimeout = 5 * time.Second
	defaultMaxConns    = 4
)

// Options configures a SQLite connection pool.
type Options struct {
	Path         string
	BusyTimeout  time.Duration
	MaxOpenConns int
	MaxIdleConns int
}

// Open opens and verifies a caller-owned SQLite connection pool. It does not
// create parent directories, run application migrations, or assume ownership
// of the returned pool.
func Open(ctx context.Context, opts Options) (*sql.DB, error) {
	if opts.Path == "" {
		return nil, errors.New("sqlite: path is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: open: %w", err)
	}

	busyTimeout := opts.BusyTimeout
	if busyTimeout <= 0 {
		busyTimeout = defaultBusyTimeout
	}
	maxOpen := opts.MaxOpenConns
	if maxOpen <= 0 {
		maxOpen = defaultMaxConns
	}
	maxIdle := opts.MaxIdleConns
	if maxIdle <= 0 {
		maxIdle = maxOpen
	}

	dsn, err := dataSourceName(opts.Path, busyTimeout)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("sqlite: open: %w", err)
	}
	db.SetMaxOpenConns(maxOpen)
	db.SetMaxIdleConns(maxIdle)

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("sqlite: ping: %w", err)
	}
	return db, nil
}

func dataSourceName(path string, busyTimeout time.Duration) (string, error) {
	var u *url.URL
	var err error
	switch {
	case path == ":memory:":
		u, err = url.Parse(fmt.Sprintf("file:nyro-infra-memory-%d?mode=memory&cache=shared", memoryDatabaseID.Add(1)))
	case strings.HasPrefix(path, "file:"):
		u, err = url.Parse(path)
	default:
		u = &url.URL{Scheme: "file", Path: filepath.ToSlash(path)}
	}
	if err != nil {
		return "", fmt.Errorf("sqlite: parse path: %w", err)
	}

	q := u.Query()
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(NORMAL)")
	q.Add("_pragma", "foreign_keys(ON)")
	q.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", busyTimeout.Milliseconds()))
	u.RawQuery = q.Encode()
	return u.String(), nil
}
