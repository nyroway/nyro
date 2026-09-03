package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	configsnapshot "github.com/nyroway/nyro/go/internal/config/snapshot"
	"github.com/nyroway/nyro/go/internal/llm/pipeline"
	"github.com/nyroway/nyro/go/internal/telemetry"
	"github.com/nyroway/nyro/go/internal/telemetry/schema"
)

type telemetryResource struct {
	key      string
	signal   schema.Signal
	config   telemetry.SignalConfig
	provider *telemetry.Provider
	server   *http.Server
	listener net.Listener
	refs     int
}

type telemetryPool struct {
	mu      sync.Mutex
	entries map[string]*telemetryResource
	closed  bool
}

func newTelemetryPool() *telemetryPool {
	return &telemetryPool{entries: make(map[string]*telemetryResource)}
}

func telemetrySignalKey(signal schema.Signal, config telemetry.SignalConfig) string {
	encoded, _ := json.Marshal(config)
	return string(signal) + "\x00" + string(encoded)
}

func (pool *telemetryPool) acquire(ctx context.Context, signal schema.Signal, config telemetry.SignalConfig) (*telemetryResource, error) {
	key := telemetrySignalKey(signal, config)
	pool.mu.Lock()
	if pool.closed {
		pool.mu.Unlock()
		return nil, errors.New("telemetry resource pool is closed")
	}
	if existing := pool.entries[key]; existing != nil {
		existing.refs++
		pool.mu.Unlock()
		return existing, nil
	}
	pool.mu.Unlock()

	provider, err := telemetry.NewSignalProvider(ctx, signal, config)
	if err != nil {
		return nil, err
	}
	candidate := &telemetryResource{key: key, signal: signal, config: config, provider: provider}
	if signal == schema.SignalMetrics && provider.PromHandler != nil {
		listener, listenErr := net.Listen("tcp", provider.PromListen)
		if listenErr != nil {
			_ = provider.Shutdown(context.Background())
			return nil, listenErr
		}
		path := config.Params["path"]
		if path == "" {
			path = "/metrics"
		}
		mux := http.NewServeMux()
		mux.Handle(path, provider.PromHandler)
		candidate.listener = listener
		candidate.server = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	}

	pool.mu.Lock()
	if pool.closed {
		pool.mu.Unlock()
		_ = candidate.close(context.Background())
		return nil, errors.New("telemetry resource pool is closed")
	}
	if existing := pool.entries[key]; existing != nil {
		existing.refs++
		pool.mu.Unlock()
		_ = candidate.close(context.Background())
		return existing, nil
	}
	candidate.refs = 1
	pool.entries[key] = candidate
	pool.mu.Unlock()
	if candidate.server != nil {
		go func() {
			if err := candidate.server.Serve(candidate.listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
				slog.Error("prometheus metrics server failed", "error", err)
			}
		}()
	}
	return candidate, nil
}

func (pool *telemetryPool) release(ctx context.Context, resource *telemetryResource) error {
	if resource == nil {
		return nil
	}
	pool.mu.Lock()
	current := pool.entries[resource.key]
	if current != resource || resource.refs <= 0 {
		pool.mu.Unlock()
		return nil
	}
	resource.refs--
	if resource.refs != 0 {
		pool.mu.Unlock()
		return nil
	}
	delete(pool.entries, resource.key)
	pool.mu.Unlock()
	return resource.close(ctx)
}

func (pool *telemetryPool) Close(ctx context.Context) error {
	if pool == nil {
		return nil
	}
	pool.mu.Lock()
	if pool.closed {
		pool.mu.Unlock()
		return nil
	}
	pool.closed = true
	entries := make([]*telemetryResource, 0, len(pool.entries))
	for _, entry := range pool.entries {
		entries = append(entries, entry)
	}
	pool.entries = make(map[string]*telemetryResource)
	pool.mu.Unlock()
	var result error
	for _, entry := range entries {
		result = errors.Join(result, entry.close(ctx))
	}
	return result
}

func (resource *telemetryResource) close(ctx context.Context) error {
	if resource == nil {
		return nil
	}
	var serverErr error
	if resource.server != nil {
		serverErr = resource.server.Shutdown(ctx)
		if serverErr != nil {
			_ = resource.server.Close()
		}
	}
	return errors.Join(serverErr, resource.provider.Shutdown(ctx))
}

type telemetryBinding struct {
	pool   *telemetryPool
	config telemetry.Config
	target atomic.Pointer[telemetry.SwappableProvider]

	mu        sync.Mutex
	resources [3]*telemetryResource
	closed    bool
}

func newTelemetryBinding(pool *telemetryPool, config telemetry.Config) *telemetryBinding {
	return &telemetryBinding{pool: pool, config: config}
}

func (binding *telemetryBinding) Phase() pipeline.Phase {
	return telemetry.NewPhase(&binding.target)
}

func (binding *telemetryBinding) Start(ctx context.Context) error {
	if binding == nil || binding.pool == nil {
		return errors.New("telemetry binding pool is required")
	}
	configs := []struct {
		signal schema.Signal
		config telemetry.SignalConfig
	}{
		{schema.SignalLogs, binding.config.Logs},
		{schema.SignalMetrics, binding.config.Metrics},
		{schema.SignalTraces, binding.config.Traces},
	}
	var resources [3]*telemetryResource
	for index, config := range configs {
		resource, err := binding.pool.acquire(ctx, config.signal, config.config)
		if err != nil {
			return errors.Join(err, binding.releaseResources(context.Background(), resources))
		}
		resources[index] = resource
	}
	binding.mu.Lock()
	defer binding.mu.Unlock()
	if binding.closed {
		return errors.Join(errors.New("telemetry binding is closed"), binding.releaseResources(context.Background(), resources))
	}
	binding.resources = resources
	binding.target.Store(telemetry.NewSwappableProviderFromSignals(
		resources[0].provider,
		resources[1].provider,
		resources[2].provider,
	))
	return nil
}

func (binding *telemetryBinding) Close(ctx context.Context) error {
	if binding == nil {
		return nil
	}
	binding.mu.Lock()
	if binding.closed {
		binding.mu.Unlock()
		return nil
	}
	binding.closed = true
	resources := binding.resources
	binding.resources = [3]*telemetryResource{}
	binding.target.Store(nil)
	binding.mu.Unlock()
	return binding.releaseResources(ctx, resources)
}

func (binding *telemetryBinding) releaseResources(ctx context.Context, resources [3]*telemetryResource) error {
	var result error
	for index := len(resources) - 1; index >= 0; index-- {
		result = errors.Join(result, binding.pool.release(ctx, resources[index]))
	}
	return result
}

func (binding *telemetryBinding) logResource() *telemetryResource    { return binding.resources[0] }
func (binding *telemetryBinding) metricResource() *telemetryResource { return binding.resources[1] }
func (binding *telemetryBinding) traceResource() *telemetryResource  { return binding.resources[2] }

func resolveTelemetryConfig(snapshot *configsnapshot.Snapshot) (telemetry.Config, error) {
	config, err := telemetry.LoadConfig(func(key string) (string, error) {
		if snapshot == nil {
			return "", nil
		}
		value, _ := snapshot.SettingGet(key)
		return value, nil
	})
	if err != nil {
		return telemetry.Config{}, err
	}
	if config.Logs.Kind == "" && config.Metrics.Kind == "" && config.Traces.Kind == "" {
		config, err = telemetry.LoadConfig(func(key string) (string, error) {
			if key == "obs_logs_exporter" {
				return "stdout", nil
			}
			return "", nil
		})
	}
	return config, err
}
