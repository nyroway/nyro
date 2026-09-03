package bootstrap

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	configsnapshot "github.com/nyroway/nyro/go/internal/config/snapshot"
	"github.com/nyroway/nyro/go/internal/kernel"
	llmruntime "github.com/nyroway/nyro/go/internal/llm/runtime"
)

func TestBuildDataPlaneStandaloneActivatesSynchronously(t *testing.T) {
	path := writeDataPlaneYAML(t, "version: 1\n")
	protocols, err := NewLLMProtocolCatalog()
	if err != nil {
		t.Fatal(err)
	}
	providers, err := NewLLMProviderCatalog()
	if err != nil {
		t.Fatal(err)
	}
	controller, err := BuildDataPlane(context.Background(), DataPlaneOptions{
		Protocols: protocols, Providers: providers, ConfigPath: path,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = controller.Shutdown(context.Background()) })
	if !controller.Ready() {
		t.Fatal("standalone BuildDataPlane returned before initial activation")
	}
	if runtime, release, ok := controller.RuntimeSource().Acquire(); !ok || runtime == nil || release == nil {
		t.Fatalf("RuntimeSource.Acquire() = %p, release present %v, ok %v", runtime, release != nil, ok)
	} else {
		release()
	}
}

func TestBuildDataPlaneStandaloneRejectsInvalidTelemetry(t *testing.T) {
	path := writeDataPlaneYAML(t, `version: 1
settings:
  telemetry:
    traces:
      exporter: otlp
`)
	protocols, _ := NewLLMProtocolCatalog()
	providers, _ := NewLLMProviderCatalog()
	controller, err := BuildDataPlane(context.Background(), DataPlaneOptions{
		Protocols: protocols, Providers: providers, ConfigPath: path,
	})
	if controller != nil || err == nil || !strings.Contains(err.Error(), "endpoint") {
		t.Fatalf("BuildDataPlane() = %#v, %v; want telemetry endpoint rejection", controller, err)
	}
}

func writeDataPlaneYAML(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRuntimeControllerShutdownStopsSourceThenHostThenDependenciesAndIsIdempotent(t *testing.T) {
	var mu sync.Mutex
	var events []string
	record := func(event string) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, event)
	}

	host := kernel.NewHost[*ApplicationRuntime]()
	if _, err := host.Activate(context.Background(), kernel.Candidate[*ApplicationRuntime]{
		Value: &ApplicationRuntime{Snapshot: (&configsnapshot.Builder{}).Build(), LLM: new(llmruntime.Runtime)},
		Components: []kernel.Component{{ID: "runtime", Lifecycle: controllerLifecycle{
			close: func(context.Context) error { record("host"); return nil },
		}}},
	}); err != nil {
		t.Fatal(err)
	}
	sourceCtx, sourceCancel := context.WithCancel(context.Background())
	sourceDone := make(chan struct{})
	go func() {
		<-sourceCtx.Done()
		record("source")
		close(sourceDone)
	}()
	dependency := controllerLifecycle{close: func(context.Context) error { record("dependency"); return nil }}
	controller := newRuntimeController(host, sourceCancel, sourceDone, []runtimeDependency{dependency})

	if err := controller.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := controller.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	got := append([]string(nil), events...)
	mu.Unlock()
	if want := []string{"source", "host", "dependency"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("shutdown events = %v, want %v", got, want)
	}
}

func TestRuntimeControllerShutdownCanceledFirstCallerDoesNotCancelOwnedCleanup(t *testing.T) {
	sourceCtx, sourceCancel := context.WithCancel(context.Background())
	sourceDone := make(chan struct{})
	releaseSource := make(chan struct{})
	go func() {
		<-sourceCtx.Done()
		<-releaseSource
		close(sourceDone)
	}()
	controller := newRuntimeController(nil, sourceCancel, sourceDone, nil)

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := controller.Shutdown(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("first Shutdown() error = %v, want context.Canceled", err)
	}
	secondDone := make(chan error, 1)
	go func() { secondDone <- controller.Shutdown(context.Background()) }()
	select {
	case err := <-secondDone:
		t.Fatalf("second Shutdown returned before owned cleanup completed: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(releaseSource)
	if err := <-secondDone; err != nil {
		t.Fatalf("second Shutdown() error = %v, want nil", err)
	}
}

func TestRuntimeControllerShutdownTimeoutThenLaterCallerCanWaitForCompletion(t *testing.T) {
	sourceCtx, sourceCancel := context.WithCancel(context.Background())
	sourceDone := make(chan struct{})
	releaseSource := make(chan struct{})
	go func() {
		<-sourceCtx.Done()
		<-releaseSource
		close(sourceDone)
	}()
	controller := newRuntimeController(nil, sourceCancel, sourceDone, nil)

	firstCtx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := controller.Shutdown(firstCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first Shutdown() error = %v, want context deadline", err)
	}
	secondDone := make(chan error, 1)
	go func() { secondDone <- controller.Shutdown(context.Background()) }()
	select {
	case err := <-secondDone:
		t.Fatalf("second Shutdown returned before owned cleanup completed: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(releaseSource)
	if err := <-secondDone; err != nil {
		t.Fatalf("second Shutdown() error = %v, want nil", err)
	}
}

func TestRuntimeControllerConcurrentShutdownCallersUseIndependentDeadlines(t *testing.T) {
	sourceCtx, sourceCancel := context.WithCancel(context.Background())
	sourceDone := make(chan struct{})
	releaseSource := make(chan struct{})
	go func() {
		<-sourceCtx.Done()
		<-releaseSource
		close(sourceDone)
	}()
	controller := newRuntimeController(nil, sourceCancel, sourceDone, nil)

	longDone := make(chan error, 1)
	go func() { longDone <- controller.Shutdown(context.Background()) }()
	<-sourceCtx.Done()
	shortCtx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	shortDone := make(chan error, 1)
	go func() { shortDone <- controller.Shutdown(shortCtx) }()
	select {
	case err := <-shortDone:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("short Shutdown() error = %v, want context deadline", err)
		}
	case <-time.After(200 * time.Millisecond):
		close(releaseSource)
		t.Fatal("short Shutdown caller was blocked by another caller's deadline")
	}
	close(releaseSource)
	if err := <-longDone; err != nil {
		t.Fatalf("long Shutdown() error = %v, want nil", err)
	}
}

type controllerLifecycle struct {
	start func(context.Context) error
	close func(context.Context) error
}

func (l controllerLifecycle) Start(ctx context.Context) error {
	if l.start != nil {
		return l.start(ctx)
	}
	return nil
}

func (l controllerLifecycle) Close(ctx context.Context) error {
	if l.close != nil {
		return l.close(ctx)
	}
	return nil
}
