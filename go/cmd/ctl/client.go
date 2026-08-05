// Package ctl implements CLI commands for interacting with the nyro Admin API.
package ctl

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/nyroway/nyro/go/internal/defaults"
	"github.com/nyroway/nyro/go/internal/state"
)

const (
	envCTLServer = "NYRO_CTL_SERVER"
	envCTLToken  = "NYRO_CTL_TOKEN"
	httpTimeout  = 10 * time.Second
	defaultAddr  = defaults.ControlPlaneBaseURL
)

// ClientConfig holds the resolved Admin API endpoint and optional token.
type ClientConfig struct {
	ServerAddr     string
	Token          string
	explicitServer bool // true when address came from --server flag or env var
}

// ResolveClientConfig resolves the Admin API endpoint and token.
//
// Endpoint priority: flagServer > NYRO_CTL_SERVER > ~/.nyro/server.json > default.
// Token priority: flagToken > NYRO_CTL_TOKEN > ~/.nyro/server.json > empty.
func ResolveClientConfig(flagServer, flagToken string) (ClientConfig, error) {
	cfg := ClientConfig{}

	var stateToken string
	switch {
	case strings.TrimSpace(flagServer) != "":
		cfg.ServerAddr = strings.TrimRight(strings.TrimSpace(flagServer), "/")
		cfg.explicitServer = true
	case strings.TrimSpace(os.Getenv(envCTLServer)) != "":
		cfg.ServerAddr = strings.TrimRight(strings.TrimSpace(os.Getenv(envCTLServer)), "/")
		cfg.explicitServer = true
	default:
		st, err := state.Read()
		if err == nil {
			cfg.ServerAddr = st.AdminBaseURL()
			stateToken = st.AdminToken
		} else {
			// Fall back to the hard-coded default when no live state file
			// exists; connection errors are reported by DoRequest.
			cfg.ServerAddr = defaults.ControlPlaneBaseURL
		}
	}

	switch {
	case strings.TrimSpace(flagToken) != "":
		cfg.Token = strings.TrimSpace(flagToken)
	case strings.TrimSpace(os.Getenv(envCTLToken)) != "":
		cfg.Token = strings.TrimSpace(os.Getenv(envCTLToken))
	default:
		cfg.Token = stateToken
	}
	return cfg, nil
}

// connErr converts a low-level HTTP connection error into a user-friendly message
// that does not expose internal URLs or OS error details.
func connErr(cfg ClientConfig, err error) error {
	// Timeout (context deadline or http client timeout).
	if errors.Is(err, context.DeadlineExceeded) {
		return errors.New("nyro server did not respond (request timed out)")
	}
	var netErr *net.OpError
	if errors.As(err, &netErr) {
		if errors.Is(netErr.Err, net.ErrClosed) || isRefused(netErr) {
			return notRunningErr(cfg)
		}
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		if urlErr.Timeout() {
			return errors.New("nyro server did not respond (request timed out)")
		}
		if inner := new(net.OpError); errors.As(urlErr.Err, &inner) && isRefused(inner) {
			return notRunningErr(cfg)
		}
	}
	return errors.New("failed to reach nyro server")
}

func isRefused(e *net.OpError) bool {
	return e != nil && e.Err != nil && strings.Contains(e.Err.Error(), "connection refused")
}

func notRunningErr(cfg ClientConfig) error {
	if cfg.explicitServer {
		u, err := url.Parse(cfg.ServerAddr)
		addr := cfg.ServerAddr
		if err == nil {
			addr = u.Host
		}
		return fmt.Errorf("cannot reach nyro server at %s; verify the address or check server status", addr)
	}
	return errors.New("nyro server is not running; start it first with: nyro server")
}

// DoRequest sends an HTTP request to the Admin API. Non-2xx responses return
// an error that includes the status code and response body.
func DoRequest(ctx context.Context, method string, cfg ClientConfig, path string) ([]byte, error) {
	url := strings.TrimRight(cfg.ServerAddr, "/") + path
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	if cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.Token)
	}

	client := &http.Client{Timeout: httpTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, connErr(cfg, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("authentication failed, check --token or %s", envCTLToken)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(body))
		if msg == "" {
			msg = resp.Status
		}
		return nil, fmt.Errorf("server returned %d: %s", resp.StatusCode, msg)
	}
	return body, nil
}
