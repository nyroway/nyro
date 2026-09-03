package bootstrap

import (
	"sync"

	"github.com/nyroway/nyro/go/internal/kernel"
	llmruntime "github.com/nyroway/nyro/go/internal/llm/runtime"
)

// RuntimeSource adapts an Application Host to the narrow LLM HTTP ingress
// lease contract without exposing the configuration Snapshot.
type RuntimeSource struct {
	host *kernel.Host[*ApplicationRuntime]
}

func NewRuntimeSource(host *kernel.Host[*ApplicationRuntime]) *RuntimeSource {
	return &RuntimeSource{host: host}
}

func (source *RuntimeSource) Acquire() (*llmruntime.Runtime, func(), bool) {
	if source == nil || source.host == nil {
		return nil, nil, false
	}
	lease, ok := source.host.Acquire()
	if !ok {
		return nil, nil, false
	}
	runtime := lease.Value()
	if !runtime.isReady() {
		lease.Release()
		return nil, nil, false
	}
	var once sync.Once
	return runtime.LLM, func() { once.Do(lease.Release) }, true
}

func (source *RuntimeSource) Ready() bool {
	if source == nil || source.host == nil || !source.host.Ready() {
		return false
	}
	lease, ok := source.host.Acquire()
	if !ok {
		return false
	}
	defer lease.Release()
	return lease.Value().isReady()
}
