package otlphttp

import (
	"context"
	"errors"
	"sync"

	"github.com/nyroway/nyro/go/internal/platform/observe"
)

var (
	errQueueFull   = errors.New("otlphttp: queue full")
	errQueueClosed = errors.New("otlphttp: queue closed")
)

type queuedExport struct {
	request observe.ExportRequest
	bytes   int
}

type exportQueue struct {
	mu         sync.Mutex
	items      []queuedExport
	bytes      int
	maxBatches int
	maxBytes   int
	closed     bool
	notify     chan struct{}
	closedCh   chan struct{}
	closeOnce  sync.Once
}

func newExportQueue(maxBatches, maxBytes int) (*exportQueue, error) {
	if maxBatches <= 0 || maxBytes <= 0 {
		return nil, errors.New("otlphttp: positive queue batch and byte limits are required")
	}
	return &exportQueue{
		items: make([]queuedExport, 0, maxBatches), maxBatches: maxBatches, maxBytes: maxBytes,
		notify: make(chan struct{}, 1), closedCh: make(chan struct{}),
	}, nil
}

func (q *exportQueue) push(item queuedExport) error {
	if item.bytes < 0 {
		return errors.New("otlphttp: queued bytes cannot be negative")
	}
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return errQueueClosed
	}
	if len(q.items) >= q.maxBatches || item.bytes > q.maxBytes-q.bytes {
		q.mu.Unlock()
		return errQueueFull
	}
	q.items = append(q.items, item)
	q.bytes += item.bytes
	q.mu.Unlock()
	select {
	case q.notify <- struct{}{}:
	default:
	}
	return nil
}

func (q *exportQueue) pop(ctx context.Context) (queuedExport, error) {
	for {
		q.mu.Lock()
		if len(q.items) > 0 {
			item := q.items[0]
			q.items[0] = queuedExport{}
			q.items = q.items[1:]
			q.bytes -= item.bytes
			q.mu.Unlock()
			return item, nil
		}
		closed := q.closed
		q.mu.Unlock()
		if closed {
			return queuedExport{}, errQueueClosed
		}
		select {
		case <-ctx.Done():
			return queuedExport{}, ctx.Err()
		case <-q.closedCh:
		case <-q.notify:
		}
	}
}

func (q *exportQueue) size() (int, int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items), q.bytes
}

func (q *exportQueue) close() {
	q.closeOnce.Do(func() {
		q.mu.Lock()
		q.closed = true
		q.mu.Unlock()
		close(q.closedCh)
	})
}
