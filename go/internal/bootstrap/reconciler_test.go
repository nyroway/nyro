package bootstrap

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	configsnapshot "github.com/nyroway/nyro/go/internal/config/snapshot"
	"github.com/nyroway/nyro/go/internal/kernel"
	llmruntime "github.com/nyroway/nyro/go/internal/llm/runtime"
)

type reconcilerFactory struct {
	mu         sync.Mutex
	builds     int
	buildErr   error
	startErr   error
	runtimes   map[*configsnapshot.Snapshot]*llmruntime.Runtime
	lifecycles []*reconcilerLifecycle
}

type llmFactoryFunc func(context.Context, *configsnapshot.Snapshot) (*llmruntime.Runtime, []kernel.Component, error)

func (build llmFactoryFunc) Build(ctx context.Context, snapshot *configsnapshot.Snapshot) (*llmruntime.Runtime, []kernel.Component, error) {
	return build(ctx, snapshot)
}

func (f *reconcilerFactory) Build(_ context.Context, snapshot *configsnapshot.Snapshot) (*llmruntime.Runtime, []kernel.Component, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.builds++
	if f.buildErr != nil {
		return nil, nil, f.buildErr
	}
	runtime := new(llmruntime.Runtime)
	if f.runtimes == nil {
		f.runtimes = make(map[*configsnapshot.Snapshot]*llmruntime.Runtime)
	}
	f.runtimes[snapshot] = runtime
	lifecycle := &reconcilerLifecycle{startErr: f.startErr}
	f.lifecycles = append(f.lifecycles, lifecycle)
	return runtime, []kernel.Component{{ID: "llm", Lifecycle: lifecycle}}, nil
}

func (f *reconcilerFactory) buildCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.builds
}

func (f *reconcilerFactory) runtimeFor(snapshot *configsnapshot.Snapshot) *llmruntime.Runtime {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.runtimes[snapshot]
}

type reconcilerLifecycle struct {
	startErr  error
	starts    atomic.Int32
	closes    atomic.Int32
	closed    chan struct{}
	closeOnce sync.Once
}

func (l *reconcilerLifecycle) Start(context.Context) error {
	l.starts.Add(1)
	return l.startErr
}

func (l *reconcilerLifecycle) Close(context.Context) error {
	l.closes.Add(1)
	if l.closed != nil {
		l.closeOnce.Do(func() { close(l.closed) })
	}
	return nil
}

func TestReconcilerActivatesSnapshotAndRuntimeTogether(t *testing.T) {
	host := kernel.NewHost[*ApplicationRuntime]()
	factory := &reconcilerFactory{}
	reconciler := NewReconciler(host, &GraphBuilder{LLMFactory: factory})
	snapshot := reconcilerSnapshot("one")

	if err := reconciler.Apply(context.Background(), snapshot, "v1"); err != nil {
		t.Fatalf("Apply(): %v", err)
	}
	lease, ok := host.Acquire()
	if !ok {
		t.Fatal("Host.Acquire() rejected activated generation")
	}
	defer lease.Release()
	if got := lease.Value(); got.Snapshot != snapshot || got.LLM != factory.runtimeFor(snapshot) {
		t.Fatalf("generation = %#v, want snapshot and runtime from one build", got)
	}
}

func TestReconcilerKeepsLastKnownGoodWhenBuildFails(t *testing.T) {
	host := kernel.NewHost[*ApplicationRuntime]()
	factory := &reconcilerFactory{}
	reconciler := NewReconciler(host, &GraphBuilder{LLMFactory: factory})
	good := reconcilerSnapshot("good")
	if err := reconciler.Apply(context.Background(), good, "v1"); err != nil {
		t.Fatal(err)
	}
	factory.buildErr = errors.New("build rejected")
	if err := reconciler.Apply(context.Background(), reconcilerSnapshot("bad"), "v2"); !errors.Is(err, factory.buildErr) {
		t.Fatalf("Apply() error = %v, want build rejection", err)
	}
	assertActiveGeneration(t, host, good, factory.runtimeFor(good))
}

func TestReconcilerKeepsLastKnownGoodWhenStartFails(t *testing.T) {
	host := kernel.NewHost[*ApplicationRuntime]()
	factory := &reconcilerFactory{}
	reconciler := NewReconciler(host, &GraphBuilder{LLMFactory: factory})
	good := reconcilerSnapshot("good")
	if err := reconciler.Apply(context.Background(), good, "v1"); err != nil {
		t.Fatal(err)
	}
	factory.startErr = errors.New("start rejected")
	if err := reconciler.Apply(context.Background(), reconcilerSnapshot("bad"), "v2"); !errors.Is(err, factory.startErr) {
		t.Fatalf("Apply() error = %v, want start rejection", err)
	}
	assertActiveGeneration(t, host, good, factory.runtimeFor(good))
	failed := factory.lifecycles[len(factory.lifecycles)-1]
	if failed.starts.Load() != 1 || failed.closes.Load() != 1 {
		t.Fatalf("failed lifecycle start/close = %d/%d, want 1/1", failed.starts.Load(), failed.closes.Load())
	}
}

