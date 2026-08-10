package redis_test

import (
	"context"
	"errors"
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
