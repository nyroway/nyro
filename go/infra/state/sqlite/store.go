package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strconv"
	"sync"
	"time"

	"github.com/nyroway/nyro/go/infra/state"
)

// Options configures the SQLite state store lifecycle.
type Options struct {
	CleanupInterval  time.Duration
	CleanupBatchSize int
	Now              func() time.Time
}

// Store implements state.Store on a caller-owned SQLite pool.
type Store struct {
	db     *sql.DB
	now    func() time.Time
	write  sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
	stop   sync.Once
}

type executor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type operations struct {
	exec executor
	now  func() time.Time
}

// New migrates and opens a state store without taking ownership of db.
func New(ctx context.Context, db *sql.DB, opts Options) (*Store, error) {
	if db == nil {
		return nil, errors.New("state sqlite: database is required")
	}
	if err := migrate(ctx, db); err != nil {
		return nil, err
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	s := &Store{db: db, now: now, done: make(chan struct{})}
	s.startJanitor(context.Background(), opts.CleanupInterval, opts.CleanupBatchSize)
	return s, nil
}

func migrate(ctx context.Context, db *sql.DB) error {
	const schema = `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version INTEGER PRIMARY KEY,
    applied_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS state_kv (
    key BLOB PRIMARY KEY,
    value BLOB NOT NULL,
    expires_at_ms INTEGER NULL
);
CREATE INDEX IF NOT EXISTS state_kv_expires_at ON state_kv(expires_at_ms)
    WHERE expires_at_ms IS NOT NULL;
INSERT OR IGNORE INTO schema_migrations(version, applied_at)
VALUES (1, CAST(strftime('%s','now') AS INTEGER) * 1000);
`
	var version int
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&version); err != nil {
		if _, execErr := db.ExecContext(ctx, schema); execErr != nil {
			return fmt.Errorf("state sqlite: migrate: %w", execErr)
		}
		return nil
	}
	if version > 1 {
		return fmt.Errorf("state sqlite: schema version %d is newer than supported version 1", version)
	}
	if _, err := db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("state sqlite: migrate: %w", err)
	}
	return nil
}

func (o operations) get(ctx context.Context, key []byte) (state.Value, *int64, error) {
	var value []byte
	var expires sql.NullInt64
	err := o.exec.QueryRowContext(ctx, `
SELECT value, expires_at_ms FROM state_kv
WHERE key = ? AND (expires_at_ms IS NULL OR expires_at_ms > ?)
`, key, o.now().UnixMilli()).Scan(&value, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return state.Value{}, nil, nil
	}
	if err != nil {
		return state.Value{}, nil, fmt.Errorf("state sqlite: get: %w", err)
	}
	var deadline *int64
	if expires.Valid {
		v := expires.Int64
		deadline = &v
	}
	return state.Value{Bytes: append([]byte(nil), value...), Found: true}, deadline, nil
}

func (o operations) Get(ctx context.Context, key []byte) (state.Value, error) {
	value, _, err := o.get(ctx, key)
	return value, err
}

func validateSetOptions(opts state.SetOptions) error {
	if opts.Condition > state.SetIfPresent {
		return state.ErrInvalidOptions
	}
	if opts.Expiration < 0 || (opts.Expiration > 0 && !opts.ExpireAt.IsZero()) {
		return state.ErrInvalidOptions
	}
	if opts.KeepTTL && (opts.Expiration > 0 || !opts.ExpireAt.IsZero()) {
		return state.ErrInvalidOptions
	}
	return nil
}

func (o operations) Set(ctx context.Context, key, value []byte, opts state.SetOptions) (state.SetResult, error) {
	if err := validateSetOptions(opts); err != nil {
		return state.SetResult{}, err
	}
	now := o.now()
	if !opts.ExpireAt.IsZero() && !deadlineRepresentable(now, opts.ExpireAt) {
		return state.SetResult{}, state.ErrInvalidOptions
	}
	previous, previousExpiry, err := o.get(ctx, key)
	if err != nil {
		return state.SetResult{}, err
	}
	result := state.SetResult{Applied: true}
	if opts.GetPrevious {
		result.Previous = previous
	}
	switch opts.Condition {
	case state.SetIfMissing:
		result.Applied = !previous.Found
	case state.SetIfPresent:
		result.Applied = previous.Found
	}
	if !result.Applied {
		return result, nil
	}

	var expiry any
	switch {
	case opts.KeepTTL && previousExpiry != nil:
		expiry = *previousExpiry
	case opts.Expiration > 0:
		expiry = now.Add(opts.Expiration).UnixMilli()
	case !opts.ExpireAt.IsZero():
		expiry = opts.ExpireAt.UnixMilli()
	default:
		expiry = nil
	}
	_, err = o.exec.ExecContext(ctx, `
INSERT INTO state_kv(key, value, expires_at_ms) VALUES (?, ?, ?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value, expires_at_ms = excluded.expires_at_ms
`, key, value, expiry)
	if err != nil {
		return state.SetResult{}, fmt.Errorf("state sqlite: set: %w", err)
	}
	return result, nil
}

