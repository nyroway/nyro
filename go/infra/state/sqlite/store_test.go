package sqlite_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	dbsqlite "github.com/nyroway/nyro/go/infra/database/sqlite"
	"github.com/nyroway/nyro/go/infra/state"
	statesqlite "github.com/nyroway/nyro/go/infra/state/sqlite"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func newStore(t *testing.T, opts statesqlite.Options) (*statesqlite.Store, *fakeClock) {
	t.Helper()
	ctx := context.Background()
	db, err := dbsqlite.Open(ctx, dbsqlite.Options{Path: filepath.Join(t.TempDir(), "state.db")})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	clock := &fakeClock{now: time.Unix(1_800_000_000, 0)}
	if opts.Now == nil {
		opts.Now = clock.Now
	}
	if opts.CleanupInterval == 0 {
		opts.CleanupInterval = -1
	}
	store, err := statesqlite.New(ctx, db, opts)
	if err != nil {
		t.Fatalf("new state store: %v", err)
	}
	t.Cleanup(func() { _ = store.Shutdown(context.Background()) })
	return store, clock
}

func TestStoreSetAndGetBinaryValue(t *testing.T) {
	ctx := context.Background()
	store, _ := newStore(t, statesqlite.Options{})

	key := []byte{'k', 0, '1'}
	want := []byte{0, 1, 2, 255}
	result, err := store.Set(ctx, key, want, state.SetOptions{})
	if err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	if !result.Applied {
		t.Fatal("Set() Applied = false, want true")
	}

	got, err := store.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if !got.Found || string(got.Bytes) != string(want) {
		t.Fatalf("Get() = %#v, want bytes %v", got, want)
	}

	got.Bytes[0] = 9
	again, err := store.Get(ctx, key)
	if err != nil {
		t.Fatalf("second Get() error = %v", err)
	}
	if again.Bytes[0] != 0 {
		t.Fatal("Get() returned storage-owned bytes")
	}
}

func TestStoreSetConditionsPreviousAndKeepTTL(t *testing.T) {
	ctx := context.Background()
	store, clock := newStore(t, statesqlite.Options{})
	key := []byte("key")

	if _, err := store.Set(ctx, key, []byte("one"), state.SetOptions{Expiration: time.Minute}); err != nil {
		t.Fatalf("initial Set() error = %v", err)
	}
	result, err := store.Set(ctx, key, []byte("ignored"), state.SetOptions{
		Condition: state.SetIfMissing, GetPrevious: true,
	})
	if err != nil {
		t.Fatalf("conditional Set() error = %v", err)
	}
	if result.Applied || !result.Previous.Found || string(result.Previous.Bytes) != "one" {
		t.Fatalf("conditional Set() = %#v", result)
	}

	result, err = store.Set(ctx, key, []byte("two"), state.SetOptions{
		Condition: state.SetIfPresent, GetPrevious: true, KeepTTL: true,
	})
	if err != nil {
		t.Fatalf("replacement Set() error = %v", err)
	}
	if !result.Applied || string(result.Previous.Bytes) != "one" {
		t.Fatalf("replacement Set() = %#v", result)
	}
	clock.Advance(10 * time.Second)
	ttl, err := store.TTL(ctx, key)
	if err != nil {
		t.Fatalf("TTL() error = %v", err)
	}
	if ttl.State != state.TTLExpiring || ttl.Remaining != 50*time.Second {
		t.Fatalf("TTL() = %#v, want 50s", ttl)
	}

	if _, err := store.Set(ctx, []byte("missing"), []byte("x"), state.SetOptions{Condition: state.SetIfPresent}); err != nil {
		t.Fatalf("XX Set() error = %v", err)
	}
	missing, _ := store.Get(ctx, []byte("missing"))
	if missing.Found {
		t.Fatal("XX Set created a missing key")
	}
	if _, err := store.Set(ctx, []byte("bad"), []byte("x"), state.SetOptions{Expiration: time.Second, KeepTTL: true}); !errors.Is(err, state.ErrInvalidOptions) {
		t.Fatalf("conflicting Set() error = %v, want ErrInvalidOptions", err)
	}
	farFuture := clock.Now().AddDate(1000, 0, 0)
	if _, err := store.Set(ctx, []byte("far"), []byte("x"), state.SetOptions{ExpireAt: farFuture}); !errors.Is(err, state.ErrInvalidOptions) {
		t.Fatalf("far-future Set() error = %v, want ErrInvalidOptions", err)
	}
	if applied, err := store.Expire(ctx, key, farFuture, state.ExpireOptions{}); applied || !errors.Is(err, state.ErrInvalidOptions) {
		t.Fatalf("far-future Expire() = %v, %v, want ErrInvalidOptions", applied, err)
	}
	farPast := time.Date(-300_000_000, time.January, 1, 0, 0, 0, 0, time.UTC)
	if _, err := store.Set(ctx, []byte("far-past"), []byte("x"), state.SetOptions{ExpireAt: farPast}); !errors.Is(err, state.ErrInvalidOptions) {
		t.Fatalf("far-past Set() error = %v, want ErrInvalidOptions", err)
	}
}

