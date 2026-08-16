package redis

import (
	"context"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/nyroway/nyro/go/internal/quota"
)

func TestExternalRedisAtomicAdmission(t *testing.T) {
	rawURL := os.Getenv("NYRO_TEST_REDIS_URL")
	if rawURL == "" {
		t.Skip("NYRO_TEST_REDIS_URL is not set")
	}
	options, err := goredis.ParseURL(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	clientA := goredis.NewClient(options)
	clientB := goredis.NewClient(options)
	t.Cleanup(func() {
		_ = clientA.Close()
		_ = clientB.Close()
	})

	now := time.Now().Truncate(time.Minute)
	fixedNow := func() time.Time { return now }
	storeA, err := New(clientA, Options{Now: fixedNow})
	if err != nil {
		t.Fatal(err)
	}
	storeB, err := New(clientB, Options{Now: fixedNow})
	if err != nil {
		t.Fatal(err)
	}
	if err := storeA.Probe(context.Background()); err != nil {
		t.Fatal(err)
	}

	consumerID := "external-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	t.Cleanup(func() { deleteCurrentQuotaKeys(t, clientA, consumerID, now) })
	const attempts = 100
	const limit = 23
	var admitted atomic.Int64
	errorsCh := make(chan error, attempts)
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			store := storeA
			if index%2 == 1 {
				store = storeB
			}
			allowed, err := store.AdmitRequest(context.Background(), consumerID, []quota.RequestLimit{{Limit: limit, Window: time.Minute}})
			if err != nil {
				errorsCh <- err
				return
			}
			if allowed {
				admitted.Add(1)
			}
		}(i)
	}
	wg.Wait()
	close(errorsCh)
	for err := range errorsCh {
		t.Error(err)
	}
	if got := admitted.Load(); got != limit {
		t.Fatalf("admitted = %d, want %d", got, limit)
	}
	allowed, err := storeA.AdmitRequest(context.Background(), consumerID, []quota.RequestLimit{{Limit: limit, Window: time.Minute}})
	if err != nil || allowed {
		t.Fatalf("post-limit admission = %v, %v", allowed, err)
	}
}

func deleteCurrentQuotaKeys(t *testing.T, client *goredis.Client, consumerID string, now time.Time) {
	t.Helper()
	currentMinute := now.Unix() / int64(time.Minute/time.Second)
	keys := []string{
		usageKey(consumerID, "requests", "m", currentMinute),
		usageKey(consumerID, "requests", "h", floorHour(currentMinute)),
		concurrencyKey(consumerID),
	}
	if err := client.Del(context.Background(), keys...).Err(); err != nil {
		t.Errorf("delete external quota keys: %v", err)
	}
}
