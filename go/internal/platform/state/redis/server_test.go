package redis_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"

	dbsqlite "github.com/nyroway/nyro/go/internal/platform/database/sqlite"
	redisserver "github.com/nyroway/nyro/go/internal/platform/state/redis"
	statesqlite "github.com/nyroway/nyro/go/internal/platform/state/sqlite"
)

func TestGoRedisConnectsWithRESP2AndRESP3(t *testing.T) {
	for _, protocol := range []int{2, 3} {
		t.Run(goredisProtocolName(protocol), func(t *testing.T) {
			addr, shutdown := startServer(t, "")
			defer shutdown()

			ctx := context.Background()
			client := goredis.NewClient(&goredis.Options{Addr: addr, Protocol: protocol})
			t.Cleanup(func() { _ = client.Close() })
			if got, err := client.Ping(ctx).Result(); err != nil || got != "PONG" {
				t.Fatalf("Ping() = %q, %v", got, err)
			}
			if err := client.Set(ctx, "key", "value", time.Minute).Err(); err != nil {
				t.Fatalf("Set() error = %v", err)
			}
			if got, err := client.Get(ctx, "key").Result(); err != nil || got != "value" {
				t.Fatalf("Get() = %q, %v", got, err)
			}
			if got, err := client.IncrBy(ctx, "counter", 3).Result(); err != nil || got != 3 {
				t.Fatalf("IncrBy() = %d, %v", got, err)
			}
			if ok, err := client.Expire(ctx, "counter", time.Minute).Result(); err != nil || !ok {
				t.Fatalf("Expire() = %v, %v", ok, err)
			}
			if ttl, err := client.TTL(ctx, "counter").Result(); err != nil || ttl <= 0 {
				t.Fatalf("TTL() = %v, %v", ttl, err)
			}
		})
	}
}

func TestGoRedisPasswordAndTransactionPipeline(t *testing.T) {
	addr, shutdown := startServer(t, "secret")
	defer shutdown()
	ctx := context.Background()

	unauthenticated := goredis.NewClient(&goredis.Options{Addr: addr})
	if err := unauthenticated.Ping(ctx).Err(); err == nil {
		t.Fatal("unauthenticated Ping() error = nil")
	}
	_ = unauthenticated.Close()

	options, err := goredis.ParseURL("redis://:secret@" + addr + "/0?protocol=3")
	if err != nil {
		t.Fatalf("ParseURL() error = %v", err)
	}
	client := goredis.NewClient(options)
	t.Cleanup(func() { _ = client.Close() })
	pipe := client.TxPipeline()
	pipe.Set(ctx, "a", "1", 0)
	pipe.Incr(ctx, "a")
	commands, err := pipe.Exec(ctx)
	if err != nil {
		t.Fatalf("TxPipeline.Exec() error = %v", err)
	}
	if len(commands) != 2 {
		t.Fatalf("TxPipeline command count = %d, want 2", len(commands))
	}
	if got, err := client.Get(ctx, "a").Result(); err != nil || got != "2" {
		t.Fatalf("transaction result = %q, %v", got, err)
	}
}

func TestRedisWatchAbortsWhenWatchedKeyChanges(t *testing.T) {
	for _, protocol := range []int{2, 3} {
		t.Run(goredisProtocolName(protocol), func(t *testing.T) {
			addr, shutdown := startServer(t, "")
			defer shutdown()
			ctx := context.Background()
			watcher := goredis.NewClient(&goredis.Options{Addr: addr, Protocol: protocol})
			writer := goredis.NewClient(&goredis.Options{Addr: addr, Protocol: protocol})
			t.Cleanup(func() {
				_ = watcher.Close()
				_ = writer.Close()
			})

			err := watcher.Watch(ctx, func(tx *goredis.Tx) error {
				if err := writer.Set(ctx, "quota-key", "changed", 0).Err(); err != nil {
					return err
				}
				_, err := tx.TxPipelined(ctx, func(pipe goredis.Pipeliner) error {
					pipe.Incr(ctx, "quota-key")
					return nil
				})
				return err
			}, "quota-key")
			if !errors.Is(err, goredis.TxFailedErr) {
				t.Fatalf("Watch() error = %v, want redis.TxFailedErr", err)
			}
		})
	}
}

