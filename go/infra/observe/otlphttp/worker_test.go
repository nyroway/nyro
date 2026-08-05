package otlphttp_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/nyroway/nyro/go/infra/observe"
	"github.com/nyroway/nyro/go/infra/observe/otlphttp"
)

type batchStore struct {
	mu      sync.Mutex
	batches []int
	err     error
	started chan struct{}
	release chan struct{}
}

func (s *batchStore) Append(ctx context.Context, requests []observe.ExportRequest) error {
	if s.started != nil {
		select {
		case s.started <- struct{}{}:
		default:
		}
	}
	if s.release != nil {
		select {
		case <-s.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	s.mu.Lock()
	s.batches = append(s.batches, len(requests))
	s.mu.Unlock()
	return s.err
}

func (s *batchStore) QueryLogs(context.Context, observe.LogQuery) (observe.LogPage, error) {
	return observe.LogPage{}, nil
}

func (s *batchStore) QuerySpans(context.Context, observe.SpanQuery) (observe.SpanPage, error) {
	return observe.SpanPage{}, nil
}

func (s *batchStore) QueryMetrics(context.Context, observe.MetricQuery) (observe.MetricPage, error) {
	return observe.MetricPage{}, nil
}

func (s *batchStore) DeleteBefore(context.Context, observe.Signal, time.Time, int) (int64, error) {
	return 0, nil
}

func (s *batchStore) snapshot() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int(nil), s.batches...)
}

func TestWorkerBatchesRequestsAndShutdownDrains(t *testing.T) {
	store := &batchStore{}
	receiver, err := otlphttp.New(otlphttp.Options{Store: store, FlushInterval: 10 * time.Millisecond, FlushBatch: 2})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	for range 3 {
		response := sendEmptyLog(receiver)
		if response.Code != http.StatusOK {
			t.Fatalf("status = %d", response.Code)
		}
	}
	if err := receiver.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	if got := store.snapshot(); len(got) != 2 || got[0] != 2 || got[1] != 1 {
		t.Fatalf("persisted batches = %v, want [2 1]", got)
	}
	stats := receiver.Stats()
	if stats.AcceptedBatches != 3 || stats.PersistedBatches != 3 || stats.QueuedBatches != 0 {
		t.Fatalf("Stats() = %#v", stats)
	}
}

func TestReceiverReturnsRetryable503WhenQueueIsFull(t *testing.T) {
	store := &batchStore{started: make(chan struct{}, 1), release: make(chan struct{})}
	receiver, err := otlphttp.New(otlphttp.Options{
		Store: store, QueueMaxBatches: 1, QueueMaxBytes: 1024, FlushInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if response := sendEmptyLog(receiver); response.Code != http.StatusOK {
		t.Fatalf("first status = %d", response.Code)
	}
	select {
	case <-store.started:
	case <-time.After(time.Second):
		t.Fatal("worker did not start persistence")
	}
	if response := sendEmptyLog(receiver); response.Code != http.StatusOK {
		t.Fatalf("second status = %d", response.Code)
	}
	response := sendEmptyLog(receiver)
	if response.Code != http.StatusServiceUnavailable || response.Header().Get("Retry-After") == "" {
		t.Fatalf("saturated response = %d, headers = %v", response.Code, response.Header())
	}
	close(store.release)
	if err := receiver.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	if receiver.Stats().RejectedBatches != 1 {
		t.Fatalf("Stats() = %#v", receiver.Stats())
	}
}

func TestPersistenceFailureAfterAcknowledgementIsCounted(t *testing.T) {
	store := &batchStore{err: errors.New("disk full")}
	receiver, err := otlphttp.New(otlphttp.Options{Store: store, FlushInterval: time.Millisecond})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	response := sendEmptyLog(receiver)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	waitFor(t, time.Second, func() bool { return receiver.Stats().FailedBatches == 1 })
	if err := receiver.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
}

func TestShutdownHonorsDeadlineWhenPersistenceBlocks(t *testing.T) {
	store := &batchStore{started: make(chan struct{}, 1), release: make(chan struct{})}
	receiver, err := otlphttp.New(otlphttp.Options{Store: store, FlushInterval: time.Millisecond})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	_ = sendEmptyLog(receiver)
	select {
	case <-store.started:
	case <-time.After(time.Second):
		t.Fatal("worker did not start persistence")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := receiver.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown() error = %v, want deadline exceeded", err)
	}
	close(store.release)
}

func sendEmptyLog(receiver *otlphttp.Receiver) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/v1/logs", bytes.NewReader(nil))
	request.Header.Set("Content-Type", "application/x-protobuf")
	response := httptest.NewRecorder()
	receiver.Handler().ServeHTTP(response, request)
	return response
}
