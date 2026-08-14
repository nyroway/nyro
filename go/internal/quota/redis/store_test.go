package redis

import (
	"context"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"

	dbsqlite "github.com/nyroway/nyro/go/internal/platform/database/sqlite"
	embeddedredis "github.com/nyroway/nyro/go/internal/platform/state/redis"
	statesqlite "github.com/nyroway/nyro/go/internal/platform/state/sqlite"
	"github.com/nyroway/nyro/go/internal/quota"
)

func TestRedisStoreSharesUsageAcrossInstancesAndWindows(t *testing.T) {
	addr, shutdown := startEmbeddedRedis(t)
	defer shutdown()
	clientA := newClient(t, addr)
	clientB := newClient(t, addr)
	clock := &storeClock{now: time.Unix(2_000_000, 0).Truncate(time.Minute)}
	storeA := newStore(t, clientA, clock.Now)
	storeB := newStore(t, clientB, clock.Now)
	ctx := context.Background()

	if err := storeA.Record(ctx, "consumer", quota.Usage{Requests: 1, Tokens: 150}); err != nil {
		t.Fatal(err)
	}
	assertRedisValue(t, storeB, "consumer", "requests", time.Minute, 1)
	assertRedisValue(t, storeB, "consumer", "tokens", time.Minute, 150)
	assertRedisValue(t, storeB, "consumer", "requests", quota.MaxWindow, 1)
	assertRedisValue(t, storeB, "missing", "requests", time.Minute, 0)

	clock.now = clock.now.Add(90 * time.Minute)
	assertRedisValue(t, storeB, "consumer", "requests", time.Minute, 0)
	assertRedisValue(t, storeB, "consumer", "requests", quota.MaxWindow, 1)

	clock.now = clock.now.Add(25 * time.Hour)
	assertRedisValue(t, storeB, "consumer", "requests", quota.MaxWindow, 0)
}

func TestRedisStoreConcurrentRecordIsExact(t *testing.T) {
	addr, shutdown := startEmbeddedRedis(t)
	defer shutdown()
	client := newClient(t, addr)
	clock := &storeClock{now: time.Unix(3_000_000, 0).Truncate(time.Minute)}
	store := newStore(t, client, clock.Now)
	ctx := context.Background()
	const writers = 20
	const perWriter = 25
	var wg sync.WaitGroup
	errs := make(chan error, writers)

	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < perWriter; j++ {
				if err := store.Record(ctx, "consumer", quota.Usage{Requests: 1, Tokens: 3}); err != nil {
					errs <- err
					return
				}
			}
			errs <- nil
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	assertRedisValue(t, store, "consumer", "requests", time.Minute, writers*perWriter)
	assertRedisValue(t, store, "consumer", "tokens", time.Minute, writers*perWriter*3)
}

func TestRedisStoreConcurrencyLeaseAcrossInstances(t *testing.T) {
	addr, shutdown := startEmbeddedRedis(t)
	defer shutdown()
	clientA := newClient(t, addr)
	clientB := newClient(t, addr)
	clock := &storeClock{now: time.Unix(4_000_000, 0).Truncate(time.Minute)}
	storeA := newStore(t, clientA, clock.Now)
	storeB := newStore(t, clientB, clock.Now)
	ctx := context.Background()

	first, allowed, err := storeA.Acquire(ctx, "consumer", 1, 5*time.Minute)
	if err != nil || !allowed || first == nil {
		t.Fatalf("first Acquire() = %v, %v, %v", first, allowed, err)
	}
	if _, allowed, err := storeB.Acquire(ctx, "consumer", 1, 5*time.Minute); err != nil || allowed {
		t.Fatalf("second Acquire() = allowed %v, error %v", allowed, err)
	}
	if err := first.Release(ctx); err != nil {
		t.Fatal(err)
	}
	if err := first.Release(ctx); err != nil {
		t.Fatal(err)
	}
	third, allowed, err := storeB.Acquire(ctx, "consumer", 1, 5*time.Minute)
	if err != nil || !allowed || third == nil {
		t.Fatalf("Acquire() after release = %v, %v, %v", third, allowed, err)
	}

	clock.now = clock.now.Add(6 * time.Minute)
	expiredReplacement, allowed, err := storeA.Acquire(ctx, "consumer", 1, 5*time.Minute)
	if err != nil || !allowed || expiredReplacement == nil {
		t.Fatalf("Acquire() after expiry = %v, %v, %v", expiredReplacement, allowed, err)
	}
}

