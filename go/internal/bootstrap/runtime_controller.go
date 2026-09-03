package bootstrap

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"google.golang.org/grpc"

	"github.com/nyroway/nyro/go/internal/config"
	"github.com/nyroway/nyro/go/internal/configsync"
	"github.com/nyroway/nyro/go/internal/configsync/pki"
	"github.com/nyroway/nyro/go/internal/kernel"
	"github.com/nyroway/nyro/go/internal/llm/protocol"
	"github.com/nyroway/nyro/go/internal/llm/provider"
)

const (
	RuntimeShutdownTimeout  = 5 * time.Second
	certExpiryCheckInterval = 24 * time.Hour
)

// DataPlaneOptions selects one canonical configuration source for an LLM data
// plane. Exactly one of ConfigPath and SyncTarget is required.
type DataPlaneOptions struct {
	Protocols *protocol.Catalog
	Providers *provider.Catalog

	ConfigPath      string
	SyncTarget      string
	SyncTLS         *tls.Config
	SyncToken       string
	SyncDialOptions []grpc.DialOption
	ListenAddr      string
}

type runtimeDependency interface {
	Close(context.Context) error
}

type dependencyFunc func(context.Context) error

func (close dependencyFunc) Close(ctx context.Context) error { return close(ctx) }

// RuntimeController owns the configuration source, Application Host and
// process-scoped resource pools in their required shutdown order.
type RuntimeController struct {
	host   *kernel.Host[*ApplicationRuntime]
	source *RuntimeSource

	sourceCancel context.CancelFunc
	sourceDone   <-chan struct{}
	dependencies []runtimeDependency

	shutdownOnce sync.Once
	shutdownErr  error
}

func newRuntimeController(
	host *kernel.Host[*ApplicationRuntime],
	sourceCancel context.CancelFunc,
	sourceDone <-chan struct{},
	dependencies []runtimeDependency,
) *RuntimeController {
	return &RuntimeController{
		host: host, source: NewRuntimeSource(host),
		sourceCancel: sourceCancel, sourceDone: sourceDone,
		dependencies: append([]runtimeDependency(nil), dependencies...),
	}
}

func (controller *RuntimeController) RuntimeSource() *RuntimeSource {
	if controller == nil {
		return nil
	}
	return controller.source
}

func (controller *RuntimeController) Ready() bool {
	return controller != nil && controller.source != nil && controller.source.Ready()
}

func (controller *RuntimeController) Shutdown(ctx context.Context) error {
	if controller == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	controller.shutdownOnce.Do(func() {
		if controller.sourceCancel != nil {
			controller.sourceCancel()
		}
		var sourceErr error
		if controller.sourceDone != nil {
			select {
			case <-controller.sourceDone:
			case <-ctx.Done():
				sourceErr = ctx.Err()
			}
		}
		var hostErr error
		if controller.host != nil {
			hostErr = controller.host.Close(ctx)
		}
		var dependencyErr error
		for _, dependency := range controller.dependencies {
			if dependency != nil {
				dependencyErr = errors.Join(dependencyErr, dependency.Close(ctx))
			}
		}
		controller.shutdownErr = errors.Join(sourceErr, hostErr, dependencyErr)
	})
	return controller.shutdownErr
}

// BuildDataPlane constructs the explicit catalogs' runtime graph and starts
// its selected configuration source. Standalone initial activation is
// synchronous; ConfigSync begins live and not-ready until a candidate applies.
func BuildDataPlane(ctx context.Context, options DataPlaneOptions) (*RuntimeController, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if options.Protocols == nil {
		return nil, errors.New("dataplane: protocol catalog is required")
	}
	if options.Providers == nil {
		return nil, errors.New("dataplane: provider catalog is required")
	}
	if (options.ConfigPath == "") == (options.SyncTarget == "") {
		return nil, errors.New("dataplane: exactly one of ConfigPath or SyncTarget is required")
	}

	states := newStatePool()
	telemetryResources := newTelemetryPool()
	host := kernel.NewHost[*ApplicationRuntime]()
	factory := newDefaultLLMFactory(options.Protocols, options.Providers, states, telemetryResources, nil)
	graph := &GraphBuilder{Protocols: options.Protocols, Providers: options.Providers, LLMFactory: factory}
	reconciler := NewReconciler(host, graph)
	dependencies := []runtimeDependency{
		dependencyFunc(telemetryResources.Close),
		dependencyFunc(func(context.Context) error { return states.Close() }),
	}
	controller := newRuntimeController(host, nil, nil, dependencies)
	cleanup := func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), RuntimeShutdownTimeout)
		defer cancel()
		_ = controller.Shutdown(cleanupCtx)
	}

	if options.ConfigPath != "" {
		loaded, missing, err := config.LoadYAML(options.ConfigPath)
		if err != nil {
			cleanup()
			return nil, err
		}
		for _, name := range missing {
			slog.Warn("config references an unset environment variable", "var", name)
		}
		snapshot, err := loaded.BuildSnapshot(options.Providers)
		if err != nil {
			cleanup()
			return nil, fmt.Errorf("build snapshot: %w", err)
		}
		if err := reconciler.Apply(ctx, snapshot, "standalone"); err != nil {
			cleanup()
			return nil, fmt.Errorf("activate snapshot: %w", err)
		}
		return controller, nil
	}

	sourceCtx, sourceCancel := context.WithCancel(ctx)
	sourceDone := make(chan struct{})
	client := configsync.NewConfigClient(options.SyncTarget, reconciler, servicePort(options.ListenAddr), options.SyncTLS)
	if len(options.SyncDialOptions) > 0 {
		client.SetDialOptions(options.SyncDialOptions...)
	}
	client.SetJoinToken(options.SyncToken)
	controller.sourceCancel = sourceCancel
	controller.sourceDone = sourceDone
	go func() {
		defer close(sourceDone)
		if err := client.Run(sourceCtx); err != nil {
			slog.Warn("config-sync source stopped", "error", err)
		}
	}()
	pki.WatchExpiry(sourceCtx, options.SyncTLS, certExpiryCheckInterval, func(notAfter time.Time) {
		slog.Warn("config-sync client certificate expiring soon — run `nyro tool ca sign-proxy` and redistribute before it lapses",
			"not_after", notAfter, "remaining", time.Until(notAfter).Round(time.Hour))
	})
	return controller, nil
}

func servicePort(address string) string {
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		return ""
	}
	return port
}