func TestRedisWatchAbortsWhenWatchedKeyExpires(t *testing.T) {
	for _, protocol := range []int{2, 3} {
		t.Run(goredisProtocolName(protocol), func(t *testing.T) {
			addr, shutdown := startServer(t, "")
			defer shutdown()
			ctx := context.Background()
			watcher := goredis.NewClient(&goredis.Options{Addr: addr, Protocol: protocol})
			observer := goredis.NewClient(&goredis.Options{Addr: addr, Protocol: protocol})
			t.Cleanup(func() {
				_ = watcher.Close()
				_ = observer.Close()
			})

			if err := watcher.Set(ctx, "expiring", "value", 25*time.Millisecond).Err(); err != nil {
				t.Fatal(err)
			}
			err := watcher.Watch(ctx, func(tx *goredis.Tx) error {
				deadline := time.Now().Add(2 * time.Second)
				for {
					_, getErr := observer.Get(ctx, "expiring").Result()
					if errors.Is(getErr, goredis.Nil) {
						break
					}
					if getErr != nil {
						return getErr
					}
					if time.Now().After(deadline) {
						return errors.New("watched key did not expire")
					}
					time.Sleep(5 * time.Millisecond)
				}
				_, txErr := tx.TxPipelined(ctx, func(pipe goredis.Pipeliner) error {
					pipe.Set(ctx, "must-not-commit", "value", 0)
					return nil
				})
				return txErr
			}, "expiring")
			if !errors.Is(err, goredis.TxFailedErr) {
				t.Fatalf("Watch() error = %v, want redis.TxFailedErr", err)
			}
			if _, err := watcher.Get(ctx, "must-not-commit").Result(); !errors.Is(err, goredis.Nil) {
				t.Fatalf("expired-key transaction committed, GET error = %v", err)
			}
		})
	}
}

func TestRedisWatchIgnoresUnrelatedMutation(t *testing.T) {
	addr, shutdown := startServer(t, "")
	defer shutdown()
	ctx := context.Background()
	watcher := goredis.NewClient(&goredis.Options{Addr: addr})
	writer := goredis.NewClient(&goredis.Options{Addr: addr})
	t.Cleanup(func() {
		_ = watcher.Close()
		_ = writer.Close()
	})

	err := watcher.Watch(ctx, func(tx *goredis.Tx) error {
		if err := writer.Set(ctx, "other", "changed", 0).Err(); err != nil {
			return err
		}
		_, err := tx.TxPipelined(ctx, func(pipe goredis.Pipeliner) error {
			pipe.Set(ctx, "watched", "committed", 0)
			return nil
		})
		return err
	}, "watched")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := watcher.Get(ctx, "watched").Result(); err != nil || got != "committed" {
		t.Fatalf("GET watched = %q, %v", got, err)
	}
}

func TestRedisUnwatchAllowsTransactionAfterMutation(t *testing.T) {
	addr, shutdown := startServer(t, "")
	defer shutdown()
	ctx := context.Background()
	clientA := goredis.NewClient(&goredis.Options{Addr: addr})
	clientB := goredis.NewClient(&goredis.Options{Addr: addr})
	t.Cleanup(func() {
		_ = clientA.Close()
		_ = clientB.Close()
	})

	if err := clientA.Watch(ctx, func(tx *goredis.Tx) error {
		if err := clientB.Set(ctx, "guard", "changed", 0).Err(); err != nil {
			return err
		}
		if err := tx.Unwatch(ctx).Err(); err != nil {
			return err
		}
		_, err := tx.TxPipelined(ctx, func(pipe goredis.Pipeliner) error {
			pipe.Set(ctx, "result", "committed", 0)
			return nil
		})
		return err
	}, "guard"); err != nil {
		t.Fatal(err)
	}
	if got, err := clientA.Get(ctx, "result").Result(); err != nil || got != "committed" {
		t.Fatalf("GET result = %q, %v", got, err)
	}
}

