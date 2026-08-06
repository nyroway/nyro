// Package postgres opens caller-owned PostgreSQL connection pools.
package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/stdlib"
)

// Options configures a PostgreSQL connection pool.
type Options struct {
	DSN             string
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	ConnMaxIdleTime time.Duration
}

// Open opens and verifies a caller-owned PostgreSQL connection pool.
func Open(ctx context.Context, opts Options) (*sql.DB, error) {
	if opts.DSN == "" {
		return nil, errors.New("postgres: DSN is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("postgres: open: %w", err)
	}

	config, location, err := connectionConfig(opts.DSN)
	if err != nil {
		return nil, err
	}
	var openOptions []stdlib.OptionOpenDB
	if location != nil {
		openOptions = append(openOptions, stdlib.OptionAfterConnect(func(_ context.Context, conn *pgx.Conn) error {
			conn.TypeMap().RegisterType(&pgtype.Type{
				Name:  "timestamp",
				OID:   pgtype.TimestampOID,
				Codec: &pgtype.TimestampCodec{ScanLocation: location},
			})
			return nil
		}))
	}

	db := stdlib.OpenDB(*config, openOptions...)
	if opts.MaxOpenConns > 0 {
		db.SetMaxOpenConns(opts.MaxOpenConns)
	}
	if opts.MaxIdleConns > 0 {
		db.SetMaxIdleConns(opts.MaxIdleConns)
	}
	if opts.ConnMaxLifetime > 0 {
		db.SetConnMaxLifetime(opts.ConnMaxLifetime)
	}
	if opts.ConnMaxIdleTime > 0 {
		db.SetConnMaxIdleTime(opts.ConnMaxIdleTime)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("postgres: ping: %w", safeError(err, opts.DSN))
	}
	return db, nil
}

// connectionConfig mirrors the timestamp handling used by GORM's PostgreSQL
// dialector when it owns the connection. The infra layer owns the pool now, so
// it must also install the location-aware timestamp codec.
func connectionConfig(dsn string) (*pgx.ConnConfig, *time.Location, error) {
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, nil, fmt.Errorf("postgres: parse DSN: %w", safeError(err, dsn))
	}

	var zone string
	for key, value := range config.RuntimeParams {
		if strings.EqualFold(key, "timezone") || strings.EqualFold(key, "time_zone") {
			zone = value
			delete(config.RuntimeParams, key)
		}
	}
	if zone == "" {
		return config, nil, nil
	}

	location, err := time.LoadLocation(zone)
	if err != nil {
		return nil, nil, fmt.Errorf("postgres: load TimeZone %q: %w", zone, err)
	}
	config.RuntimeParams["timezone"] = zone
	return config, location, nil
}

func safeError(err error, dsn string) error {
	message := err.Error()
	u, parseErr := url.Parse(dsn)
	if parseErr != nil || u.User == nil {
		return errors.New(strings.ReplaceAll(message, dsn, "<redacted DSN>"))
	}
	password, ok := u.User.Password()
	if !ok {
		return errors.New(strings.ReplaceAll(message, dsn, u.String()))
	}
	message = strings.ReplaceAll(message, password, "xxxxx")
	message = strings.ReplaceAll(message, url.QueryEscape(password), "xxxxx")
	u.User = url.UserPassword(u.User.Username(), "xxxxx")
	message = strings.ReplaceAll(message, dsn, u.String())
	return errors.New(message)
}
