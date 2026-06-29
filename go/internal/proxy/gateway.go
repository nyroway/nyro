package proxy

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nyroway/nyro/go/internal/auth"
	"github.com/nyroway/nyro/go/internal/router"
	"github.com/nyroway/nyro/go/internal/storage"
)

// Gateway holds the runtime dependencies for dispatching requests. It is
// config-driven: model→backend→provider resolution happens against Storage on
// every request; Router selects among a model's backends and tracks failover.
type Gateway struct {
	HTTPClient     *http.Client
	Storage        storage.Storage
	Router         *router.Router
	driverRegistry *auth.Registry

	proxyMu        sync.Mutex
	proxyClient    *http.Client
	proxyClientKey string
}

// NewGateway builds a Gateway backed by the given storage.
func NewGateway(s storage.Storage) *Gateway {
	return &Gateway{
		HTTPClient: &http.Client{Timeout: 5 * time.Minute},
		Storage:    s,
		Router:     router.New(),
	}
}

// SetDriverRegistry wires the OAuth driver registry (for token refresh).
func (g *Gateway) SetDriverRegistry(r *auth.Registry) { g.driverRegistry = r }

// httpClientFor returns the HTTP client for an upstream provider. When useProxy
// is false (or the proxy is disabled/empty in settings) it returns the default
// direct client; when useProxy is true and "proxy_enabled" is on, it returns a
// client routed through "proxy_url" (cached by url|force_http1). Ported from
// Gateway::http_client_for_provider.
func (g *Gateway) httpClientFor(useProxy bool) (*http.Client, error) {
	if !useProxy {
		return g.HTTPClient, nil
	}
	enabled, _ := g.Storage.Settings().Get("proxy_enabled")
	if !parseBoolSetting(enabled) {
		return g.HTTPClient, nil
	}
	proxyURL, _ := g.Storage.Settings().Get("proxy_url")
	proxyURL = strings.TrimSpace(proxyURL)
	if proxyURL == "" {
		return nil, errors.New("upstream proxy_url is empty")
	}
	parsed, err := url.Parse(proxyURL)
	if err != nil {
		return nil, fmt.Errorf("parse proxy_url: %w", err)
	}
	forceHTTP1Str, _ := g.Storage.Settings().Get("proxy_force_http1")
	forceHTTP1 := parseBoolSetting(forceHTTP1Str)

	cacheKey := proxyURL + "|" + strconv.FormatBool(forceHTTP1)
	g.proxyMu.Lock()
	defer g.proxyMu.Unlock()
	if g.proxyClient != nil && g.proxyClientKey == cacheKey {
		return g.proxyClient, nil
	}

	transport := &http.Transport{Proxy: http.ProxyURL(parsed)}
	if forceHTTP1 {
		transport.ForceAttemptHTTP2 = false
	}
	client := &http.Client{Timeout: 5 * time.Minute, Transport: transport}
	g.proxyClient = client
	g.proxyClientKey = cacheKey
	return client, nil
}

// parseBoolSetting parses a settings-stored boolean (true/1/yes/on).
func parseBoolSetting(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true", "1", "yes", "on":
		return true
	}
	return false
}