func TestRedisWatchTransactionRules(t *testing.T) {
	addr, shutdown := startServer(t, "")
	defer shutdown()
	ctx := context.Background()
	client := goredis.NewClient(&goredis.Options{Addr: addr})
	writer := goredis.NewClient(&goredis.Options{Addr: addr})
	t.Cleanup(func() {
		_ = client.Close()
		_ = writer.Close()
	})

	pipe := client.TxPipeline()
	pipe.Do(ctx, "WATCH", "guard")
	pipe.Set(ctx, "must-not-commit", "value", 0)
	if _, err := pipe.Exec(ctx); err == nil || !strings.Contains(err.Error(), "EXECABORT") {
		t.Fatalf("WATCH inside MULTI error = %v, want EXECABORT", err)
	}
	if _, err := client.Get(ctx, "must-not-commit").Result(); !errors.Is(err, goredis.Nil) {
		t.Fatalf("dirty transaction committed key, Get error = %v", err)
	}

	conn := client.Conn()
	t.Cleanup(func() { _ = conn.Close() })
	if err := conn.Do(ctx, "WATCH", "guard").Err(); err != nil {
		t.Fatalf("WATCH error = %v", err)
	}
	if err := conn.Do(ctx, "MULTI").Err(); err != nil {
		t.Fatalf("MULTI error = %v", err)
	}
	if err := conn.Do(ctx, "DISCARD").Err(); err != nil {
		t.Fatalf("DISCARD error = %v", err)
	}
	if err := writer.Set(ctx, "guard", "changed", 0).Err(); err != nil {
		t.Fatalf("mutate guard: %v", err)
	}
	if err := conn.Do(ctx, "MULTI").Err(); err != nil {
		t.Fatalf("second MULTI error = %v", err)
	}
	if got, err := conn.Do(ctx, "SET", "result", "after-discard").Result(); err != nil || got != "QUEUED" {
		t.Fatalf("queued SET = %v, %v", got, err)
	}
	if _, err := conn.Do(ctx, "EXEC").Result(); err != nil {
		t.Fatalf("EXEC after DISCARD error = %v", err)
	}
	if got, err := client.Get(ctx, "result").Result(); err != nil || got != "after-discard" {
		t.Fatalf("GET result = %q, %v", got, err)
	}
}

func TestRedisEmptyTransactionReturnsEmptyArray(t *testing.T) {
	for _, protocol := range []int{2, 3} {
		t.Run(goredisProtocolName(protocol), func(t *testing.T) {
			addr, shutdown := startServer(t, "")
			defer shutdown()
			ctx := context.Background()
			client := goredis.NewClient(&goredis.Options{Addr: addr, Protocol: protocol})
			t.Cleanup(func() { _ = client.Close() })
			conn := client.Conn()
			t.Cleanup(func() { _ = conn.Close() })

			if err := conn.Do(ctx, "MULTI").Err(); err != nil {
				t.Fatal(err)
			}
			result, err := conn.Do(ctx, "EXEC").Slice()
			if err != nil {
				t.Fatalf("empty EXEC error = %v", err)
			}
			if len(result) != 0 {
				t.Fatalf("empty EXEC result = %#v, want empty array", result)
			}
		})
	}
}

