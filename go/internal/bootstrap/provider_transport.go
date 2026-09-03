package bootstrap

import (
	"context"
	"errors"
	"net/http"
	"sync"

	"github.com/nyroway/nyro/go/internal/llm/provider"
	providerhttp "github.com/nyroway/nyro/go/internal/llm/provider/httptransport"
	llmruntime "github.com/nyroway/nyro/go/internal/llm/runtime"
)

// providerTransport is owned by exactly one Application generation. Concrete
// connection pools are created lazily after Start and close only when that
// generation's leases have drained.
type providerTransport struct {
	settings     llmruntime.Settings
	roundTripper http.RoundTripper

	mu         sync.Mutex
	started    bool
	closed     bool
	transports map[string]*providerhttp.Transport
}

func newProviderTransport(settings llmruntime.Settings, roundTripper http.RoundTripper) *providerTransport {
	return &providerTransport{settings: settings, roundTripper: roundTripper}
}

func (transport *providerTransport) Start(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if transport.closed {
		return errors.New("provider transport is closed")
	}
	transport.started = true
	if transport.transports == nil {
		transport.transports = make(map[string]*providerhttp.Transport)
	}
	return nil
}

func (transport *providerTransport) Do(ctx context.Context, request provider.Request) (*provider.Response, error) {
	proxyURL := providerhttp.NormalizeProxyURL(request.ProxyURL)
	key := proxyURL
	transport.mu.Lock()
	if !transport.started || transport.closed {
		transport.mu.Unlock()
		return nil, errors.New("provider transport is not active")
	}
	selected := transport.transports[key]
	if selected == nil {
		var err error
		selected, err = providerhttp.New(providerhttp.Config{
			RequestTimeout: transport.settings.RequestTimeout,
			ConnectTimeout: transport.settings.ConnectTimeout,
			ProxyURL:       proxyURL,
			RoundTripper:   transport.roundTripper,
		})
		if err != nil {
			transport.mu.Unlock()
			return nil, err
		}
		transport.transports[key] = selected
	}
	transport.mu.Unlock()
	return selected.Do(ctx, request)
}

func (transport *providerTransport) CloseIdleConnections() {
	transport.mu.Lock()
	transports := make([]*providerhttp.Transport, 0, len(transport.transports))
	for _, selected := range transport.transports {
		transports = append(transports, selected)
	}
	transport.mu.Unlock()
	for _, selected := range transports {
		selected.CloseIdleConnections()
	}
}

func (transport *providerTransport) Close(context.Context) error {
	transport.mu.Lock()
	if transport.closed {
		transport.mu.Unlock()
		return nil
	}
	transport.closed = true
	transport.started = false
	transports := make([]*providerhttp.Transport, 0, len(transport.transports))
	for _, selected := range transport.transports {
		transports = append(transports, selected)
	}
	transport.transports = nil
	transport.mu.Unlock()
	for _, selected := range transports {
		selected.CloseIdleConnections()
	}
	return nil
}

var _ provider.Transport = (*providerTransport)(nil)
