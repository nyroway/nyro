package redis

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/nyroway/nyro/go/internal/quota"
)

func TestRedisAdmitRequestIsExactAcrossClients(t *testing.T) {
	addr, shutdown := startEmbeddedRedis(t)
	defer shutdown()
	clientA := newClient(t, addr)
	clientB := newClient(t, addr)
	clock := &storeClock{now: time.Unix(3_000_000, 0).Truncate(time.Minute)}
	storeA := newStore(t, clientA, clock.Now)
	storeB := newStore(t, clientB, clock.Now)

	const attempts = 80
	const limit = 13
	var allowed atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			store := storeA
			if index%2 == 1 {
				store = storeB
			}
			ok, err := store.AdmitRequest(context.Background(), "consumer", []quota.RequestLimit{{Limit: limit, Window: time.Minute}})
			if err != nil {
				t.Error(err)
				return
			}
			if ok {
				allowed.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if got := allowed.Load(); got != limit {
		t.Fatalf("allowed = %d, want %d", got, limit)
	}
}

func TestRedisAdmitRequestChecksMultipleWindows(t *testing.T) {
	store, clock := newEmbeddedStoreWithClock(t)
	limits := []quota.RequestLimit{
		{Limit: 2, Window: time.Minute},
		{Limit: 3, Window: time.Hour},
	}
	for i := 0; i < 2; i++ {
		allowed, err := store.AdmitRequest(context.Background(), "consumer", limits)
		if err != nil || !allowed {
			t.Fatalf("admission %d = %v, %v", i, allowed, err)
		}
	}
	clock.now = clock.now.Add(time.Minute)
	allowed, err := store.AdmitRequest(context.Background(), "consumer", limits)
	if err != nil || !allowed {
		t.Fatalf("third admission = %v, %v", allowed, err)
	}
	allowed, err = store.AdmitRequest(context.Background(), "consumer", limits)
	if err != nil || allowed {
		t.Fatalf("hour denial = %v, %v", allowed, err)
	}
}

func TestRedisAdmitRequestDenialDoesNotIncrement(t *testing.T) {
	store, clock := newEmbeddedStoreWithClock(t)
	limits := []quota.RequestLimit{{Limit: 1, Window: time.Minute}, {Limit: 2, Window: time.Hour}}
	if allowed, err := store.AdmitRequest(context.Background(), "consumer", limits); err != nil || !allowed {
		t.Fatalf("first admission = %v, %v", allowed, err)
	}
	for i := 0; i < 10; i++ {
		if allowed, err := store.AdmitRequest(context.Background(), "consumer", limits); err != nil || allowed {
			t.Fatalf("denial %d = %v, %v", i, allowed, err)
		}
	}
	clock.now = clock.now.Add(time.Minute)
	allowed, err := store.AdmitRequest(context.Background(), "consumer", limits)
	if err != nil || !allowed {
		t.Fatalf("admission after denials = %v, %v; denials consumed quota", allowed, err)
	}
}

func TestRedisAdmitRequestRejectsMalformedCounter(t *testing.T) {
	store, client, clock := newEmbeddedStoreClientAndClock(t)
	currentMinute := clock.now.Unix() / int64(time.Minute/time.Second)
	key := usageKey("consumer", "requests", "m", currentMinute)
	if err := client.Set(context.Background(), key, "not-an-integer", time.Hour).Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AdmitRequest(context.Background(), "consumer", []quota.RequestLimit{{Limit: 2, Window: time.Minute}}); err == nil {
		t.Fatal("AdmitRequest() error = nil")
	}
}

func TestRedisAdmitRequestWrongTypeDoesNotPartiallyCommit(t *testing.T) {
	store, client, clock := newEmbeddedStoreClientAndClock(t)
	currentMinute := clock.now.Unix() / int64(time.Minute/time.Second)
	minuteKey := usageKey("consumer", "requests", "m", currentMinute)
	hourKey := usageKey("consumer", "requests", "h", floorHour(currentMinute))
	if err := client.ZAdd(context.Background(), minuteKey, goredis.Z{Score: 1, Member: "lease"}).Err(); err != nil {
		t.Fatal(err)
	}

	allowed, err := store.AdmitRequest(context.Background(), "consumer", []quota.RequestLimit{{Limit: 2, Window: time.Minute}})
	if err == nil || allowed {
		t.Fatalf("AdmitRequest() = %v, %v; want false and WRONGTYPE error", allowed, err)
	}
	count, existsErr := client.Exists(context.Background(), hourKey).Result()
	if existsErr != nil || count != 0 {
		t.Fatalf("hour counter exists after failed admission = %d, %v", count, existsErr)
	}
}

func TestRedisAdmitRequestHonorsCanceledContext(t *testing.T) {
	store, _ := newEmbeddedStoreWithClock(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	allowed, err := store.AdmitRequest(ctx, "consumer", []quota.RequestLimit{{Limit: 1, Window: time.Minute}})
	if !errors.Is(err, context.Canceled) || allowed {
		t.Fatalf("AdmitRequest() = %v, %v; want false, context.Canceled", allowed, err)
	}
}

func TestRedisAdmitRequestRejectsInvalidLimit(t *testing.T) {
	store, _ := newEmbeddedStoreWithClock(t)
	allowed, err := store.AdmitRequest(context.Background(), "consumer", []quota.RequestLimit{{Limit: 0, Window: time.Minute}})
	if err == nil || allowed {
		t.Fatalf("AdmitRequest() = %v, %v; want false and error", allowed, err)
	}
}

func TestRetryAdmissionStopsAtConfiguredConflictLimit(t *testing.T) {
	attempts := 0
	_, err := retryAdmission(3, func() (bool, error) {
		attempts++
		return false, goredis.TxFailedErr
	})
	if !errors.Is(err, quota.ErrAdmissionContended) || attempts != 3 {
		t.Fatalf("retry result = attempts %d, error %v", attempts, err)
	}
}

func TestRedisStoreRejectsNegativeAdmissionRetries(t *testing.T) {
	addr, shutdown := startEmbeddedRedis(t)
	defer shutdown()
	if _, err := New(newClient(t, addr), Options{MaxAdmissionRetries: -1}); err == nil {
		t.Fatal("New() error = nil")
	}
}

func newEmbeddedStoreWithClock(t *testing.T) (*Store, *storeClock) {
	t.Helper()
	store, _, clock := newEmbeddedStoreClientAndClock(t)
	return store, clock
}

func newEmbeddedStoreClientAndClock(t *testing.T) (*Store, *goredis.Client, *storeClock) {
	t.Helper()
	addr, _ := startEmbeddedRedis(t)
	client := newClient(t, addr)
	clock := &storeClock{now: time.Unix(3_000_000, 0).Truncate(time.Minute)}
	return newStore(t, client, clock.Now), client, clock
}
