// Package queue implements the receiver's in-memory bounded FIFO.
package queue

import (
	"context"
	"errors"
	"sync"
)

var (
	ErrFull   = errors.New("observe queue: full")
	ErrClosed = errors.New("observe queue: closed")
)

// Item is one queued export and its accounted encoded size.
type Item struct {
	Value any
	Bytes int
}

// Queue is bounded independently by item count and total accounted bytes.
type Queue struct {
	mu         sync.Mutex
	items      []Item
	bytes      int
	maxBatches int
	maxBytes   int
	closed     bool
	notify     chan struct{}
	closedCh   chan struct{}
	closeOnce  sync.Once
}

// New constructs a bounded queue.
func New(maxBatches, maxBytes int) (*Queue, error) {
	if maxBatches <= 0 || maxBytes <= 0 {
		return nil, errors.New("observe queue: positive batch and byte limits are required")
	}
	return &Queue{
		items: make([]Item, 0, maxBatches), maxBatches: maxBatches, maxBytes: maxBytes,
		notify: make(chan struct{}, 1), closedCh: make(chan struct{}),
	}, nil
}

// Push appends an item without blocking.
func (q *Queue) Push(item Item) error {
	if item.Bytes < 0 {
		return errors.New("observe queue: item bytes cannot be negative")
	}
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return ErrClosed
	}
	if len(q.items) >= q.maxBatches || item.Bytes > q.maxBytes-q.bytes {
		q.mu.Unlock()
		return ErrFull
	}
	q.items = append(q.items, item)
	q.bytes += item.Bytes
	q.mu.Unlock()
	select {
	case q.notify <- struct{}{}:
	default:
	}
	return nil
}

// Pop blocks until the oldest item, queue closure, or context cancellation.
func (q *Queue) Pop(ctx context.Context) (Item, error) {
	for {
		q.mu.Lock()
		if len(q.items) > 0 {
			item := q.items[0]
			q.items[0] = Item{}
			q.items = q.items[1:]
			q.bytes -= item.Bytes
			q.mu.Unlock()
			return item, nil
		}
		closed := q.closed
		q.mu.Unlock()
		if closed {
			return Item{}, ErrClosed
		}
		select {
		case <-ctx.Done():
			return Item{}, ctx.Err()
		case <-q.closedCh:
		case <-q.notify:
		}
	}
}

// Drain removes up to limit currently buffered items without blocking. A
// non-positive limit drains all buffered items.
func (q *Queue) Drain(limit int) []Item {
	q.mu.Lock()
	defer q.mu.Unlock()
	if limit <= 0 || limit > len(q.items) {
		limit = len(q.items)
	}
	items := append([]Item(nil), q.items[:limit]...)
	for _, item := range items {
		q.bytes -= item.Bytes
	}
	clear(q.items[:limit])
	q.items = q.items[limit:]
	return items
}

// Size reports buffered item and byte counts.
func (q *Queue) Size() (int, int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items), q.bytes
}

// Close prevents future pushes while allowing buffered items to be popped.
func (q *Queue) Close() {
	q.closeOnce.Do(func() {
		q.mu.Lock()
		q.closed = true
		q.mu.Unlock()
		close(q.closedCh)
	})
}
