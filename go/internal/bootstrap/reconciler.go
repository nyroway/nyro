package bootstrap

import (
	"context"
	"errors"
	"sync"

	configsnapshot "github.com/nyroway/nyro/go/internal/config/snapshot"
	"github.com/nyroway/nyro/go/internal/kernel"
)

// Reconciler serializes the complete compare, build, start and activation
// decision for full configuration snapshots.
type Reconciler struct {
	host    *kernel.Host[*ApplicationRuntime]
	builder *GraphBuilder
	mu      sync.Mutex
}

func NewReconciler(host *kernel.Host[*ApplicationRuntime], builder *GraphBuilder) *Reconciler {
	return &Reconciler{host: host, builder: builder}
}

// Apply activates snapshot atomically, retaining the last-known-good
// generation on candidate construction or startup failure.
func (reconciler *Reconciler) Apply(ctx context.Context, snapshot *configsnapshot.Snapshot, version string) error {
	if reconciler == nil || reconciler.host == nil {
		return errors.New("bootstrap reconciler: host is required")
	}
	if reconciler.builder == nil {
		return errors.New("bootstrap reconciler: graph builder is required")
	}
	if snapshot == nil {
		return errors.New("bootstrap reconciler: snapshot is required")
	}
	fingerprint := snapshot.Fingerprint()
	if fingerprint == "" {
		return errors.New("bootstrap reconciler: snapshot fingerprint is required")
	}

	reconciler.mu.Lock()
	defer reconciler.mu.Unlock()
	if reconciler.host.Status().ActiveFingerprint == fingerprint {
		return nil
	}
	candidate, err := reconciler.builder.Build(ctx, snapshot, version)
	if err != nil {
		return err
	}
	_, err = reconciler.host.Activate(ctx, candidate)
	if err != nil {
		return errors.Join(err, closeCandidateComponents(ctx, candidate.Components))
	}
	return nil
}
