package kernel

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

var errHostClosed = errors.New("kernel host is closed")

// Host owns one active typed runtime generation and any generations draining leases.
type Host[T any] struct {
	activateGate chan struct{}

	mu         sync.Mutex
	active     *generation[T]
	retiring   map[*generation[T]]struct{}
	closing    bool
	closeDone  chan struct{}
	closeErr   error
	closeFinal bool
	changed    chan struct{}
	retireCtx  context.Context
	retireStop context.CancelFunc
}

// NewHost constructs an empty, not-ready runtime host.
func NewHost[T any]() *Host[T] {
	ctx, cancel := context.WithCancel(context.Background())
	host := &Host[T]{
		activateGate: make(chan struct{}, 1),
		retiring:     make(map[*generation[T]]struct{}),
		changed:      make(chan struct{}),
		retireCtx:    ctx,
		retireStop:   cancel,
	}
	host.activateGate <- struct{}{}
	return host
}

// Activate validates and starts a candidate before atomically publishing it.
func (h *Host[T]) Activate(ctx context.Context, candidate Candidate[T]) (Activation, error) {
	ordered, err := orderComponents(candidate.Components)
	if err != nil {
		return Activation{}, err
	}

	if err := ctx.Err(); err != nil {
		return Activation{}, err
	}
	h.mu.Lock()
	closed := h.closing
	h.mu.Unlock()
	if closed {
		return Activation{}, errHostClosed
	}
	select {
	case <-h.activateGate:
		defer func() { h.activateGate <- struct{}{} }()
	case <-ctx.Done():
		return Activation{}, ctx.Err()
	}

	h.mu.Lock()
	closed = h.closing
	h.mu.Unlock()
	if closed {
		return Activation{}, errHostClosed
	}

	started := make([]Component, 0, len(ordered))
	for _, component := range ordered {
		started = append(started, component)
		if startErr := component.Lifecycle.Start(ctx); startErr != nil {
			rollbackErr := closeComponents(ctx, started)
			return Activation{}, errors.Join(
				fmt.Errorf("start component %q: %w", component.ID, startErr),
				rollbackErr,
			)
		}
	}

	next := &generation[T]{
		version:     candidate.Version,
		fingerprint: candidate.Fingerprint,
		value:       candidate.Value,
		components:  ordered,
	}

	h.mu.Lock()
	if h.closing {
		h.mu.Unlock()
		rollbackErr := closeComponents(ctx, started)
		return Activation{}, errors.Join(errHostClosed, rollbackErr)
	}
	previous := h.active
	h.active = next
	if previous != nil {
		h.markRetiringLocked(previous)
	}
	h.notifyLocked()
	h.mu.Unlock()

	if previous != nil {
		h.startRetirementIfReady(previous)
	}
	return Activation{Changed: true, Version: candidate.Version}, nil
}

// Acquire leases the currently active generation.
func (h *Host[T]) Acquire() (*Lease[T], bool) {
	if h == nil {
		return nil, false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closing || h.active == nil {
		return nil, false
	}
	h.active.leases++
	return &Lease[T]{state: &leaseState[T]{host: h, generation: h.active}}, true
}

// Ready reports whether the host accepts leases for an active generation.
func (h *Host[T]) Ready() bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return !h.closing && h.active != nil
}

