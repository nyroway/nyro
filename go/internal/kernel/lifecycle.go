// Package kernel manages workload-neutral component lifecycles and runtime generations.
//
// Layer: 0 (foundation). Kernel production code imports only the Go standard library.
package kernel

import "context"

// ComponentID uniquely identifies one lifecycle component within a generation.
type ComponentID string

// Lifecycle starts and closes a resource-owning component. Close must begin
// cleanup even when ctx is already canceled and return promptly after ctx ends.
type Lifecycle interface {
	Start(context.Context) error
	Close(context.Context) error
}

// Component declares a lifecycle and the components that must start before it.
type Component struct {
	ID        ComponentID
	After     []ComponentID
	Lifecycle Lifecycle
}