func TestStoreBulkDeleteAndExists(t *testing.T) {
	ctx := context.Background()
	store, _ := newStore(t, statesqlite.Options{})
	if err := store.MSet(ctx, []state.Pair{
		{Key: []byte("a"), Value: []byte("1")},
		{Key: []byte("b"), Value: []byte("2")},
	}); err != nil {
		t.Fatalf("MSet() error = %v", err)
	}
	values, err := store.MGet(ctx, []byte("b"), []byte("missing"), []byte("a"))
	if err != nil {
		t.Fatalf("MGet() error = %v", err)
	}
	if len(values) != 3 || string(values[0].Bytes) != "2" || values[1].Found || string(values[2].Bytes) != "1" {
		t.Fatalf("MGet() = %#v", values)
	}
	count, err := store.Exists(ctx, []byte("a"), []byte("a"), []byte("missing"))
	if err != nil || count != 2 {
		t.Fatalf("Exists() = %d, %v, want 2", count, err)
	}
	deleted, err := store.Delete(ctx, []byte("a"), []byte("a"), []byte("missing"))
	if err != nil || deleted != 1 {
		t.Fatalf("Delete() = %d, %v, want 1", deleted, err)
	}
}

func TestStoreExpirePersistAndLazyExpiration(t *testing.T) {
	ctx := context.Background()
	store, clock := newStore(t, statesqlite.Options{})
	key := []byte("key")
	_, _ = store.Set(ctx, key, []byte("value"), state.SetOptions{})

	ttl, err := store.TTL(ctx, key)
	if err != nil || ttl.State != state.TTLPersistent {
		t.Fatalf("initial TTL() = %#v, %v", ttl, err)
	}
	applied, err := store.Expire(ctx, key, clock.Now().Add(time.Minute), state.ExpireOptions{Condition: state.ExpireIfNoExpiry})
	if err != nil || !applied {
		t.Fatalf("Expire NX = %v, %v", applied, err)
	}
	applied, err = store.Expire(ctx, key, clock.Now().Add(30*time.Second), state.ExpireOptions{Condition: state.ExpireIfGreater})
	if err != nil || applied {
		t.Fatalf("Expire GT shorter = %v, %v", applied, err)
	}
	applied, err = store.Expire(ctx, key, clock.Now().Add(30*time.Second), state.ExpireOptions{Condition: state.ExpireIfLess})
	if err != nil || !applied {
		t.Fatalf("Expire LT shorter = %v, %v", applied, err)
	}
	persisted, err := store.Persist(ctx, key)
	if err != nil || !persisted {
		t.Fatalf("Persist() = %v, %v", persisted, err)
	}
	_, _ = store.Expire(ctx, key, clock.Now().Add(time.Second), state.ExpireOptions{})
	clock.Advance(2 * time.Second)
	value, _ := store.Get(ctx, key)
	if value.Found {
		t.Fatal("expired key remained visible")
	}
	ttl, _ = store.TTL(ctx, key)
	if ttl.State != state.TTLMissing {
		t.Fatalf("expired TTL() = %#v, want missing", ttl)
	}
	deleted, err := store.Delete(ctx, key)
	if err != nil || deleted != 0 {
		t.Fatalf("Delete() expired key = %d, %v, want 0", deleted, err)
	}
}

func TestStoreIncrByErrorsAndOverflow(t *testing.T) {
	ctx := context.Background()
	store, _ := newStore(t, statesqlite.Options{})
	got, err := store.IncrBy(ctx, []byte("counter"), 5)
	if err != nil || got != 5 {
		t.Fatalf("first IncrBy() = %d, %v", got, err)
	}
	got, err = store.IncrBy(ctx, []byte("counter"), -2)
	if err != nil || got != 3 {
		t.Fatalf("second IncrBy() = %d, %v", got, err)
	}
	_, _ = store.Set(ctx, []byte("text"), []byte("nope"), state.SetOptions{})
	if _, err := store.IncrBy(ctx, []byte("text"), 1); !errors.Is(err, state.ErrNotInteger) {
		t.Fatalf("non-integer IncrBy() error = %v", err)
	}
	_, _ = store.Set(ctx, []byte("max"), []byte("9223372036854775807"), state.SetOptions{})
	if _, err := store.IncrBy(ctx, []byte("max"), 1); !errors.Is(err, state.ErrOverflow) {
		t.Fatalf("overflow IncrBy() error = %v", err)
	}
	value, _ := store.Get(ctx, []byte("max"))
	if string(value.Bytes) != "9223372036854775807" {
		t.Fatalf("overflow changed value to %q", value.Bytes)
	}
	_, _ = store.Set(ctx, []byte("min"), []byte("-9223372036854775808"), state.SetOptions{})
	if _, err := store.IncrBy(ctx, []byte("min"), -1); !errors.Is(err, state.ErrOverflow) {
		t.Fatalf("underflow IncrBy() error = %v", err)
	}
}

