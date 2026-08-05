// Package otlphttp implements a lightweight embedded OTLP/HTTP protobuf receiver.
//
// It supports the standard /v1/logs, /v1/metrics, and /v1/traces endpoints.
// Accepted requests are acknowledged after entering a bounded in-memory queue;
// persistence is asynchronous and observable through Receiver.Stats.
package otlphttp

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nyroway/nyro/go/infra/observe"
)

const (
	defaultMaxRequestBytes = 64 << 20
	defaultQueueMaxBytes   = 64 << 20
	defaultQueueMaxBatches = 256
	defaultFlushInterval   = 200 * time.Millisecond
	defaultFlushBatch      = 64
)

// Options configures an OTLP/HTTP receiver.
type Options struct {
	Store           observe.Store
	MaxRequestBytes int64
	QueueMaxBytes   int
	QueueMaxBatches int
	FlushInterval   time.Duration
	FlushBatch      int
}

// Stats is a point-in-time receiver reliability snapshot.
type Stats struct {
	AcceptedBatches  uint64
	AcceptedBytes    uint64
	PersistedBatches uint64
	FailedBatches    uint64
	RejectedBatches  uint64
	QueuedBatches    int
	QueuedBytes      int
}

// Receiver accepts OTLP/HTTP protobuf requests and asynchronously persists them.
type Receiver struct {
	store           observe.Store
	queue           *exportQueue
	maxRequestBytes int64
	flushInterval   time.Duration
	flushBatch      int
	cancel          context.CancelFunc
	done            chan struct{}
	stop            sync.Once
	acceptedBatches atomic.Uint64
	acceptedBytes   atomic.Uint64
	persisted       atomic.Uint64
	failed          atomic.Uint64
	rejected        atomic.Uint64
}

// New starts a receiver worker. The caller owns the supplied Store.
func New(opts Options) (*Receiver, error) {
	if opts.Store == nil {
		return nil, errors.New("otlphttp: store is required")
	}
	if opts.MaxRequestBytes == 0 {
		opts.MaxRequestBytes = defaultMaxRequestBytes
	}
	if opts.QueueMaxBytes == 0 {
		opts.QueueMaxBytes = defaultQueueMaxBytes
	}
	if opts.QueueMaxBatches == 0 {
		opts.QueueMaxBatches = defaultQueueMaxBatches
	}
	if opts.FlushInterval == 0 {
		opts.FlushInterval = defaultFlushInterval
	}
	if opts.FlushBatch == 0 {
		opts.FlushBatch = defaultFlushBatch
	}
	if opts.MaxRequestBytes < 0 || opts.QueueMaxBytes < 0 || opts.QueueMaxBatches < 0 || opts.FlushInterval < 0 || opts.FlushBatch < 0 {
		return nil, errors.New("otlphttp: limits and intervals must be positive")
	}
	q, err := newExportQueue(opts.QueueMaxBatches, opts.QueueMaxBytes)
	if err != nil {
		return nil, err
	}
	workerContext, cancel := context.WithCancel(context.Background())
	r := &Receiver{
		store: opts.Store, queue: q, maxRequestBytes: opts.MaxRequestBytes,
		flushInterval: opts.FlushInterval, flushBatch: opts.FlushBatch,
		cancel: cancel, done: make(chan struct{}),
	}
	go r.run(workerContext)
	return r, nil
}

// Handler returns an HTTP handler for the three standard OTLP paths.
func (r *Receiver) Handler() http.Handler { return http.HandlerFunc(r.serveHTTP) }

// Stats returns counters and current queue occupancy.
func (r *Receiver) Stats() Stats {
	batches, bytes := r.queue.size()
	return Stats{
		AcceptedBatches: r.acceptedBatches.Load(), AcceptedBytes: r.acceptedBytes.Load(),
		PersistedBatches: r.persisted.Load(), FailedBatches: r.failed.Load(), RejectedBatches: r.rejected.Load(),
		QueuedBatches: batches, QueuedBytes: bytes,
	}
}

// Shutdown stops accepting work, drains queued exports, and does not close Store.
func (r *Receiver) Shutdown(ctx context.Context) error {
	r.stop.Do(r.queue.close)
	select {
	case <-r.done:
		return nil
	case <-ctx.Done():
		r.cancel()
		return ctx.Err()
	}
}