// Status returns active identity and the number of draining generations.
func (h *Host[T]) Status() Status {
	if h == nil {
		return Status{}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	status := Status{Ready: !h.closing && h.active != nil, Retiring: len(h.retiring)}
	if h.active != nil {
		status.ActiveVersion = h.active.version
		status.ActiveFingerprint = h.active.fingerprint
	}
	return status
}

// Close stops new leases, drains current generations, and closes their components.
func (h *Host[T]) Close(ctx context.Context) error {
	if h == nil {
		return nil
	}

	h.mu.Lock()
	if h.closing {
		done := h.closeDone
		h.mu.Unlock()
		select {
		case <-done:
			h.mu.Lock()
			err := h.closeErr
			h.mu.Unlock()
			return err
		default:
		}
		select {
		case <-done:
			h.mu.Lock()
			err := h.closeErr
			h.mu.Unlock()
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	h.closing = true
	h.closeDone = make(chan struct{})
	if h.active != nil {
		h.markRetiringLocked(h.active)
		h.active = nil
	}
	h.notifyLocked()
	h.mu.Unlock()

	// Retire every lease-free published generation while an in-progress candidate
	// observes shutdown. Candidate startup must not consume the drain grace period.
	h.startReadyRetirements()

	// Wait for an in-progress activation to observe closing and roll itself back,
	// but never beyond the shutdown deadline.
	select {
	case <-h.activateGate:
		h.activateGate <- struct{}{}
	case <-ctx.Done():
		return h.closeAfterDeadline(ctx)
	}

	for {
		h.mu.Lock()
		if len(h.retiring) == 0 {
			err := h.closeErr
			h.finishCloseLocked(err)
			h.mu.Unlock()
			h.retireStop()
			return err
		}
		changed := h.changed
		h.mu.Unlock()

		select {
		case <-changed:
			continue
		case <-ctx.Done():
			h.mu.Lock()
			if len(h.retiring) == 0 {
				err := h.closeErr
				h.finishCloseLocked(err)
				h.mu.Unlock()
				h.retireStop()
				return err
			}
			h.mu.Unlock()

			return h.closeAfterDeadline(ctx)
		}
	}
}

func (h *Host[T]) closeAfterDeadline(ctx context.Context) error {
	h.retireStop()
	h.forceClose(ctx)
	h.mu.Lock()
	err := errors.Join(h.closeErr, ctx.Err())
	h.finishCloseLocked(err)
	h.mu.Unlock()
	return err
}

func (h *Host[T]) release(current *generation[T]) {
	h.mu.Lock()
	if current.leases > 0 {
		current.leases--
	}
	ready := current.retiring && current.leases == 0
	h.notifyLocked()
	h.mu.Unlock()
	if ready {
		h.startRetirementIfReady(current)
	}
}

func (h *Host[T]) markRetiringLocked(current *generation[T]) {
	if current.retiring {
		return
	}
	current.retiring = true
	h.retiring[current] = struct{}{}
}

func (h *Host[T]) startReadyRetirements() {
	h.mu.Lock()
	ready := make([]*generation[T], 0, len(h.retiring))
	for current := range h.retiring {
		if current.leases == 0 {
			ready = append(ready, current)
		}
	}
	h.mu.Unlock()
	for _, current := range ready {
		h.startRetirementIfReady(current)
	}
}

func (h *Host[T]) startRetirementIfReady(current *generation[T]) {
	h.mu.Lock()
	_, retiring := h.retiring[current]
	ready := retiring && current.leases == 0
	h.mu.Unlock()
	if !ready {
		return
	}
	current.closeOnce.Do(func() {
		go h.closeGeneration(h.retireCtx, current)
	})
}

func (h *Host[T]) forceClose(ctx context.Context) {
	h.mu.Lock()
	remaining := make([]*generation[T], 0, len(h.retiring))
	for current := range h.retiring {
		remaining = append(remaining, current)
	}
	h.mu.Unlock()

	for _, current := range remaining {
		run := false
		current.closeOnce.Do(func() { run = true })
		if run {
			h.closeGeneration(ctx, current)
		}
	}
}

func (h *Host[T]) closeGeneration(ctx context.Context, current *generation[T]) {
	err := closeComponents(ctx, current.components)
	h.mu.Lock()
	if err != nil && !h.closeFinal {
		h.closeErr = errors.Join(h.closeErr, err)
	}
	delete(h.retiring, current)
	h.notifyLocked()
	h.mu.Unlock()
}

func (h *Host[T]) finishCloseLocked(err error) {
	if h.closeDone == nil {
		h.closeDone = make(chan struct{})
	}
	if h.closeFinal {
		return
	}
	h.closeErr = err
	h.closeFinal = true
	close(h.closeDone)
}

func (h *Host[T]) notifyLocked() {
	close(h.changed)
	h.changed = make(chan struct{})
}

func orderComponents(components []Component) ([]Component, error) {
	byID := make(map[ComponentID]int, len(components))
	orderedInput := make([]Component, len(components))
	for index, component := range components {
		if component.ID == "" {
			return nil, errors.New("kernel component id is required")
		}
		if component.Lifecycle == nil {
			return nil, fmt.Errorf("kernel component %q lifecycle is required", component.ID)
		}
		if _, exists := byID[component.ID]; exists {
			return nil, fmt.Errorf("duplicate kernel component id %q", component.ID)
		}
		byID[component.ID] = index
		orderedInput[index] = component
		orderedInput[index].After = append([]ComponentID(nil), component.After...)
	}
	for _, component := range orderedInput {
		for _, dependency := range component.After {
			if _, exists := byID[dependency]; !exists {
				return nil, fmt.Errorf("kernel component %q depends on missing component %q", component.ID, dependency)
			}
		}
	}

	ordered := make([]Component, 0, len(orderedInput))
	started := make(map[ComponentID]bool, len(orderedInput))
	for len(ordered) < len(orderedInput) {
		selected := -1
		for index, component := range orderedInput {
			if started[component.ID] {
				continue
			}
			ready := true
			for _, dependency := range component.After {
				if !started[dependency] {
					ready = false
					break
				}
			}
			if ready {
				selected = index
				break
			}
		}
		if selected < 0 {
			return nil, errors.New("kernel component dependency cycle")
		}
		component := orderedInput[selected]
		started[component.ID] = true
		ordered = append(ordered, component)
	}
	return ordered, nil
}

func closeComponents(ctx context.Context, components []Component) error {
	var errs []error
	for index := len(components) - 1; index >= 0; index-- {
		component := components[index]
		if err := component.Lifecycle.Close(ctx); err != nil {
			errs = append(errs, fmt.Errorf("close component %q: %w", component.ID, err))
		}
	}
	return errors.Join(errs...)
}