func (o operations) MGet(ctx context.Context, keys ...[]byte) ([]state.Value, error) {
	values := make([]state.Value, len(keys))
	for i, key := range keys {
		value, err := o.Get(ctx, key)
		if err != nil {
			return nil, err
		}
		values[i] = value
	}
	return values, nil
}

func (o operations) MSet(ctx context.Context, pairs []state.Pair) error {
	for _, pair := range pairs {
		if _, err := o.Set(ctx, pair.Key, pair.Value, state.SetOptions{}); err != nil {
			return err
		}
	}
	return nil
}

func (o operations) Delete(ctx context.Context, keys ...[]byte) (int64, error) {
	seen := make(map[string]struct{}, len(keys))
	var deleted int64
	for _, key := range keys {
		encoded := string(key)
		if _, ok := seen[encoded]; ok {
			continue
		}
		seen[encoded] = struct{}{}
		result, err := o.exec.ExecContext(ctx, `
DELETE FROM state_kv
WHERE key = ? AND (expires_at_ms IS NULL OR expires_at_ms > ?)
`, key, o.now().UnixMilli())
		if err != nil {
			return 0, fmt.Errorf("state sqlite: delete: %w", err)
		}
		n, err := result.RowsAffected()
		if err != nil {
			return 0, fmt.Errorf("state sqlite: delete count: %w", err)
		}
		deleted += n
	}
	return deleted, nil
}

func (o operations) Exists(ctx context.Context, keys ...[]byte) (int64, error) {
	var count int64
	for _, key := range keys {
		value, err := o.Get(ctx, key)
		if err != nil {
			return 0, err
		}
		if value.Found {
			count++
		}
	}
	return count, nil
}

func (o operations) IncrBy(ctx context.Context, key []byte, delta int64) (int64, error) {
	value, expiry, err := o.get(ctx, key)
	if err != nil {
		return 0, err
	}
	var current int64
	if value.Found {
		current, err = strconv.ParseInt(string(value.Bytes), 10, 64)
		if err != nil {
			return 0, state.ErrNotInteger
		}
	}
	if (delta > 0 && current > math.MaxInt64-delta) || (delta < 0 && current < math.MinInt64-delta) {
		return 0, state.ErrOverflow
	}
	next := current + delta
	var deadline any
	if expiry != nil {
		deadline = *expiry
	}
	_, err = o.exec.ExecContext(ctx, `
INSERT INTO state_kv(key, value, expires_at_ms) VALUES (?, ?, ?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value, expires_at_ms = excluded.expires_at_ms
`, key, strconv.FormatInt(next, 10), deadline)
	if err != nil {
		return 0, fmt.Errorf("state sqlite: increment: %w", err)
	}
	return next, nil
}

func (o operations) Expire(ctx context.Context, key []byte, deadline time.Time, opts state.ExpireOptions) (bool, error) {
	if opts.Condition > state.ExpireIfLess {
		return false, state.ErrInvalidOptions
	}
	now := o.now()
	if !deadlineRepresentable(now, deadline) {
		return false, state.ErrInvalidOptions
	}
	value, current, err := o.get(ctx, key)
	if err != nil || !value.Found {
		return false, err
	}
	apply := true
	switch opts.Condition {
	case state.ExpireIfNoExpiry:
		apply = current == nil
	case state.ExpireIfHasExpiry:
		apply = current != nil
	case state.ExpireIfGreater:
		apply = current != nil && deadline.UnixMilli() > *current
	case state.ExpireIfLess:
		apply = current == nil || deadline.UnixMilli() < *current
	}
	if !apply {
		return false, nil
	}
	if !deadline.After(now) {
		_, err := o.Delete(ctx, key)
		return true, err
	}
	_, err = o.exec.ExecContext(ctx, `UPDATE state_kv SET expires_at_ms = ? WHERE key = ?`, deadline.UnixMilli(), key)
	if err != nil {
		return false, fmt.Errorf("state sqlite: expire: %w", err)
	}
	return true, nil
}

