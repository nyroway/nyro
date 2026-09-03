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
)

type telemetryResource struct {
	key      string
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

func telemetryKey(config telemetry.Config) string {
	encoded, _ := json.Marshal(config)
	return string(encoded)
}

func (pool *telemetryPool) acquire(ctx context.Context, config telemetry.Config) (*telemetryResource, error) {
	key := telemetryKey(config)
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

	provider, err := telemetry.NewProvider(ctx, config)
	if err != nil {
		return nil, err
	}
	candidate := &telemetryResource{key: key, provider: provider}
	if provider.PromHandler != nil {
		listener, listenErr := net.Listen("tcp", provider.PromListen)
		if listenErr != nil {
			_ = provider.Shutdown(context.Background())
			return nil, listenErr
		}
		path := config.Metrics.Params["path"]
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

	mu       sync.Mutex
	resource *telemetryResource
	closed   bool
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
	resource, err := binding.pool.acquire(ctx, binding.config)
	if err != nil {
		return err
	}
	binding.mu.Lock()
	defer binding.mu.Unlock()
	if binding.closed {
		_ = binding.pool.release(context.Background(), resource)
		return errors.New("telemetry binding is closed")
	}
	binding.resource = resource
	binding.target.Store(telemetry.NewSwappableProvider(resource.provider))
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
	resource := binding.resource
	binding.resource = nil
	binding.target.Store(nil)
	binding.mu.Unlock()
	return binding.pool.release(ctx, resource)
}

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
