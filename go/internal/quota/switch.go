package quota

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Switch is a stable Store facade whose backend can be replaced atomically.
type Switch struct {
	mu         sync.Mutex
	current    *switchEntry
	generation uint64
	closed     bool
}

type switchEntry struct {
	store       Store
	generation  uint64
	healthy     bool
	refs        int64
	retired     bool
	retiredDone bool
	retire      func()
	forceTimer  *time.Timer
}

// NewSwitch returns a ready facade for initial. A nil Store is unavailable.
func NewSwitch(initial Store) *Switch {
	sw := &Switch{}
	if initial != nil {
		sw.generation = 1
		sw.current = &switchEntry{store: initial, generation: 1, healthy: true}
	}
	return sw
}

// NewUnavailableSwitch returns a facade that fails closed until Swap.
func NewUnavailableSwitch() *Switch {
	return &Switch{}
}

// Ready reports whether the current backend may serve quota operations.
func (s *Switch) Ready() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.closed && s.current != nil && s.current.healthy
}

// Swap publishes next and retires the previous backend after its references
// drain. retire closes resources owned by the previous backend.
func (s *Switch) Swap(next Store, retire func(), forceAfter time.Duration) uint64 {
	s.mu.Lock()
	if s.closed {
		generation := s.generation
		s.mu.Unlock()
		return generation
	}

	previous := s.current
	s.generation++
	generation := s.generation
	if next == nil {
		s.current = nil
	} else {
		s.current = &switchEntry{store: next, generation: generation, healthy: true}
	}

	var runRetire func()
	if previous != nil {
		previous.retired = true
		previous.retire = retire
		runRetire = s.retireIfDrainedLocked(previous)
		if runRetire == nil && !previous.retiredDone && forceAfter > 0 {
			previous.forceTimer = time.AfterFunc(forceAfter, func() {
				s.forceRetire(previous)
			})
		}
	}
	s.mu.Unlock()
	runCallback(runRetire)
	return generation
}

// MarkHealthy changes health only when generation is still current.
func (s *Switch) MarkHealthy(generation uint64, healthy bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.current == nil || s.current.generation != generation {
		return
	}
	s.current.healthy = healthy
}

// Shutdown permanently makes the facade unavailable.
func (s *Switch) Shutdown() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	previous := s.current
	s.current = nil
	var runRetire func()
	if previous != nil {
		previous.retired = true
		runRetire = s.retireIfDrainedLocked(previous)
	}
	s.mu.Unlock()
	runCallback(runRetire)
}

func (s *Switch) TokenValue(ctx context.Context, consumerID string, window time.Duration) (int64, error) {
	entry, err := s.reference()
	if err != nil {
		return 0, err
	}
	value, callErr := entry.store.TokenValue(ctx, consumerID, window)
	s.finish(entry, callErr)
	return value, callErr
}

func (s *Switch) AdmitRequest(ctx context.Context, consumerID string, limits []RequestLimit) (bool, error) {
	entry, err := s.reference()
	if err != nil {
		return false, err
	}
	allowed, callErr := entry.store.AdmitRequest(ctx, consumerID, limits)
	s.finish(entry, callErr)
	return allowed, callErr
}

func (s *Switch) RecordTokens(ctx context.Context, consumerID string, tokens int64) error {
	entry, err := s.reference()
	if err != nil {
		return err
	}
	callErr := entry.store.RecordTokens(ctx, consumerID, tokens)
	s.finish(entry, callErr)
	return callErr
}

func (s *Switch) Acquire(ctx context.Context, consumerID string, limit int64, leaseTTL time.Duration) (Lease, bool, error) {
	entry, err := s.reference()
	if err != nil {
		return nil, false, err
	}
	lease, allowed, callErr := entry.store.Acquire(ctx, consumerID, limit, leaseTTL)
	if callErr != nil {
		s.finish(entry, callErr)
		return nil, false, callErr
	}
	if !allowed {
		s.finish(entry, nil)
		return nil, false, nil
	}
	if lease == nil {
		callErr = fmt.Errorf("quota backend returned an empty lease")
		s.finish(entry, callErr)
		return nil, false, callErr
	}
	return &switchLease{owner: s, entry: entry, inner: lease}, true, nil
}

func (s *Switch) reference() (*switchEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.current == nil || !s.current.healthy {
		return nil, ErrUnavailable
	}
	s.current.refs++
	return s.current, nil
}

func (s *Switch) finish(entry *switchEntry, operationErr error) {
	s.mu.Lock()
	if isBackendFailure(operationErr) {
		entry.healthy = false
	}
	if entry.refs > 0 {
		entry.refs--
	}
	runRetire := s.retireIfDrainedLocked(entry)
	s.mu.Unlock()
	runCallback(runRetire)
}

func isBackendFailure(err error) bool {
	return err != nil &&
		!errors.Is(err, context.Canceled) &&
		!errors.Is(err, ErrAdmissionContended)
}

func (s *Switch) retireIfDrainedLocked(entry *switchEntry) func() {
	if entry == nil || !entry.retired || entry.retiredDone || entry.refs != 0 {
		return nil
	}
	entry.retiredDone = true
	if entry.forceTimer != nil {
		entry.forceTimer.Stop()
		entry.forceTimer = nil
	}
	return entry.retire
}

func (s *Switch) forceRetire(entry *switchEntry) {
	s.mu.Lock()
	if entry.retiredDone {
		s.mu.Unlock()
		return
	}
	entry.retiredDone = true
	entry.forceTimer = nil
	runRetire := entry.retire
	s.mu.Unlock()
	runCallback(runRetire)
}

func runCallback(callback func()) {
	if callback != nil {
		callback()
	}
}

type switchLease struct {
	owner *Switch
	entry *switchEntry
	inner Lease
	once  sync.Once
	err   error
}

func (l *switchLease) Release(ctx context.Context) error {
	l.once.Do(func() {
		l.err = l.inner.Release(ctx)
		l.owner.finish(l.entry, l.err)
	})
	return l.err
}