func TestGoRedisStringAndTTLCommandSubset(t *testing.T) {
	addr, shutdown := startServer(t, "")
	defer shutdown()
	ctx := context.Background()
	client := goredis.NewClient(&goredis.Options{Addr: addr, Protocol: 3})
	t.Cleanup(func() { _ = client.Close() })

	if ok, err := client.SetNX(ctx, "a", "1", time.Minute).Result(); err != nil || !ok {
		t.Fatalf("SetNX() = %v, %v", ok, err)
	}
	if ok, err := client.SetNX(ctx, "a", "ignored", 0).Result(); err != nil || ok {
		t.Fatalf("second SetNX() = %v, %v", ok, err)
	}
	if err := client.MSet(ctx, "b", "2", "c", "3").Err(); err != nil {
		t.Fatalf("MSet() error = %v", err)
	}
	values, err := client.MGet(ctx, "c", "missing", "b").Result()
	if err != nil || len(values) != 3 || values[0] != "3" || values[1] != nil || values[2] != "2" {
		t.Fatalf("MGet() = %#v, %v", values, err)
	}
	if count, err := client.Exists(ctx, "a", "a", "missing").Result(); err != nil || count != 2 {
		t.Fatalf("Exists() = %d, %v", count, err)
	}
	if kind, err := client.Type(ctx, "a").Result(); err != nil || kind != "string" {
		t.Fatalf("Type() = %q, %v", kind, err)
	}
	if ok, err := client.Persist(ctx, "a").Result(); err != nil || !ok {
		t.Fatalf("Persist() = %v, %v", ok, err)
	}
	if ttl, err := client.PTTL(ctx, "a").Result(); err != nil || ttl != -1 {
		t.Fatalf("persistent PTTL() = %v, %v", ttl, err)
	}
	if count, err := client.Del(ctx, "a", "a", "missing").Result(); err != nil || count != 1 {
		t.Fatalf("Del() = %d, %v", count, err)
	}
}

func TestGoRedisSortedSetSubsetRESP2AndRESP3(t *testing.T) {
	for _, protocol := range []int{2, 3} {
		t.Run(goredisProtocolName(protocol), func(t *testing.T) {
			addr, shutdown := startServer(t, "")
			defer shutdown()
			ctx := context.Background()
			client := goredis.NewClient(&goredis.Options{Addr: addr, Protocol: protocol})
			t.Cleanup(func() { _ = client.Close() })

			added, err := client.ZAdd(ctx, "leases", goredis.Z{Score: 1000, Member: "a"}).Result()
			if err != nil || added != 1 {
				t.Fatalf("first ZAdd() = %d, %v", added, err)
			}
			added, err = client.ZAdd(ctx, "leases", goredis.Z{Score: 2000, Member: "b"}).Result()
			if err != nil || added != 1 {
				t.Fatalf("second ZAdd() = %d, %v", added, err)
			}
			added, err = client.ZAdd(ctx, "leases", goredis.Z{Score: 2500, Member: "b"}).Result()
			if err != nil || added != 0 {
				t.Fatalf("updating ZAdd() = %d, %v", added, err)
			}
			if card, err := client.ZCard(ctx, "leases").Result(); err != nil || card != 2 {
				t.Fatalf("ZCard() = %d, %v", card, err)
			}
			if removed, err := client.ZRemRangeByScore(ctx, "leases", "-inf", "1500").Result(); err != nil || removed != 1 {
				t.Fatalf("ZRemRangeByScore() = %d, %v", removed, err)
			}
			if removed, err := client.ZRem(ctx, "leases", "b").Result(); err != nil || removed != 1 {
				t.Fatalf("ZRem() = %d, %v", removed, err)
			}
			if card, err := client.ZCard(ctx, "leases").Result(); err != nil || card != 0 {
				t.Fatalf("empty ZCard() = %d, %v", card, err)
			}
		})
	}
}

func TestGoRedisSortedSetTransactionPreservesTTL(t *testing.T) {
	addr, shutdown := startServer(t, "")
	defer shutdown()
	ctx := context.Background()
	client := goredis.NewClient(&goredis.Options{Addr: addr, Protocol: 3})
	t.Cleanup(func() { _ = client.Close() })

	var card *goredis.IntCmd
	_, err := client.TxPipelined(ctx, func(pipe goredis.Pipeliner) error {
		pipe.ZRemRangeByScore(ctx, "leases", "-inf", "1000")
		pipe.ZAdd(ctx, "leases", goredis.Z{Score: 2000, Member: "member"})
		card = pipe.ZCard(ctx, "leases")
		pipe.Expire(ctx, "leases", time.Minute)
		return nil
	})
	if err != nil {
		t.Fatalf("TxPipelined() error = %v", err)
	}
	if got, err := card.Result(); err != nil || got != 1 {
		t.Fatalf("queued ZCard() = %d, %v", got, err)
	}
	if ttl, err := client.TTL(ctx, "leases").Result(); err != nil || ttl <= 0 {
		t.Fatalf("TTL() = %v, %v", ttl, err)
	}
}

