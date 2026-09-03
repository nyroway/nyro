package bootstrap

import (
	"context"
	"testing"
	"time"

	configsnapshot "github.com/nyroway/nyro/go/internal/config/snapshot"
	"github.com/nyroway/nyro/go/internal/kernel"
	llmruntime "github.com/nyroway/nyro/go/internal/llm/runtime"
)

func TestRuntimeSourceLeaseKeepsGenerationResourcesAliveAndReleaseIsIdempotent(t *testing.T) {
	host := kernel.NewHost[*ApplicationRuntime]()
	oldRuntime := new(llmruntime.Runtime)
	closed := make(chan struct{})
	lifecycle := &reconcilerLifecycle{closed: closed}
	if _, err := host.Activate(context.Background(), kernel.Candidate[*ApplicationRuntime]{
		Version: "v1", Fingerprint: "fp1",
		Value:      &ApplicationRuntime{Snapshot: (&configsnapshot.Builder{}).Build(), LLM: oldRuntime},
		Components: []kernel.Component{{ID: "owned", Lifecycle: lifecycle}},
	}); err != nil {
		t.Fatal(err)
	}
	source := NewRuntimeSource(host)
	gotRuntime, release, ok := source.Acquire()
	if !ok || gotRuntime != oldRuntime || release == nil {
		t.Fatalf("Acquire() = %p, release present %v, ok %v", gotRuntime, release != nil, ok)
	}
	if _, err := host.Activate(context.Background(), kernel.Candidate[*ApplicationRuntime]{
		Version: "v2", Fingerprint: "fp2",
		Value:      &ApplicationRuntime{Snapshot: (&configsnapshot.Builder{}).Build(), LLM: new(llmruntime.Runtime)},
		Components: []kernel.Component{{ID: "owned", Lifecycle: &reconcilerLifecycle{}}},
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-closed:
		t.Fatal("retired generation closed before RuntimeSource release")
	default:
	}
	release()
	release()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("retired generation did not close after RuntimeSource release")
	}
	if got := lifecycle.closes.Load(); got != 1 {
		t.Fatalf("resource closes = %d, want 1 after repeated release", got)
	}
}

func TestRuntimeSourceReadinessIncludesGenerationHealth(t *testing.T) {
	host := kernel.NewHost[*ApplicationRuntime]()
	healthy := false
	if _, err := host.Activate(context.Background(), kernel.Candidate[*ApplicationRuntime]{
		Version: "v1", Fingerprint: "fp1",
		Value: &ApplicationRuntime{
			Snapshot: (&configsnapshot.Builder{}).Build(),
			LLM:      new(llmruntime.Runtime),
			ready:    func() bool { return healthy },
		},
	}); err != nil {
		t.Fatal(err)
	}
	source := NewRuntimeSource(host)
	if source.Ready() {
		t.Fatal("Ready() = true while active generation health is false")
	}
	if runtime, release, ok := source.Acquire(); ok || runtime != nil || release != nil {
		t.Fatalf("Acquire() accepted unhealthy generation: %p release present %v ok %v", runtime, release != nil, ok)
	}
	healthy = true
	if !source.Ready() {
		t.Fatal("Ready() = false for healthy active generation")
	}
}