func TestReconcilerSkipsUnchangedFingerprint(t *testing.T) {
	host := kernel.NewHost[*ApplicationRuntime]()
	factory := &reconcilerFactory{}
	reconciler := NewReconciler(host, &GraphBuilder{LLMFactory: factory})
	first := reconcilerSnapshot("same")
	if err := reconciler.Apply(context.Background(), first, "v1"); err != nil {
		t.Fatal(err)
	}
	equivalent := reconcilerSnapshot("same")
	if err := reconciler.Apply(context.Background(), equivalent, "v2"); err != nil {
		t.Fatal(err)
	}
	if got := factory.buildCount(); got != 1 {
		t.Fatalf("factory builds = %d, want 1", got)
	}
	if status := host.Status(); status.ActiveVersion != "v1" || status.ActiveFingerprint != first.Fingerprint() {
		t.Fatalf("Host.Status() = %+v, unchanged apply must retain active identity", status)
	}
}

func TestReconcilerInitialFailureLeavesHostNotReady(t *testing.T) {
	host := kernel.NewHost[*ApplicationRuntime]()
	factory := &reconcilerFactory{buildErr: errors.New("initial build rejected")}
	reconciler := NewReconciler(host, &GraphBuilder{LLMFactory: factory})
	if err := reconciler.Apply(context.Background(), reconcilerSnapshot("bad"), "v1"); err == nil {
		t.Fatal("Apply() error = nil")
	}
	if host.Ready() {
		t.Fatal("Host.Ready() = true after initial failure")
	}
}

func TestReconcilerOldRequestKeepsOldSnapshotAndRuntime(t *testing.T) {
	host := kernel.NewHost[*ApplicationRuntime]()
	factory := &reconcilerFactory{}
	reconciler := NewReconciler(host, &GraphBuilder{LLMFactory: factory})
	oldSnapshot := reconcilerSnapshot("old")
	if err := reconciler.Apply(context.Background(), oldSnapshot, "v1"); err != nil {
		t.Fatal(err)
	}
	oldLease, ok := host.Acquire()
	if !ok {
		t.Fatal("Host.Acquire() rejected old generation")
	}
	oldLifecycle := factory.lifecycles[0]
	oldLifecycle.closed = make(chan struct{})

	newSnapshot := reconcilerSnapshot("new")
	if err := reconciler.Apply(context.Background(), newSnapshot, "v2"); err != nil {
		t.Fatal(err)
	}
	if got := oldLease.Value(); got.Snapshot != oldSnapshot || got.LLM != factory.runtimeFor(oldSnapshot) {
		t.Fatalf("old lease changed generations: %#v", got)
	}
	select {
	case <-oldLifecycle.closed:
		t.Fatal("old generation closed while request lease remained active")
	default:
	}
	oldLease.Release()
	select {
	case <-oldLifecycle.closed:
	case <-time.After(time.Second):
		t.Fatal("old generation did not close after request released")
	}
}

func TestReconcilerClosesUnstartedCandidateWhenGraphIsRejected(t *testing.T) {
	host := kernel.NewHost[*ApplicationRuntime]()
	lifecycle := &reconcilerLifecycle{}
	factory := llmFactoryFunc(func(context.Context, *configsnapshot.Snapshot) (*llmruntime.Runtime, []kernel.Component, error) {
		return new(llmruntime.Runtime), []kernel.Component{{
			ID: "resource", After: []kernel.ComponentID{"missing"}, Lifecycle: lifecycle,
		}}, nil
	})
	reconciler := NewReconciler(host, &GraphBuilder{LLMFactory: factory})
	if err := reconciler.Apply(context.Background(), reconcilerSnapshot("invalid-graph"), "v1"); err == nil {
		t.Fatal("Apply() error = nil")
	}
	if got := lifecycle.closes.Load(); got != 1 {
		t.Fatalf("candidate resource closes = %d, want 1 after graph rejection", got)
	}
}

func assertActiveGeneration(t *testing.T, host *kernel.Host[*ApplicationRuntime], snapshot *configsnapshot.Snapshot, runtime *llmruntime.Runtime) {
	t.Helper()
	lease, ok := host.Acquire()
	if !ok {
		t.Fatal("Host.Acquire() rejected last-known-good generation")
	}
	defer lease.Release()
	if got := lease.Value(); got.Snapshot != snapshot || got.LLM != runtime {
		t.Fatalf("active generation = %#v, want last-known-good", got)
	}
}

func reconcilerSnapshot(value string) *configsnapshot.Snapshot {
	var builder configsnapshot.Builder
	builder.SetSetting("test.value", value)
	return builder.Build()
}
