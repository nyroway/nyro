package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"strings"
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
	refs     int
}

type telemetryPool struct {
	mu                  sync.Mutex
	entries             map[string]*telemetryResource
	prometheusListeners map[string]*prometheusListener
	closed              bool
}

func newTelemetryPool() *telemetryPool {
	return &telemetryPool{
		entries:             make(map[string]*telemetryResource),
		prometheusListeners: make(map[string]*prometheusListener),
	}
}

func telemetryProviderKey(signal schema.Signal, config telemetry.SignalConfig) string {
	config = telemetryProviderConfig(signal, config)
	encoded, _ := json.Marshal(config)
	return string(signal) + "\x00" + string(encoded)
}

func telemetryProviderConfig(signal schema.Signal, config telemetry.SignalConfig) telemetry.SignalConfig {
	params := make(map[string]string, len(config.Params))
	for key, value := range config.Params {
		params[key] = value
	}
	if signal == schema.SignalMetrics && config.Kind == schema.ExporterKindPrometheus {
		delete(params, "listen")
		delete(params, "path")
	}
	if len(params) == 0 {
		params = nil
	}
	return telemetry.SignalConfig{Kind: config.Kind, Params: params}
}

func (pool *telemetryPool) acquire(ctx context.Context, signal schema.Signal, config telemetry.SignalConfig) (*telemetryResource, error) {
	providerConfig := telemetryProviderConfig(signal, config)
	key := telemetryProviderKey(signal, providerConfig)
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

	provider, err := telemetry.NewSignalProvider(ctx, signal, providerConfig)
	if err != nil {
		return nil, err
	}
	candidate := &telemetryResource{key: key, signal: signal, config: providerConfig, provider: provider}

	pool.mu.Lock()
	if pool.closed {
		pool.mu.Unlock()
		_ = candidate.provider.Shutdown(context.Background())
		return nil, errors.New("telemetry resource pool is closed")
	}
	if existing := pool.entries[key]; existing != nil {
		existing.refs++
		pool.mu.Unlock()
		_ = candidate.provider.Shutdown(context.Background())
		return existing, nil
	}
	candidate.refs = 1
	pool.entries[key] = candidate
	pool.mu.Unlock()
	return candidate, nil
}

type prometheusRegistration struct {
	listener *prometheusListener
	path     string
	route    *prometheusRoute
	released bool
}

type prometheusListener struct {
	key      string
	server   *http.Server
	listener net.Listener
	routes   map[string]*prometheusRoute
	refs     int
	mux      atomic.Pointer[http.ServeMux]
}

type prometheusRoute struct {
	owner   *telemetryResource
	handler http.Handler
	refs    int

	mu     sync.RWMutex
	closed bool
}

func (route *prometheusRoute) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	route.mu.RLock()
	defer route.mu.RUnlock()
	if route.closed {
		http.NotFound(writer, request)
		return
	}
	route.handler.ServeHTTP(writer, request)
}

func (route *prometheusRoute) close() {
	route.mu.Lock()
	route.closed = true
	route.mu.Unlock()
}

func (listener *prometheusListener) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	mux := listener.mux.Load()
	if mux == nil {
		http.NotFound(writer, request)
		return
	}
	mux.ServeHTTP(writer, request)
}

func (listener *prometheusListener) register(resource *telemetryResource, path string) (*prometheusRegistration, error) {
	if existing := listener.routes[path]; existing != nil {
		if existing.owner != resource {
			return nil, fmt.Errorf("prometheus path %q is already registered on listener %q by a different Provider", path, listener.key)
		}
		existing.refs++
		listener.refs++
		return &prometheusRegistration{listener: listener, path: path, route: existing}, nil
	}

	route := &prometheusRoute{owner: resource, handler: resource.provider.PromHandler, refs: 1}
	routes := make(map[string]*prometheusRoute, len(listener.routes)+1)
	for registeredPath, registered := range listener.routes {
		routes[registeredPath] = registered
	}
	routes[path] = route
	mux, err := buildPrometheusMux(routes)
	if err != nil {
		return nil, fmt.Errorf("register prometheus path %q on listener %q: %w", path, listener.key, err)
	}
	listener.routes[path] = route
	listener.refs++
	listener.mux.Store(mux)
	return &prometheusRegistration{listener: listener, path: path, route: route}, nil
}

func buildPrometheusMux(routes map[string]*prometheusRoute) (mux *http.ServeMux, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			mux = nil
			err = fmt.Errorf("invalid or conflicting HTTP pattern: %v", recovered)
		}
	}()
	mux = http.NewServeMux()
	paths := make([]string, 0, len(routes))
	for path := range routes {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		mux.Handle(path, routes[path])
	}
	return mux, nil
}