func deadlineRepresentable(now, deadline time.Time) bool {
	deadlineMillis := deadline.UnixMilli()
	restored := time.UnixMilli(deadlineMillis)
	if deadline.Before(restored) || !deadline.Before(restored.Add(time.Millisecond)) {
		return false
	}
	if !deadline.After(now) {
		return true
	}
	deltaMillis := uint64(deadlineMillis) - uint64(now.UnixMilli())
	return deltaMillis <= uint64(time.Duration(math.MaxInt64).Milliseconds())
}

func (o operations) TTL(ctx context.Context, key []byte) (state.TTLResult, error) {
	value, expiry, err := o.get(ctx, key)
	if err != nil {
		return state.TTLResult{}, err
	}
	if !value.Found {
		return state.TTLResult{State: state.TTLMissing}, nil
	}
	if expiry == nil {
		return state.TTLResult{State: state.TTLPersistent}, nil
	}
	return state.TTLResult{
		State:     state.TTLExpiring,
		Remaining: time.Duration(*expiry-o.now().UnixMilli()) * time.Millisecond,
	}, nil
}

func (o operations) Persist(ctx context.Context, key []byte) (bool, error) {
	result, err := o.exec.ExecContext(ctx, `
UPDATE state_kv SET expires_at_ms = NULL
WHERE key = ? AND expires_at_ms IS NOT NULL AND expires_at_ms > ?
`, key, o.now().UnixMilli())
	if err != nil {
		return false, fmt.Errorf("state sqlite: persist: %w", err)
	}
	n, err := result.RowsAffected()
	return n > 0, err
}

// Get returns a copy of the current value or a missing result.
func (s *Store) Get(ctx context.Context, key []byte) (state.Value, error) {
	return (operations{exec: s.db, now: s.now}).Get(ctx, key)
}

func (s *Store) Set(ctx context.Context, key, value []byte, opts state.SetOptions) (result state.SetResult, err error) {
	err = s.Update(ctx, func(ops state.Operations) error {
		result, err = ops.Set(ctx, key, value, opts)
		return err
	})
	return result, err
}

func (s *Store) MGet(ctx context.Context, keys ...[]byte) ([]state.Value, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("state sqlite: begin read transaction: %w", err)
	}
	values, err := (operations{exec: tx, now: s.now}).MGet(ctx, keys...)
	if err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("state sqlite: commit read transaction: %w", err)
	}
	return values, nil
}

func (s *Store) MSet(ctx context.Context, pairs []state.Pair) error {
	return s.Update(ctx, func(ops state.Operations) error { return ops.MSet(ctx, pairs) })
}

func (s *Store) Delete(ctx context.Context, keys ...[]byte) (deleted int64, err error) {
	err = s.Update(ctx, func(ops state.Operations) error {
		deleted, err = ops.Delete(ctx, keys...)
		return err
	})
	return deleted, err
}

func (s *Store) Exists(ctx context.Context, keys ...[]byte) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("state sqlite: begin read transaction: %w", err)
	}
	count, err := (operations{exec: tx, now: s.now}).Exists(ctx, keys...)
	if err != nil {
		_ = tx.Rollback()
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("state sqlite: commit read transaction: %w", err)
	}
	return count, nil
}

func (s *Store) IncrBy(ctx context.Context, key []byte, delta int64) (next int64, err error) {
	err = s.Update(ctx, func(ops state.Operations) error {
		next, err = ops.IncrBy(ctx, key, delta)
		return err
	})
	return next, err
}

func (s *Store) Expire(ctx context.Context, key []byte, deadline time.Time, opts state.ExpireOptions) (applied bool, err error) {
	err = s.Update(ctx, func(ops state.Operations) error {
		applied, err = ops.Expire(ctx, key, deadline, opts)
		return err
	})
	return applied, err
}

func (s *Store) TTL(ctx context.Context, key []byte) (state.TTLResult, error) {
	return (operations{exec: s.db, now: s.now}).TTL(ctx, key)
}

func (s *Store) Persist(ctx context.Context, key []byte) (persisted bool, err error) {
	err = s.Update(ctx, func(ops state.Operations) error {
		persisted, err = ops.Persist(ctx, key)
		return err
	})
	return persisted, err
}

// Update executes all callback operations in one SQLite transaction.
func (s *Store) Update(ctx context.Context, fn func(state.Operations) error) error {
	if fn == nil {
		return state.ErrInvalidOptions
	}
	s.write.Lock()
	defer s.write.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("state sqlite: begin transaction: %w", err)
	}
	if err := fn(operations{exec: tx, now: s.now}); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("state sqlite: commit transaction: %w", err)
	}
	return nil
}

// Shutdown stops store-owned background work without closing the database.
func (s *Store) Shutdown(ctx context.Context) error {
	s.stop.Do(func() {
		if s.cancel != nil {
			s.cancel()
		}
	})
	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

var _ state.Store = (*Store)(nil)
