// Package quota defines State-backed usage and concurrency accounting.
//
// Layer: 0 (foundation). It must not import other Nyro internal packages.
package quota

import (
	"context"
	"errors"
	"sync"
	"time"
)

const ringSize = int(MaxWindow / BucketResolution)

type bucket struct {
	value int64
}

type ring struct {
	epoch   int64
	buckets [ringSize]bucket
}

func indexFor(t time.Time) int64 {
	return t.UnixNano() / int64(BucketResolution)
}

func (r *ring) advance(target int64) {
	if target <= r.epoch {
		return
	}
	if target-r.epoch >= int64(len(r.buckets)) {
		for i := range r.buckets {
			r.buckets[i] = bucket{}
		}
		r.epoch = target
		return
	}
	for r.epoch < target {
		r.epoch++
		r.buckets[int(r.epoch)%len(r.buckets)] = bucket{}
	}
}

func (r *ring) add(value int64) {
	r.buckets[int(r.epoch)%len(r.buckets)].value += value
}

func (r *ring) sumWindow(window time.Duration) int64 {
	count := int(window / BucketResolution)
	if count <= 0 {
		count = 1
	}
	if count > len(r.buckets) {
		count = len(r.buckets)
	}
	var total int64
	for i := 0; i < count; i++ {
		index := int(r.epoch-int64(i)) % len(r.buckets)
		if index < 0 {
			index += len(r.buckets)
		}
		total += r.buckets[index].value
	}
	return total
}

type quotaKey struct {
	consumerID string
	quotaType  string
}

// Memory is a process-local Store with minute-resolution sliding windows.
type Memory struct {
	mu       sync.Mutex
	rings    map[quotaKey]*ring
	inflight map[string]int64
	now      func() time.Time
}

// NewMemory returns a ready process-local quota Store.
func NewMemory() *Memory {
	return &Memory{
		rings:    make(map[quotaKey]*ring),
		inflight: make(map[string]int64),
		now:      time.Now,
	}
}

// TokenValue returns consumerID's completed token total within the trailing window.
func (m *Memory) TokenValue(ctx context.Context, consumerID string, window time.Duration) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	key := quotaKey{consumerID: consumerID, quotaType: "tokens"}
	valueRing, ok := m.rings[key]
	if !ok {
		return 0, nil
	}
	valueRing.advance(indexFor(m.now()))
	return valueRing.sumWindow(window), nil
}

// RecordTokens adds completed token usage for one exchange.
func (m *Memory) RecordTokens(ctx context.Context, consumerID string, tokens int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}

	if tokens != 0 {
		m.addLocked(consumerID, "tokens", tokens, indexFor(m.now()))
	}
	return nil
}

// AdmitRequest atomically checks every request window and counts one request
// only when all limits allow it.
func (m *Memory) AdmitRequest(ctx context.Context, consumerID string, limits []RequestLimit) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if len(limits) == 0 {
		return true, nil
	}
	for _, limit := range limits {
		if limit.Limit <= 0 || limit.Window <= 0 {
			return false, errors.New("quota: request limit and window must be positive")
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return false, err
	}

	now := indexFor(m.now())
	requests := m.ringLocked(consumerID, "requests")
	requests.advance(now)
	for _, limit := range limits {
		if requests.sumWindow(limit.Window) >= limit.Limit {
			return false, nil
		}
	}
	requests.add(1)
	return true, nil
}

func (m *Memory) addLocked(consumerID, quotaType string, amount, now int64) {
	valueRing := m.ringLocked(consumerID, quotaType)
	valueRing.advance(now)
	valueRing.add(amount)
}

// Acquire reserves a concurrency slot when consumerID is below limit.
func (m *Memory) Acquire(ctx context.Context, consumerID string, limit int64, _ time.Duration) (Lease, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if m.inflight[consumerID] >= limit {
		return nil, false, nil
	}
	m.inflight[consumerID]++
	return &memoryLease{store: m, consumerID: consumerID}, true, nil
}

type memoryLease struct {
	store      *Memory
	consumerID string
	once       sync.Once
}

func (l *memoryLease) Release(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	l.once.Do(func() {
		l.store.mu.Lock()
		defer l.store.mu.Unlock()
		if count := l.store.inflight[l.consumerID]; count > 1 {
			l.store.inflight[l.consumerID] = count - 1
		} else {
			delete(l.store.inflight, l.consumerID)
		}
	})
	return nil
}

func (m *Memory) ringLocked(consumerID, quotaType string) *ring {
	key := quotaKey{consumerID: consumerID, quotaType: quotaType}
	valueRing, ok := m.rings[key]
	if !ok {
		valueRing = &ring{}
		m.rings[key] = valueRing
	}
	return valueRing
}

// GC drops rings whose full history has expired.
func (m *Memory) GC() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := indexFor(m.now())
	var removed int
	for key, valueRing := range m.rings {
		valueRing.advance(now)
		if valueRing.sumWindow(MaxWindow) == 0 {
			delete(m.rings, key)
			removed++
		}
	}
	return removed
}

// ParseWindow parses a quota window and extends time.ParseDuration with days.
func ParseWindow(raw string) (time.Duration, error) {
	if length := len(raw); length > 1 && raw[length-1] == 'd' {
		hours, err := time.ParseDuration(raw[:length-1] + "h")
		if err != nil {
			return 0, err
		}
		return hours * 24, nil
	}
	return time.ParseDuration(raw)
}