func TestGoRedisSortedSetTypeBehaviorAndBinaryMember(t *testing.T) {
	addr, shutdown := startServer(t, "")
	defer shutdown()
	ctx := context.Background()
	client := goredis.NewClient(&goredis.Options{Addr: addr, Protocol: 3})
	t.Cleanup(func() { _ = client.Close() })

	if err := client.Set(ctx, "plain", "value", 0).Err(); err != nil {
		t.Fatal(err)
	}
	if err := client.ZAdd(ctx, "plain", goredis.Z{Score: 1, Member: "a"}).Err(); err == nil || !strings.Contains(err.Error(), "WRONGTYPE") {
		t.Fatalf("ZAdd string error = %v", err)
	}

	binaryMember := string([]byte{0, 255, 1})
	if err := client.ZAdd(ctx, "leases", goredis.Z{Score: 1, Member: binaryMember}).Err(); err != nil {
		t.Fatalf("binary ZAdd() error = %v", err)
	}
	if _, err := client.Get(ctx, "leases").Result(); err == nil || !strings.Contains(err.Error(), "WRONGTYPE") {
		t.Fatalf("Get zset error = %v", err)
	}
	values, err := client.MGet(ctx, "leases", "missing").Result()
	if err != nil || len(values) != 2 || values[0] != nil || values[1] != nil {
		t.Fatalf("MGet zset = %#v, %v", values, err)
	}
	if kind, err := client.Type(ctx, "leases").Result(); err != nil || kind != "zset" {
		t.Fatalf("Type() = %q, %v", kind, err)
	}
	if err := client.Incr(ctx, "leases").Err(); err == nil || !strings.Contains(err.Error(), "WRONGTYPE") {
		t.Fatalf("Incr zset error = %v", err)
	}
	if err := client.Do(ctx, "SET", "leases", "string", "GET").Err(); err == nil || !strings.Contains(err.Error(), "WRONGTYPE") {
		t.Fatalf("Set GET zset error = %v", err)
	}
	if kind, err := client.Type(ctx, "leases").Result(); err != nil || kind != "zset" {
		t.Fatalf("Type() after rejected SET GET = %q, %v", kind, err)
	}
	if removed, err := client.ZRem(ctx, "leases", binaryMember).Result(); err != nil || removed != 1 {
		t.Fatalf("binary ZRem() = %d, %v", removed, err)
	}

	if err := client.ZAdd(ctx, "overwrite", goredis.Z{Score: 1, Member: "a"}).Err(); err != nil {
		t.Fatal(err)
	}
	if err := client.Set(ctx, "overwrite", "string", 0).Err(); err != nil {
		t.Fatal(err)
	}
	if kind, err := client.Type(ctx, "overwrite").Result(); err != nil || kind != "string" {
		t.Fatalf("overwritten Type() = %q, %v", kind, err)
	}
}

func TestGoRedisDirectZAddIsAtomic(t *testing.T) {
	addr, shutdown := startServer(t, "")
	defer shutdown()
	ctx := context.Background()
	client := goredis.NewClient(&goredis.Options{Addr: addr, Protocol: 3})
	t.Cleanup(func() { _ = client.Close() })

	const writers = 50
	errs := make(chan error, writers)
	for i := 0; i < writers; i++ {
		go func(index int) {
			errs <- client.ZAdd(ctx, "leases", goredis.Z{
				Score:  float64(index),
				Member: fmt.Sprintf("member-%d", index),
			}).Err()
		}(i)
	}
	for i := 0; i < writers; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if card, err := client.ZCard(ctx, "leases").Result(); err != nil || card != writers {
		t.Fatalf("ZCard() = %d, %v; want %d", card, err, writers)
	}
}

