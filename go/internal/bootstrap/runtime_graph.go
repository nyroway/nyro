package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"sync"

	configsnapshot "github.com/nyroway/nyro/go/internal/config/snapshot"
	"github.com/nyroway/nyro/go/internal/kernel"
	"github.com/nyroway/nyro/go/internal/llm/protocol"
	"github.com/nyroway/nyro/go/internal/llm/provider"
	llmruntime "github.com/nyroway/nyro/go/internal/llm/runtime"
)

// ApplicationRuntime is the immutable, atomically published application
// generation used by one request lease.
type ApplicationRuntime struct {
	Snapshot *configsnapshot.Snapshot
	LLM      *llmruntime.Runtime

	ready func() bool
}

func (runtime *ApplicationRuntime) isReady() bool {
	return runtime != nil && runtime.Snapshot != nil && runtime.LLM != nil &&
		(runtime.ready == nil || runtime.ready())
}

// LLMFactory constructs one Snapshot-bound LLM Runtime and declares every
// resource-owning lifecycle needed by it.
type LLMFactory interface {
	Build(context.Context, *configsnapshot.Snapshot) (*llmruntime.Runtime, []kernel.Component, error)
}

// GraphBuilder turns one immutable Snapshot into one typed Kernel candidate.
type GraphBuilder struct {
	Protocols  *protocol.Catalog
	Providers  *provider.Catalog
	LLMFactory LLMFactory
}

func (builder *GraphBuilder) Build(ctx context.Context, snapshot *configsnapshot.Snapshot, version string) (kernel.Candidate[*ApplicationRuntime], error) {
	if builder == nil || builder.LLMFactory == nil {
		return kernel.Candidate[*ApplicationRuntime]{}, errors.New("bootstrap graph: LLM factory is required")
	}
	if snapshot == nil {
		return kernel.Candidate[*ApplicationRuntime]{}, errors.New("bootstrap graph: snapshot is required")
	}
	runtime, components, err := builder.LLMFactory.Build(ctx, snapshot)
	components = guardCandidateComponents(components)
	if err != nil {
		return kernel.Candidate[*ApplicationRuntime]{}, errors.Join(err, closeCandidateComponents(ctx, components))
	}
	if runtime == nil {
		return kernel.Candidate[*ApplicationRuntime]{}, errors.Join(
			errors.New("bootstrap graph: LLM factory returned a nil runtime"),
			closeCandidateComponents(ctx, components),
		)
	}
	application := &ApplicationRuntime{Snapshot: snapshot, LLM: runtime}
	var readiness []interface{ Ready() bool }
	for _, component := range components {
		if ready, ok := component.Lifecycle.(interface{ Ready() bool }); ok {
			readiness = append(readiness, ready)
		}
	}
	if len(readiness) > 0 {
		application.ready = func() bool {
			for _, ready := range readiness {
				if !ready.Ready() {
					return false
				}
			}
			return true
		}
	}
	return kernel.Candidate[*ApplicationRuntime]{
		Version:     version,
		Fingerprint: snapshot.Fingerprint(),
		Value:       application,
		Components:  components,
	}, nil
}

type onceLifecycle struct {
	lifecycle kernel.Lifecycle
	once      sync.Once
	closeErr  error
}

func (lifecycle *onceLifecycle) Start(ctx context.Context) error {
	return lifecycle.lifecycle.Start(ctx)
}

func (lifecycle *onceLifecycle) Close(ctx context.Context) error {
	lifecycle.once.Do(func() { lifecycle.closeErr = lifecycle.lifecycle.Close(ctx) })
	return lifecycle.closeErr
}

func (lifecycle *onceLifecycle) Ready() bool {
	ready, ok := lifecycle.lifecycle.(interface{ Ready() bool })
	return !ok || ready.Ready()
}

func guardCandidateComponents(components []kernel.Component) []kernel.Component {
	guarded := make([]kernel.Component, len(components))
	for index, component := range components {
		guarded[index] = component
		guarded[index].After = append([]kernel.ComponentID(nil), component.After...)
		if component.Lifecycle != nil {
			guarded[index].Lifecycle = &onceLifecycle{lifecycle: component.Lifecycle}
		}
	}
	return guarded
}

func closeCandidateComponents(ctx context.Context, components []kernel.Component) error {
	var result error
	for index := len(components) - 1; index >= 0; index-- {
		component := components[index]
		if component.Lifecycle == nil {
			continue
		}
		if err := component.Lifecycle.Close(ctx); err != nil {
			result = errors.Join(result, fmt.Errorf("close candidate component %q: %w", component.ID, err))
		}
	}
	return result
}
