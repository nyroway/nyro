package httpingress_test

import (
	"net/http"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/nyroway/nyro/go/internal/quota"
	"github.com/nyroway/nyro/go/internal/storage"
)

func TestRequestQuotaConcurrentAdmissionIsExact(t *testing.T) {
	store := quota.NewMemory()
	engine, key, _, upstreamCalls := newQuotaTestSourceWithQuotas(t, store, []storage.CreateConsumerQuota{{
		QuotaType:  "requests",
		QuotaLimit: 7,
		Window:     "1m",
	}})

	const attempts = 40
	start := make(chan struct{})
	statuses := make(chan int, attempts)
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			statuses <- makeQuotaRequest(engine, key).Code
		}()
	}
	close(start)
	wg.Wait()
	close(statuses)

	var okResponses atomic.Int64
	var limitedResponses atomic.Int64
	for status := range statuses {
		switch status {
		case http.StatusOK:
			okResponses.Add(1)
		case http.StatusTooManyRequests:
			limitedResponses.Add(1)
		default:
			t.Errorf("unexpected status = %d", status)
		}
	}
	if got := upstreamCalls.Load(); got != 7 {
		t.Fatalf("upstream calls = %d, want 7", got)
	}
	if okResponses.Load() != 7 || limitedResponses.Load() != 33 {
		t.Fatalf("responses: 200=%d 429=%d; want 7 and 33", okResponses.Load(), limitedResponses.Load())
	}
}
