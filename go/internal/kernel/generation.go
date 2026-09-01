package kernel

import "sync"

// Candidate is a fully described runtime generation awaiting activation.
type Candidate[T any] struct {
	Version     string
	Fingerprint string
	Value       T
	Components  []Component
}

// Activation reports the generation published by Activate.
type Activation struct {
	Changed bool
	Version string
}

// Status is a point-in-time view of the host lifecycle.
type Status struct {
	Ready             bool
	ActiveVersion     string
	ActiveFingerprint string
	Retiring          int
}

type generation[T any] struct {
	version     string
	fingerprint string
	value       T
	components  []Component
	leases      int
	retiring    bool
	closeOnce   sync.Once
}

type leaseState[T any] struct {
	once       sync.Once
	host       *Host[T]
	generation *generation[T]
}

// Lease keeps one runtime generation alive until Release is called.
// Copies of a Lease share the same idempotent release operation.
type Lease[T any] struct {
	state *leaseState[T]
}

// Value returns the runtime value held by the lease.
func (l *Lease[T]) Value() T {
	if l == nil || l.state == nil || l.state.generation == nil {
		var zero T
		return zero
	}
	return l.state.generation.value
}

// Release relinquishes the generation lease. Repeated calls are safe.
func (l *Lease[T]) Release() {
	if l == nil || l.state == nil {
		return
	}
	l.state.once.Do(func() {
		l.state.host.release(l.state.generation)
	})
}