func TestStoreUpdateRollsBack(t *testing.T) {
	ctx := context.Background()
	store, _ := newStore(t, statesqlite.Options{})
	wantErr := errors.New("stop")
	err := store.Update(ctx, func(ops state.Operations) error {
		if _, err := ops.Set(ctx, []byte("a"), []byte("1"), state.SetOptions{}); err != nil {
			return err
		}
		if _, err := ops.IncrBy(ctx, []byte("counter"), 1); err != nil {
			return err
		}
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Update() error = %v, want %v", err, wantErr)
	}
	for _, key := range [][]byte{[]byte("a"), []byte("counter")} {
		value, _ := store.Get(ctx, key)
		if value.Found {
			t.Fatalf("rolled-back key %q exists", key)
		}
	}
}

func TestStoreConcurrentIncrementsAreAtomic(t *testing.T) {
	ctx := context.Background()
	store, _ := newStore(t, statesqlite.Options{})
	const goroutines = 100
	const increments = 20
	var wg sync.WaitGroup
	errCh := make(chan error, goroutines)
	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range increments {
				if _, err := store.IncrBy(ctx, []byte("counter"), 1); err != nil {
					errCh <- err
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("IncrBy() error = %v", err)
	}
	value, err := store.Get(ctx, []byte("counter"))
	if err != nil || string(value.Bytes) != "2000" {
		t.Fatalf("counter = %q, %v, want 2000", value.Bytes, err)
	}
}

func TestStoreValueAndTTLSurviveRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	clock := &fakeClock{now: time.Unix(1_800_000_000, 0)}

	db, err := dbsqlite.Open(ctx, dbsqlite.Options{Path: path})
	if err != nil {
		t.Fatalf("first open database: %v", err)
	}
	store, err := statesqlite.New(ctx, db, statesqlite.Options{Now: clock.Now, CleanupInterval: -1})
	if err != nil {
		t.Fatalf("first new store: %v", err)
	}
	if _, err := store.Set(ctx, []byte("persistent"), []byte("value"), state.SetOptions{Expiration: time.Hour}); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	if err := store.Shutdown(ctx); err != nil {
		t.Fatalf("first Shutdown() error = %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("first database Close() error = %v", err)
	}

	clock.Advance(10 * time.Minute)
	db, err = dbsqlite.Open(ctx, dbsqlite.Options{Path: path})
	if err != nil {
		t.Fatalf("second open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err = statesqlite.New(ctx, db, statesqlite.Options{Now: clock.Now, CleanupInterval: -1})
	if err != nil {
		t.Fatalf("second new store: %v", err)
	}
	t.Cleanup(func() { _ = store.Shutdown(context.Background()) })
	value, err := store.Get(ctx, []byte("persistent"))
	if err != nil || !value.Found || string(value.Bytes) != "value" {
		t.Fatalf("Get() after restart = %#v, %v", value, err)
	}
	ttl, err := store.TTL(ctx, []byte("persistent"))
	if err != nil || ttl.State != state.TTLExpiring || ttl.Remaining != 50*time.Minute {
		t.Fatalf("TTL() after restart = %#v, %v", ttl, err)
	}
}

func TestMGetObservesOneSnapshotDuringConcurrentMSet(t *testing.T) {
	ctx := context.Background()
	store, _ := newStore(t, statesqlite.Options{})
	keys := make([][]byte, 10)
	pairs := make([]state.Pair, len(keys))
	for index := range keys {
		keys[index] = []byte(string(rune('a' + index)))
		pairs[index] = state.Pair{Key: keys[index], Value: []byte("0")}
	}
	if err := store.MSet(ctx, pairs); err != nil {
		t.Fatalf("initial MSet() error = %v", err)
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	errCh := make(chan error, 1)
	go func() {
		defer close(done)
		value := byte('1')
		for {
			select {
			case <-stop:
				return
			default:
			}
			for index := range pairs {
				pairs[index].Value = []byte{value}
			}
			if err := store.MSet(ctx, pairs); err != nil {
				errCh <- err
				return
			}
			if value == '1' {
				value = '0'
			} else {
				value = '1'
			}
		}
	}()
	t.Cleanup(func() {
		close(stop)
		<-done
	})
	for range 500 {
		values, err := store.MGet(ctx, keys...)
		if err != nil {
			t.Fatalf("MGet() error = %v", err)
		}
		for index := 1; index < len(values); index++ {
			if string(values[index].Bytes) != string(values[0].Bytes) {
				t.Fatalf("MGet() mixed snapshots: %#v", values)
			}
		}
		select {
		case err := <-errCh:
			t.Fatalf("concurrent MSet() error = %v", err)
		default:
		}
	}
}
