package bootstrap

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

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
