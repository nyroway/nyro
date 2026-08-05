package ctl

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nyroway/nyro/go/internal/state"
)

func TestResolveClientConfigFlagOverridesEnvAndState(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// Point state package at this HOME via UserHomeDir (uses HOME on Unix).
	if err := state.Write(state.ServerState{
		PID:       os.Getpid(),
		Listen:    "127.0.0.1:19999",
		StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("Write state: %v", err)
	}
	t.Setenv(envCTLServer, "http://env.example:1")
	t.Setenv(envCTLToken, "env-token")

	cfg, err := ResolveClientConfig("http://flag.example:2/", "flag-token")
	if err != nil {
		t.Fatalf("ResolveClientConfig: %v", err)
	}
	if cfg.ServerAddr != "http://flag.example:2" {
		t.Fatalf("ServerAddr = %q, want flag address", cfg.ServerAddr)
	}
	if cfg.Token != "flag-token" {
		t.Fatalf("Token = %q, want flag-token", cfg.Token)
	}
}

func TestResolveClientConfigEnvOverridesState(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := state.Write(state.ServerState{
		PID:       os.Getpid(),
		Listen:    "127.0.0.1:19999",
		StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("Write state: %v", err)
	}
	t.Setenv(envCTLServer, "http://env.example:3")
	t.Setenv(envCTLToken, "env-token")

	cfg, err := ResolveClientConfig("", "")
	if err != nil {
		t.Fatalf("ResolveClientConfig: %v", err)
	}
	if cfg.ServerAddr != "http://env.example:3" {
		t.Fatalf("ServerAddr = %q, want env address", cfg.ServerAddr)
	}
	if cfg.Token != "env-token" {
		t.Fatalf("Token = %q, want env-token", cfg.Token)
	}
}

func TestResolveClientConfigFromState(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(envCTLServer, "")
	t.Setenv(envCTLToken, "")
	if err := state.Write(state.ServerState{
		PID:        os.Getpid(),
		Listen:     "0.0.0.0:19531",
		StartedAt:  time.Now().UTC(),
		AdminToken: "state-token",
	}); err != nil {
		t.Fatalf("Write state: %v", err)
	}

	cfg, err := ResolveClientConfig("", "")
	if err != nil {
		t.Fatalf("ResolveClientConfig: %v", err)
	}
	if cfg.ServerAddr != "http://127.0.0.1:19531" {
		t.Fatalf("ServerAddr = %q, want rewritten loopback", cfg.ServerAddr)
	}
	if cfg.Token != "state-token" {
		t.Fatalf("Token = %q, want state-token", cfg.Token)
	}
}

func TestResolveClientConfigTokenFromState(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(envCTLServer, "")
	t.Setenv(envCTLToken, "")
	if err := state.Write(state.ServerState{
		PID:        os.Getpid(),
		Listen:     "127.0.0.1:19531",
		StartedAt:  time.Now().UTC(),
		AdminToken: "from-state",
	}); err != nil {
		t.Fatalf("Write state: %v", err)
	}

	cfg, err := ResolveClientConfig("", "")
	if err != nil {
		t.Fatalf("ResolveClientConfig: %v", err)
	}
	if cfg.Token != "from-state" {
		t.Fatalf("Token = %q, want from-state", cfg.Token)
	}
}

func TestResolveClientConfigFlagTokenBeatsState(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(envCTLServer, "")
	t.Setenv(envCTLToken, "")
	if err := state.Write(state.ServerState{
		PID:        os.Getpid(),
		Listen:     "127.0.0.1:19531",
		StartedAt:  time.Now().UTC(),
		AdminToken: "from-state",
	}); err != nil {
		t.Fatalf("Write state: %v", err)
	}

	cfg, err := ResolveClientConfig("", "flag-token")
	if err != nil {
		t.Fatalf("ResolveClientConfig: %v", err)
	}
	if cfg.Token != "flag-token" {
		t.Fatalf("Token = %q, want flag-token", cfg.Token)
	}
}

func TestResolveClientConfigEnvTokenBeatsState(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(envCTLServer, "")
	t.Setenv(envCTLToken, "env-token")
	if err := state.Write(state.ServerState{
		PID:        os.Getpid(),
		Listen:     "127.0.0.1:19531",
		StartedAt:  time.Now().UTC(),
		AdminToken: "from-state",
	}); err != nil {
		t.Fatalf("Write state: %v", err)
	}

	cfg, err := ResolveClientConfig("", "")
	if err != nil {
		t.Fatalf("ResolveClientConfig: %v", err)
	}
	if cfg.Token != "env-token" {
		t.Fatalf("Token = %q, want env-token", cfg.Token)
	}
}

func TestResolveClientConfigDefaultWhenNoState(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(envCTLServer, "")
	t.Setenv(envCTLToken, "")

	cfg, err := ResolveClientConfig("", "")
	if err != nil {
		t.Fatalf("ResolveClientConfig: %v", err)
	}
	if cfg.ServerAddr != defaultAddr {
		t.Fatalf("ServerAddr = %q, want %q", cfg.ServerAddr, defaultAddr)
	}
	_ = filepath.Join(home, ".nyro") // silence unused in some builds
}

func TestDoRequestSuccessAndAuth(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if r.URL.Path != "/api/v1/status" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
	}))
	defer srv.Close()

	body, err := DoRequest(context.Background(), "GET", ClientConfig{
		ServerAddr: srv.URL,
		Token:      "secret",
	}, "/api/v1/status")
	if err != nil {
		t.Fatalf("DoRequest: %v", err)
	}
	if gotAuth != "Bearer secret" {
		t.Fatalf("Authorization = %q, want Bearer secret", gotAuth)
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil || m["status"] != "ok" {
		t.Fatalf("body = %s, err=%v", body, err)
	}
}

func TestDoRequestUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer srv.Close()

	_, err := DoRequest(context.Background(), "GET", ClientConfig{ServerAddr: srv.URL}, "/api/v1/status")
	if err == nil || err.Error() != "authentication failed, check --token or NYRO_CTL_TOKEN" {
		t.Fatalf("DoRequest error = %v", err)
	}
}

func TestDoRequestNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream not found", http.StatusNotFound)
	}))
	defer srv.Close()

	_, err := DoRequest(context.Background(), "GET", ClientConfig{ServerAddr: srv.URL}, "/api/v1/upstreams/x")
	if err == nil {
		t.Fatal("expected error")
	}
	wantPrefix := "server returned 404:"
	if err.Error()[:len(wantPrefix)] != wantPrefix {
		t.Fatalf("error = %v, want prefix %q", err, wantPrefix)
	}
}