func (pool *telemetryPool) registerPrometheus(config telemetry.SignalConfig, resource *telemetryResource) (*prometheusRegistration, error) {
	if resource == nil || resource.provider == nil || resource.provider.PromHandler == nil {
		return nil, nil
	}
	listen := strings.TrimSpace(config.Params["listen"])
	if listen == "" {
		listen = resource.provider.PromListen
	}
	resolved, err := net.ResolveTCPAddr("tcp", listen)
	if err != nil {
		return nil, fmt.Errorf("resolve prometheus listen address %q: %w", listen, err)
	}
	listen = resolved.String()
	path := strings.TrimSpace(config.Params["path"])
	if path == "" {
		path = "/metrics"
	}
	if !strings.HasPrefix(path, "/") {
		return nil, fmt.Errorf("prometheus path %q must start with /", path)
	}

	pool.mu.Lock()
	if pool.closed {
		pool.mu.Unlock()
		return nil, errors.New("telemetry resource pool is closed")
	}
	listener := pool.prometheusListeners[listen]
	created := false
	if listener == nil {
		networkListener, listenErr := net.Listen("tcp", listen)
		if listenErr != nil {
			pool.mu.Unlock()
			return nil, listenErr
		}
		listener = &prometheusListener{
			key: listen, listener: networkListener, routes: make(map[string]*prometheusRoute),
		}
		listener.server = &http.Server{Handler: listener, ReadHeaderTimeout: 10 * time.Second}
		created = true
	}
	registration, err := listener.register(resource, path)
	if err != nil {
		if created {
			_ = listener.listener.Close()
		}
		pool.mu.Unlock()
		return nil, err
	}
	if created {
		pool.prometheusListeners[listen] = listener
	}
	pool.mu.Unlock()
	if created {
		go listener.serve()
	}
	return registration, nil
}

func (listener *prometheusListener) serve() {
	if err := listener.server.Serve(listener.listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("prometheus metrics server failed", "error", err)
	}
}

func (pool *telemetryPool) releasePrometheus(ctx context.Context, registration *prometheusRegistration) error {
	if registration == nil {
		return nil
	}
	pool.mu.Lock()
	if registration.released {
		pool.mu.Unlock()
		return nil
	}
	registration.released = true
	listener := pool.prometheusListeners[registration.listener.key]
	if listener != registration.listener || listener.routes[registration.path] != registration.route {
		pool.mu.Unlock()
		return nil
	}
	route := registration.route
	route.refs--
	listener.refs--
	if route.refs == 0 {
		delete(listener.routes, registration.path)
		mux, err := buildPrometheusMux(listener.routes)
		if err != nil {
			listener.routes[registration.path] = route
			route.refs++
			listener.refs++
			registration.released = false
			pool.mu.Unlock()
			return err
		}
		listener.mux.Store(mux)
		route.close()
	}
	if listener.refs != 0 {
		pool.mu.Unlock()
		return nil
	}
	delete(pool.prometheusListeners, listener.key)
	err := listener.close(ctx)
	pool.mu.Unlock()
	return err
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
	listeners := make([]*prometheusListener, 0, len(pool.prometheusListeners))
	for _, listener := range pool.prometheusListeners {
		listeners = append(listeners, listener)
	}
	pool.entries = make(map[string]*telemetryResource)
	pool.prometheusListeners = make(map[string]*prometheusListener)
	pool.mu.Unlock()
	var result error
	for _, listener := range listeners {
		result = errors.Join(result, listener.close(ctx))
	}
	for _, entry := range entries {
		result = errors.Join(result, entry.close(ctx))
	}
	return result
}

func (resource *telemetryResource) close(ctx context.Context) error {
	if resource == nil || resource.provider == nil {
		return nil
	}
	return resource.provider.Shutdown(ctx)
}

func (listener *prometheusListener) close(ctx context.Context) error {
	if listener == nil || listener.server == nil {
		return nil
	}
	serverErr := listener.server.Shutdown(ctx)
	if serverErr != nil {
		_ = listener.server.Close()
	}
	for _, route := range listener.routes {
		route.close()
	}
	return serverErr
}

type telemetryBinding struct {
	pool   *telemetryPool
	config telemetry.Config
	target atomic.Pointer[telemetry.SwappableProvider]

	mu         sync.Mutex
	resources  [3]*telemetryResource
	prometheus *prometheusRegistration
	closed     bool
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
	registration, err := binding.pool.registerPrometheus(binding.config.Metrics, resources[1])
	if err != nil {
		return errors.Join(err, binding.releaseResources(context.Background(), resources))
	}
	binding.mu.Lock()
	if binding.closed {
		binding.mu.Unlock()
		return errors.Join(
			errors.New("telemetry binding is closed"),
			binding.pool.releasePrometheus(context.Background(), registration),
			binding.releaseResources(context.Background(), resources),
		)
	}
	binding.resources = resources
	binding.prometheus = registration
	binding.target.Store(telemetry.NewSwappableProviderFromSignals(
		resources[0].provider,
		resources[1].provider,
		resources[2].provider,
	))
	binding.mu.Unlock()
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
	registration := binding.prometheus
	binding.prometheus = nil
	binding.target.Store(nil)
	binding.mu.Unlock()
	return errors.Join(
		binding.pool.releasePrometheus(ctx, registration),
		binding.releaseResources(ctx, resources),
	)
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