func TestRedisRejectsUnsupportedAndInvalidTransactionalCommands(t *testing.T) {
	addr, shutdown := startServer(t, "")
	defer shutdown()
	ctx := context.Background()
	client := goredis.NewClient(&goredis.Options{Addr: addr})
	t.Cleanup(func() { _ = client.Close() })

	if err := client.Do(ctx, "HGET", "hash", "field").Err(); err == nil || !strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("unsupported command error = %v", err)
	}
	if err := client.Do(ctx, "SELECT", "1").Err(); err == nil {
		t.Fatal("SELECT 1 error = nil")
	}
	if err := client.Do(ctx, "SET", "key", "value", "NX", "XX").Err(); err == nil {
		t.Fatal("conflicting SET options error = nil")
	}

	pipe := client.TxPipeline()
	pipe.Do(ctx, "SET", "broken")
	pipe.Set(ctx, "must-not-commit", "value", 0)
	_, err := pipe.Exec(ctx)
	if err == nil || !strings.Contains(err.Error(), "EXECABORT") {
		t.Fatalf("transaction error = %v, want EXECABORT", err)
	}
	if _, err := client.Get(ctx, "must-not-commit").Result(); !errors.Is(err, goredis.Nil) {
		t.Fatalf("transaction committed key, Get error = %v", err)
	}
}

func TestRedisRejectsRelativeExpirationThatOverflowsDuration(t *testing.T) {
	addr, shutdown := startServer(t, "")
	defer shutdown()
	ctx := context.Background()
	client := goredis.NewClient(&goredis.Options{Addr: addr})
	t.Cleanup(func() { _ = client.Close() })

	const overflowingSeconds = int64(20_000_000_000)
	if err := client.Do(ctx, "SET", "set-key", "value", "EX", overflowingSeconds).Err(); err == nil {
		t.Fatal("SET EX with overflowing duration error = nil")
	}
	if _, err := client.Get(ctx, "set-key").Result(); !errors.Is(err, goredis.Nil) {
		t.Fatalf("overflowing SET created key, Get error = %v", err)
	}
	if err := client.Set(ctx, "expire-key", "value", 0).Err(); err != nil {
		t.Fatalf("seed key: %v", err)
	}
	if err := client.Do(ctx, "EXPIRE", "expire-key", overflowingSeconds).Err(); err == nil {
		t.Fatal("EXPIRE with overflowing duration error = nil")
	}
	if got, err := client.Get(ctx, "expire-key").Result(); err != nil || got != "value" {
		t.Fatalf("overflowing EXPIRE changed key = %q, %v", got, err)
	}
}

func startServer(t *testing.T, password string) (string, func()) {
	t.Helper()
	ctx := context.Background()
	db, err := dbsqlite.Open(ctx, dbsqlite.Options{Path: filepath.Join(t.TempDir(), "state.db")})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	store, err := statesqlite.New(ctx, db, statesqlite.Options{CleanupInterval: -1})
	if err != nil {
		t.Fatalf("new state store: %v", err)
	}
	server, err := redisserver.New(redisserver.Options{Store: store, Password: password})
	if err != nil {
		t.Fatalf("new Redis server: %v", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()

	return listener.Addr().String(), func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			t.Errorf("server shutdown: %v", err)
		}
		if err := <-serveDone; err != nil {
			t.Errorf("Serve() error = %v", err)
		}
		if err := store.Shutdown(shutdownCtx); err != nil {
			t.Errorf("store shutdown: %v", err)
		}
		if err := db.Close(); err != nil {
			t.Errorf("database close: %v", err)
		}
	}
}

func goredisProtocolName(protocol int) string {
	if protocol == 3 {
		return "RESP3"
	}
	return "RESP2"
}