func TestRedisStoreShortAcquireDoesNotShortenExistingLeaseTTL(t *testing.T) {
	addr, shutdown := startEmbeddedRedis(t)
	defer shutdown()
	client := newClient(t, addr)
	store := newStore(t, client, time.Now)
	ctx := context.Background()

	lease, allowed, err := store.Acquire(ctx, "consumer", 1, 10*time.Minute)
	if err != nil || !allowed || lease == nil {
		t.Fatalf("long Acquire() = %v, %v, %v", lease, allowed, err)
	}
	before, err := client.PTTL(ctx, concurrencyKey("consumer")).Result()
	if err != nil {
		t.Fatal(err)
	}
	if _, allowed, err := store.Acquire(ctx, "consumer", 1, time.Minute); err != nil || allowed {
		t.Fatalf("short Acquire() = allowed %v, error %v", allowed, err)
	}
	after, err := client.PTTL(ctx, concurrencyKey("consumer")).Result()
	if err != nil {
		t.Fatal(err)
	}
	if after < before-time.Second {
		t.Fatalf("short acquire reduced key TTL from %v to %v", before, after)
	}
}

func TestRedisStoreReturnsInfrastructureErrorsAfterDisconnect(t *testing.T) {
	addr, shutdown := startEmbeddedRedis(t)
	client := newClient(t, addr)
	clock := &storeClock{now: time.Unix(5_000_000, 0).Truncate(time.Minute)}
	store := newStore(t, client, clock.Now)
	ctx := context.Background()
	lease, allowed, err := store.Acquire(ctx, "consumer", 1, time.Minute)
	if err != nil || !allowed {
		t.Fatalf("Acquire() before shutdown = %v, %v, %v", lease, allowed, err)
	}
	shutdown()

	if _, err := store.Value(ctx, "consumer", "requests", time.Minute); err == nil {
		t.Fatal("Value() error = nil after disconnect")
	}
	if err := store.Record(ctx, "consumer", quota.Usage{Requests: 1}); err == nil {
		t.Fatal("Record() error = nil after disconnect")
	}
	if _, _, err := store.Acquire(ctx, "consumer", 1, time.Minute); err == nil {
		t.Fatal("Acquire() error = nil after disconnect")
	}
	if err := lease.Release(ctx); err == nil {
		t.Fatal("Release() error = nil after disconnect")
	}
}

type storeClock struct {
	now time.Time
}

func (c *storeClock) Now() time.Time {
	return c.now
}

func newStore(t *testing.T, client goredis.Cmdable, now func() time.Time) *Store {
	t.Helper()
	store, err := New(client, Options{Now: now})
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func newClient(t *testing.T, addr string) *goredis.Client {
	t.Helper()
	client := goredis.NewClient(&goredis.Options{
		Addr:         addr,
		Protocol:     3,
		DialTimeout:  100 * time.Millisecond,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
		MaxRetries:   -1,
	})
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func assertRedisValue(t *testing.T, store quota.Store, consumerID, quotaType string, window time.Duration, want int64) {
	t.Helper()
	got, err := store.Value(context.Background(), consumerID, quotaType, window)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("Value(%q, %q, %v) = %d, want %d", consumerID, quotaType, window, got, want)
	}
}

func startEmbeddedRedis(t *testing.T) (string, func()) {
	t.Helper()
	ctx := context.Background()
	db, err := dbsqlite.Open(ctx, dbsqlite.Options{Path: filepath.Join(t.TempDir(), "state.db")})
	if err != nil {
		t.Fatal(err)
	}
	stateStore, err := statesqlite.New(ctx, db, statesqlite.Options{CleanupInterval: -1})
	if err != nil {
		t.Fatal(err)
	}
	server, err := embeddedredis.New(embeddedredis.Options{Store: stateStore})
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()

	var once sync.Once
	shutdown := func() {
		once.Do(func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if err := server.Shutdown(shutdownCtx); err != nil {
				t.Errorf("server shutdown: %v", err)
			}
			if err := <-done; err != nil {
				t.Errorf("Serve() error: %v", err)
			}
			if err := stateStore.Shutdown(shutdownCtx); err != nil {
				t.Errorf("state shutdown: %v", err)
			}
			if err := db.Close(); err != nil {
				t.Errorf("database close: %v", err)
			}
		})
	}
	t.Cleanup(shutdown)
	return listener.Addr().String(), shutdown
}

func TestRedisStoreNewRequiresClient(t *testing.T) {
	if _, err := New(nil, Options{}); err == nil {
		t.Fatal("New(nil) error = nil")
	}
}
