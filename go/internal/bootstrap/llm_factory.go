package bootstrap

import (
	"context"
	"errors"
	"net/http"

	configsnapshot "github.com/nyroway/nyro/go/internal/config/snapshot"
	"github.com/nyroway/nyro/go/internal/kernel"
	"github.com/nyroway/nyro/go/internal/llm/protocol"
	"github.com/nyroway/nyro/go/internal/llm/provider"
	"github.com/nyroway/nyro/go/internal/llm/routing"
	llmruntime "github.com/nyroway/nyro/go/internal/llm/runtime"
)

type defaultLLMFactory struct {
	protocols *protocol.Catalog
	providers *provider.Catalog
	states    *statePool
	telemetry *telemetryPool
	roundTrip http.RoundTripper
}

func newDefaultLLMFactory(
	protocols *protocol.Catalog,
	providers *provider.Catalog,
	states *statePool,
	telemetryResources *telemetryPool,
	roundTripper http.RoundTripper,
) *defaultLLMFactory {
	return &defaultLLMFactory{
		protocols: protocols,
		providers: providers,
		states:    states,
		telemetry: telemetryResources,
		roundTrip: roundTripper,
	}
}

func (factory *defaultLLMFactory) Build(_ context.Context, snapshot *configsnapshot.Snapshot) (*llmruntime.Runtime, []kernel.Component, error) {
	switch {
	case factory == nil:
		return nil, nil, errors.New("LLM factory is required")
	case factory.protocols == nil:
		return nil, nil, errors.New("LLM protocol catalog is required")
	case factory.providers == nil:
		return nil, nil, errors.New("LLM provider catalog is required")
	case factory.states == nil:
		return nil, nil, errors.New("State resource pool is required")
	case factory.telemetry == nil:
		return nil, nil, errors.New("telemetry resource pool is required")
	}
	stateConfig, err := resolveStateConfig(snapshot)
	if err != nil {
		return nil, nil, err
	}
	telemetryConfig, err := resolveTelemetryConfig(snapshot)
	if err != nil {
		return nil, nil, err
	}
	state := newStateBinding(factory.states, stateConfig)
	observe := newTelemetryBinding(factory.telemetry, telemetryConfig)
	transport := newProviderTransport(llmruntime.SettingsFromSnapshot(snapshot), factory.roundTrip)
	components := []kernel.Component{
		{ID: "state", Lifecycle: state},
		{ID: "telemetry", Lifecycle: observe},
		{ID: "provider-transport", After: []kernel.ComponentID{"state", "telemetry"}, Lifecycle: transport},
	}
	runtime, err := llmruntime.New(llmruntime.Config{
		Snapshot:  snapshot,
		Protocols: factory.protocols,
		Providers: factory.providers,
		Router:    routing.New(),
		Transport: transport,
		Quota:     state.Store(),
		Observe:   observe.Phase(),
	})
	if err != nil {
		return nil, components, err
	}
	return runtime, components, nil
}
