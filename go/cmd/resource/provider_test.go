package resource

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mattn/go-runewidth"
	"github.com/spf13/cobra"

	"github.com/nyroway/nyro/go/internal/envflag"
)

func newProviderTestRoot(t *testing.T, args ...string) (*cobra.Command, *bytes.Buffer) {
	t.Helper()
	root := &cobra.Command{
		Use:               "nyro",
		PersistentPreRunE: envflag.Bind,
		SilenceUsage:      true,
		SilenceErrors:     true,
	}
	root.AddCommand(ProviderCmd())
	envflag.Decorate(root)
	output := new(bytes.Buffer)
	root.SetOut(output)
	root.SetErr(output)
	root.SetArgs(args)
	return root, output
}

func TestProviderCommandShape(t *testing.T) {
	cmd := ProviderCmd()
	if cmd.Name() != "provider" {
		t.Fatalf("command name = %q, want provider", cmd.Name())
	}
	if len(cmd.Aliases) != 1 || cmd.Aliases[0] != "prov" {
		t.Fatalf("aliases = %v, want [prov]", cmd.Aliases)
	}
	if len(cmd.Commands()) != 5 {
		t.Fatalf("subcommands = %v, want create, ls, rm, show and update", cmd.Commands())
	}
	for _, available := range []string{"create", "ls", "rm", "show", "update"} {
		if found, _, err := cmd.Find([]string{available}); err != nil || found.Name() != available {
			t.Fatalf("expected %q subcommand, found %v, err %v", available, found, err)
		}
	}
	if found, _, err := cmd.Find([]string{"remove"}); err != nil || found.Name() != "rm" {
		t.Fatalf("expected remove alias for rm, found %v, err %v", found, err)
	}
}

func TestProviderAliasResolves(t *testing.T) {
	root, _ := newProviderTestRoot(t)
	found, remaining, err := root.Find([]string{"prov", "ls"})
	if err != nil {
		t.Fatalf("find alias: %v", err)
	}
	if found.Name() != "ls" || len(remaining) != 0 {
		t.Fatalf("Find(prov ls) = %s, remaining %v", found.Name(), remaining)
	}
}

func TestProviderListUsesExplicitServerAndExpectedRequest(t *testing.T) {
	var method, path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"id":         "abc123def456789",
			"name":       "openai-prod",
			"provider":   "openai",
			"protocol":   "openai-chatcompletions",
			"enabled":    true,
			"updated_at": time.Now().UTC().Format(time.RFC3339),
		}})
	}))
	defer server.Close()

	root, output := newProviderTestRoot(t, "provider", "ls", "--server", server.URL+"/")
	if err := root.Execute(); err != nil {
		t.Fatalf("execute provider ls: %v", err)
	}
	if method != http.MethodGet {
		t.Fatalf("method = %q, want GET", method)
	}
	if path != providerListPath {
		t.Fatalf("path = %q, want %q", path, providerListPath)
	}
	for _, want := range []string{
		"ID",
		"NAME",
		"PROVIDER",
		"PROTOCOL",
		"ENABLED",
		"UPDATED",
		"abc123def456789",
		"openai-prod",
		"openai",
		"openai-chatcompletions",
		"true",
	} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("output %q does not contain %q", output.String(), want)
		}
	}
}

func TestProviderListUsesEnvironmentServer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("[]"))
	}))
	defer server.Close()
	t.Setenv("NYRO_PROVIDER_LS_SERVER", server.URL)

	root, output := newProviderTestRoot(t, "provider", "ls")
	if err := root.Execute(); err != nil {
		t.Fatalf("execute provider ls: %v", err)
	}
	if !strings.Contains(output.String(), "NAME") {
		t.Fatalf("output = %q, want table header", output.String())
	}
}

func TestProviderShowByNameDisplaysDetailsWithoutCredential(t *testing.T) {
	var method, path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		_, _ = w.Write([]byte(`[{
			"id":"provider-id-123",
			"name":"openai-prod",
			"provider":"openai",
			"protocol":"openai-chatcompletions",
			"base_url":"https://api.openai.com/v1",
			"credentials":{"api_key":"sk-secret"},
			"models":["gpt-4o"],
			"models_url":"",
			"proxy_url":"http://proxy.example.com",
			"enabled":true,
			"created_at":"2026-08-01T10:00:00Z",
			"updated_at":"2026-08-07T10:00:00Z"
		}]`))
	}))
	defer server.Close()

	root, output := newProviderTestRoot(t, "provider", "show", "openai-prod", "--server", server.URL)
	if err := root.Execute(); err != nil {
		t.Fatalf("execute provider show: %v", err)
	}
	if method != http.MethodGet || path != providerListPath {
		t.Fatalf("request = %s %s, want GET %s", method, path, providerListPath)
	}
	for _, want := range []string{
		"ID:", "provider-id-123",
		"Name:", "openai-prod",
		"Provider:", "openai",
		"Protocol:", "openai-chatcompletions",
		"Base URL:", "https://api.openai.com/v1",
		"Credentials:", "configured",
		"Models:", `["gpt-4o"]`,
		"Proxy URL:", "http://proxy.example.com",
		"Enabled:", "true",
		"Created:", "2026-08-01T10:00:00Z",
		"Updated:", "2026-08-07T10:00:00Z",
	} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("output %q does not contain %q", output.String(), want)
		}
	}
	if strings.Contains(output.String(), "sk-secret") {
		t.Errorf("output exposes provider credential: %q", output.String())
	}
}

func TestProviderShowSupportsIDAndEnvironmentServer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"id":"provider-id-123","name":"openai-prod","enabled":true}]`))
	}))
	defer server.Close()
	t.Setenv("NYRO_PROVIDER_SHOW_SERVER", server.URL)

	root, output := newProviderTestRoot(t, "provider", "show", "provider-id-123")
	if err := root.Execute(); err != nil {
		t.Fatalf("execute provider show: %v", err)
	}
	if !strings.Contains(output.String(), "openai-prod") {
		t.Fatalf("output = %q, want provider details", output.String())
	}
}

func TestProviderShowReturnsNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("[]"))
	}))
	defer server.Close()

	root, _ := newProviderTestRoot(t, "provider", "show", "missing", "--server", server.URL)
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), `provider "missing" not found`) {
		t.Fatalf("error = %v, want provider not found", err)
	}
}

func TestProviderShowRequiresOneIdentifier(t *testing.T) {
	for _, args := range [][]string{
		{"provider", "show"},
		{"provider", "show", "one", "two"},
	} {
		root, _ := newProviderTestRoot(t, args...)
		err := root.Execute()
		if err == nil {
			t.Fatalf("args %v: expected argument error", args)
		}
		if !strings.Contains(err.Error(), "Usage:") || !strings.Contains(err.Error(), "provider show") {
			t.Fatalf("args %v: error = %v, want usage hint", args, err)
		}
	}
}

func TestProviderListFlagOverridesEnvironment(t *testing.T) {
	explicitCalled := false
	explicit := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		explicitCalled = true
		_, _ = w.Write([]byte("[]"))
	}))
	defer explicit.Close()
	environment := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("environment server should not be called when --server is explicit")
		_, _ = w.Write([]byte("[]"))
	}))
	defer environment.Close()
	t.Setenv("NYRO_PROVIDER_LS_SERVER", environment.URL)

	root, _ := newProviderTestRoot(t, "provider", "ls", "--server", explicit.URL)
	if err := root.Execute(); err != nil {
		t.Fatalf("execute provider ls: %v", err)
	}
	if !explicitCalled {
		t.Fatal("explicit server was not called")
	}
}

func TestProviderListDefaultServer(t *testing.T) {
	cmd := ProviderCmd()
	ls, _, err := cmd.Find([]string{"ls"})
	if err != nil {
		t.Fatalf("find ls: %v", err)
	}
	flag := ls.Flags().Lookup("server")
	if flag == nil {
		t.Fatal("--server flag missing")
	}
	if flag.DefValue != defaultProviderServer {
		t.Fatalf("default server = %q, want %q", flag.DefValue, defaultProviderServer)
	}
}

func TestProviderCreateUsesPresetDefaultsAndTestsBeforeCreate(t *testing.T) {
	const apiKey = "sk-super-secret"
	var calls []string
	var tested, created createProviderRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		switch r.URL.Path {
		case providerPresetsPath:
			_, _ = w.Write([]byte(`[{
				"id":"openai",
				"name":"OpenAI",
				"default_protocol":"openai-chatcompletions",
				"protocols":[
					{"id":"openai-chatcompletions","base_url":"https://api.openai.com/v1"},
					{"id":"openai-responses","base_url":"https://api.openai.com/v1"}
				],
				"credentials":{"fields":[{"name":"api_key","required":true}]},
				"models_url":"https://api.openai.com/v1/models"
			}]`))
		case providerDraftTestPath:
			if err := json.NewDecoder(r.Body).Decode(&tested); err != nil {
				t.Errorf("decode test body: %v", err)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(
				"data: {\"type\":\"check\",\"check\":\"config\",\"status\":\"passed\",\"message\":\"Configuration is valid\"}\n\n" +
					"data: {\"type\":\"complete\",\"success\":true}\n\n",
			))
		case providerListPath:
			if err := json.NewDecoder(r.Body).Decode(&created); err != nil {
				t.Errorf("decode create body: %v", err)
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{
				"id":"provider-id-123",
				"name":"OpenAI",
				"provider":"openai",
				"protocol":"openai-chatcompletions",
				"base_url":"https://api.openai.com/v1",
				"credentials":{"api_key":"sk-super-secret"},
				"models_url":"https://api.openai.com/v1/models",
				"enabled":true
			}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	root, output := newProviderTestRoot(t, "provider", "create", "openai", apiKey, "--server", server.URL)
	if err := root.Execute(); err != nil {
		t.Fatalf("execute provider create: %v", err)
	}
	wantCalls := []string{
		"GET " + providerPresetsPath,
		"POST " + providerDraftTestPath,
		"POST " + providerListPath,
	}
	if strings.Join(calls, ",") != strings.Join(wantCalls, ",") {
		t.Fatalf("calls = %v, want %v", calls, wantCalls)
	}
	for stage, input := range map[string]createProviderRequest{"test": tested, "create": created} {
		if input.Name != "OpenAI" ||
			input.Provider != "openai" ||
			input.Protocol != "openai-chatcompletions" ||
			input.BaseURL != "https://api.openai.com/v1" ||
			input.ModelsURL != "https://api.openai.com/v1/models" ||
			input.Credentials["api_key"] != apiKey ||
			!input.Enabled {
			t.Errorf("%s input = %+v, want preset defaults and API key", stage, input)
		}
	}
	for _, want := range []string{"✓ config", "provider-id-123", "OpenAI"} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("output %q does not contain %q", output.String(), want)
		}
	}
	if strings.Contains(output.String(), apiKey) {
		t.Fatalf("output exposes API key: %q", output.String())
	}
}

func TestProviderCreateDoesNotCreateWhenTestFails(t *testing.T) {
	createCalled := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case providerPresetsPath:
			_, _ = w.Write([]byte(`[{
				"id":"openai",
				"name":"OpenAI",
				"default_protocol":"openai-chatcompletions",
				"protocols":[{"id":"openai-chatcompletions","base_url":"https://api.openai.com/v1"}],
				"credentials":{"fields":[{"name":"api_key","required":true}]},
				"models_url":"https://api.openai.com/v1/models"
			}]`))
		case providerDraftTestPath:
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(
				"data: {\"type\":\"check\",\"check\":\"model_request\",\"status\":\"failed\",\"error\":\"invalid credentials\"}\n\n" +
					"data: {\"type\":\"complete\",\"success\":false,\"error\":\"invalid credentials\"}\n\n",
			))
		case providerListPath:
			createCalled = true
			t.Error("create endpoint must not be called after a failed test")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	root, output := newProviderTestRoot(t, "provider", "create", "openai", "secret", "--server", server.URL)
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "invalid credentials") {
		t.Fatalf("error = %v, want test failure", err)
	}
	if createCalled {
		t.Fatal("create endpoint was called")
	}
	if strings.Contains(output.String(), "secret") || strings.Contains(err.Error(), "secret") {
		t.Fatalf("API key leaked; output=%q error=%q", output.String(), err.Error())
	}
}

func TestBuildCreateProviderRequestSupportsOverrides(t *testing.T) {
	presets := []providerPreset{{
		ID:              "openai",
		Name:            "OpenAI",
		DefaultProtocol: "openai-chatcompletions",
		Protocols: []providerPresetProtocol{
			{ID: "openai-chatcompletions", BaseURL: "https://api.openai.com/v1"},
			{ID: "openai-responses", BaseURL: "https://api.openai.com/v1"},
		},
		ModelsURL: "https://api.openai.com/v1/models",
	}}
	got, err := buildCreateProviderRequest(presets, createProviderOptions{
		Provider:      "OpenAI",
		APIKey:        "secret",
		Name:          "openai-prod",
		Protocol:      "openai-responses",
		BaseURL:       "https://gateway.example.com/v1/",
		Models:        []string{"gpt-4o", "gpt-4o", " gpt-4o-mini "},
		ProxyURL:      "http://127.0.0.1:7890",
		Enabled:       false,
		ModelsChanged: true,
	})
	if err != nil {
		t.Fatalf("buildCreateProviderRequest: %v", err)
	}
	if got.Name != "openai-prod" ||
		got.Protocol != "openai-responses" ||
		got.BaseURL != "https://gateway.example.com/v1" ||
		got.ModelsURL != "" ||
		got.ProxyURL != "http://127.0.0.1:7890" ||
		got.Enabled {
		t.Fatalf("overridden input = %+v", got)
	}
	if len(got.Models) != 2 || got.Models[0] != "gpt-4o" || got.Models[1] != "gpt-4o-mini" {
		t.Fatalf("models = %v, want normalized unique values", got.Models)
	}
}

func TestProviderCreateValidatesArgumentsAndModelSource(t *testing.T) {
	tests := [][]string{
		{"provider", "create"},
		{"provider", "create", "openai"},
		{"provider", "create", "openai", ""},
		{"provider", "create", "openai", "secret", "extra"},
		{"provider", "create", "openai", "secret", "--model", "gpt-4o", "--models-url", "https://example.com/models"},
	}
	for _, args := range tests {
		root, _ := newProviderTestRoot(t, args...)
		if err := root.Execute(); err == nil {
			t.Fatalf("args %v: expected validation error", args)
		}
	}
}

func TestProviderCreateListsAvailableProvidersWhenArgsMissing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != providerPresetsPath {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`[
			{"id":"anthropic","name":"Anthropic","priority":1,"default_protocol":"anthropic-messages","protocols":[{"id":"anthropic-messages","base_url":"https://api.anthropic.com"}]},
			{"id":"openai","name":"OpenAI","priority":2,"default_protocol":"openai-chatcompletions","protocols":[{"id":"openai-chatcompletions","base_url":"https://api.openai.com/v1"}]}
		]`))
	}))
	defer server.Close()

	root, _ := newProviderTestRoot(t, "provider", "create", "--server", server.URL)
	err := root.Execute()
	if err == nil {
		t.Fatal("expected argument error")
	}
	for _, want := range []string{
		"Usage:",
		"provider create",
		"Arguments:",
		"provider",
		"api-key",
		"Flags:",
		"--name",
		"--protocol",
		"--base-url",
		"--models-url",
		"--model",
		"--proxy-url",
		"--enabled",
		"--server",
		"Available providers:",
		"anthropic",
		"Anthropic",
		"openai",
		"OpenAI",
		"custom",
		"Custom",
		"Available protocols:",
		"openai-chatcompletions",
		"OpenAI Chat Completions",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %v, want it to contain %q", err, want)
		}
	}
}

func TestProviderCreateListsProviderProtocolsWhenAPIKeyMissing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != providerPresetsPath {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`[
			{"id":"openai","name":"OpenAI","priority":2,"default_protocol":"openai-chatcompletions","protocols":[
				{"id":"openai-chatcompletions","base_url":"https://api.openai.com/v1"},
				{"id":"openai-responses","base_url":"https://api.openai.com/v1"}
			]}
		]`))
	}))
	defer server.Close()

	root, _ := newProviderTestRoot(t, "provider", "create", "openai", "--server", server.URL)
	err := root.Execute()
	if err == nil {
		t.Fatal("expected argument error")
	}
	for _, want := range []string{
		"Available protocols:",
		"openai-chatcompletions",
		"openai-responses",
		"OpenAI Responses",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %v, want it to contain %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "anthropic-messages") {
		t.Fatalf("openai-scoped protocol list should not include anthropic-messages: %v", err)
	}
}

func TestProviderCreateRejectsUnsupportedProtocolWithHint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != providerPresetsPath {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`[
			{"id":"openai","name":"OpenAI","priority":2,"default_protocol":"openai-chatcompletions","protocols":[
				{"id":"openai-chatcompletions","base_url":"https://api.openai.com/v1"},
				{"id":"openai-responses","base_url":"https://api.openai.com/v1"}
			]}
		]`))
	}))
	defer server.Close()

	root, _ := newProviderTestRoot(
		t,
		"provider", "create", "openai", "sk-test",
		"--protocol", "anthropic-messages",
		"--server", server.URL,
	)
	err := root.Execute()
	if err == nil {
		t.Fatal("expected unsupported protocol error")
	}
	for _, want := range []string{
		`protocol "anthropic-messages" is not supported by provider "openai"`,
		"Available protocols:",
		"openai-chatcompletions",
		"openai-responses",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %v, want it to contain %q", err, want)
		}
	}
}

func TestProviderCreateUnknownProviderListsAvailableProviders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != providerPresetsPath {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`[
			{"id":"openai","name":"OpenAI","priority":2,"default_protocol":"openai-chatcompletions","protocols":[{"id":"openai-chatcompletions","base_url":"https://api.openai.com/v1"}]}
		]`))
	}))
	defer server.Close()

	root, _ := newProviderTestRoot(t, "provider", "create", "not-a-provider", "sk-test", "--server", server.URL)
	err := root.Execute()
	if err == nil {
		t.Fatal("expected unknown provider error")
	}
	for _, want := range []string{
		`unknown provider "not-a-provider"`,
		"Available providers:",
		"openai",
		"custom",
		"Arguments:",
		"Flags:",
		"--protocol",
		"--base-url",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %v, want it to contain %q", err, want)
		}
	}
}

func TestFormatAvailableProvidersIncludesCustom(t *testing.T) {
	got := formatAvailableProviders([]providerPreset{
		{ID: "openai", Name: "OpenAI"},
		{ID: "anthropic", Name: "Anthropic"},
	})
	for _, want := range []string{"openai", "OpenAI", "anthropic", "Anthropic", "custom", "Custom"} {
		if !strings.Contains(got, want) {
			t.Fatalf("format = %q, want it to contain %q", got, want)
		}
	}
}

func TestProtocolOptionsForProviderUsesPresetProtocols(t *testing.T) {
	presets := []providerPreset{{
		ID: "openai",
		Protocols: []providerPresetProtocol{
			{ID: "openai-chatcompletions"},
			{ID: "openai-responses"},
		},
	}}
	got := protocolOptionsForProvider(presets, "openai")
	if len(got) != 2 || got[0].ID != "openai-chatcompletions" || got[1].ID != "openai-responses" {
		t.Fatalf("protocol options = %+v", got)
	}
	if got[0].Name != "OpenAI Chat Completions" {
		t.Fatalf("display name = %q", got[0].Name)
	}
}

func TestProviderRemoveDeletesByName(t *testing.T) {
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == providerListPath:
			_, _ = w.Write([]byte(`[{
				"id":"provider-id-123",
				"name":"openai-prod",
				"provider":"openai",
				"protocol":"openai-chatcompletions",
				"enabled":true
			}]`))
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v1/upstreams/provider-id-123":
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	root, output := newProviderTestRoot(t, "provider", "rm", "openai-prod", "--server", server.URL)
	if err := root.Execute(); err != nil {
		t.Fatalf("execute provider rm: %v", err)
	}
	wantCalls := []string{
		"GET " + providerListPath,
		"DELETE /api/v1/upstreams/provider-id-123",
	}
	if strings.Join(calls, ",") != strings.Join(wantCalls, ",") {
		t.Fatalf("calls = %v, want %v", calls, wantCalls)
	}
	if !strings.Contains(output.String(), `Deleted provider "openai-prod" (provider-id-123)`) {
		t.Fatalf("output = %q, want delete confirmation", output.String())
	}
}

func TestProviderRemoveSupportsIDAndRemoveAlias(t *testing.T) {
	deleted := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == providerListPath:
			_, _ = w.Write([]byte(`[{"id":"provider-id-123","name":"openai-prod","enabled":true}]`))
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v1/upstreams/provider-id-123":
			deleted = true
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	root, _ := newProviderTestRoot(t, "provider", "remove", "provider-id-123", "--server", server.URL)
	if err := root.Execute(); err != nil {
		t.Fatalf("execute provider remove: %v", err)
	}
	if !deleted {
		t.Fatal("delete endpoint was not called")
	}
}

func TestProviderRemoveReturnsNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("[]"))
	}))
	defer server.Close()

	root, _ := newProviderTestRoot(t, "provider", "rm", "missing", "--server", server.URL)
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), `provider "missing" not found`) {
		t.Fatalf("error = %v, want provider not found", err)
	}
}

func TestProviderUpdateTestsThenUpdates(t *testing.T) {
	const apiKey = "sk-updated-secret"
	var calls []string
	var tested createProviderRequest
	var updated map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == providerListPath:
			_, _ = w.Write([]byte(`[{
				"id":"provider-id-123",
				"name":"openai-prod",
				"provider":"openai",
				"protocol":"openai-chatcompletions",
				"base_url":"https://api.openai.com/v1",
				"credentials":{"api_key":"sk-old"},
				"models_url":"https://api.openai.com/v1/models",
				"enabled":true
			}]`))
		case r.Method == http.MethodGet && r.URL.Path == providerPresetsPath:
			_, _ = w.Write([]byte(`[
				{"id":"openai","name":"OpenAI","default_protocol":"openai-chatcompletions","protocols":[
					{"id":"openai-chatcompletions","base_url":"https://api.openai.com/v1"},
					{"id":"openai-responses","base_url":"https://api.openai.com/v1"}
				]}
			]`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/upstreams/provider-id-123/test-draft/stream":
			if err := json.NewDecoder(r.Body).Decode(&tested); err != nil {
				t.Errorf("decode test body: %v", err)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(
				"data: {\"type\":\"check\",\"check\":\"config\",\"status\":\"passed\",\"message\":\"Configuration is valid\"}\n\n" +
					"data: {\"type\":\"complete\",\"success\":true}\n\n",
			))
		case r.Method == http.MethodPut && r.URL.Path == "/api/v1/upstreams/provider-id-123":
			if err := json.NewDecoder(r.Body).Decode(&updated); err != nil {
				t.Errorf("decode update body: %v", err)
			}
			_, _ = w.Write([]byte(`{
				"id":"provider-id-123",
				"name":"openai-staging",
				"provider":"openai",
				"protocol":"openai-responses",
				"base_url":"https://gateway.example.com/v1",
				"credentials":{"api_key":"sk-updated-secret"},
				"models_url":"https://gateway.example.com/v1/models",
				"enabled":false
			}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	root, output := newProviderTestRoot(
		t,
		"provider", "update", "openai-prod",
		"--name", "openai-staging",
		"--protocol", "openai-responses",
		"--base-url", "https://gateway.example.com/v1/",
		"--models-url", "https://gateway.example.com/v1/models",
		"--api-key", apiKey,
		"--enabled=false",
		"--server", server.URL,
	)
	if err := root.Execute(); err != nil {
		t.Fatalf("execute provider update: %v", err)
	}
	wantCalls := []string{
		"GET " + providerListPath,
		"GET " + providerPresetsPath,
		"POST /api/v1/upstreams/provider-id-123/test-draft/stream",
		"PUT /api/v1/upstreams/provider-id-123",
	}
	if strings.Join(calls, ",") != strings.Join(wantCalls, ",") {
		t.Fatalf("calls = %v, want %v", calls, wantCalls)
	}
	if tested.Name != "openai-staging" ||
		tested.Protocol != "openai-responses" ||
		tested.BaseURL != "https://gateway.example.com/v1" ||
		tested.ModelsURL != "https://gateway.example.com/v1/models" ||
		tested.Credentials["api_key"] != apiKey ||
		tested.Enabled {
		t.Fatalf("test draft = %+v", tested)
	}
	if updated["name"] != "openai-staging" ||
		updated["protocol"] != "openai-responses" ||
		updated["base_url"] != "https://gateway.example.com/v1" ||
		updated["models_url"] != "https://gateway.example.com/v1/models" ||
		updated["enabled"] != false {
		t.Fatalf("update body = %#v", updated)
	}
	for _, want := range []string{"✓ config", "openai-staging", "provider-id-123"} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("output %q does not contain %q", output.String(), want)
		}
	}
	if strings.Contains(output.String(), apiKey) {
		t.Fatalf("output exposes API key: %q", output.String())
	}
}

func TestProviderUpdateDoesNotUpdateWhenTestFails(t *testing.T) {
	updateCalled := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == providerListPath:
			_, _ = w.Write([]byte(`[{
				"id":"provider-id-123",
				"name":"openai-prod",
				"provider":"openai",
				"protocol":"openai-chatcompletions",
				"base_url":"https://api.openai.com/v1",
				"credentials":{"api_key":"sk-old"},
				"models_url":"https://api.openai.com/v1/models",
				"enabled":true
			}]`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/upstreams/provider-id-123/test-draft/stream":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(
				"data: {\"type\":\"check\",\"check\":\"model_request\",\"status\":\"failed\",\"error\":\"invalid credentials\"}\n\n" +
					"data: {\"type\":\"complete\",\"success\":false,\"error\":\"invalid credentials\"}\n\n",
			))
		case r.Method == http.MethodPut:
			updateCalled = true
			t.Error("update endpoint must not be called after a failed test")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	root, _ := newProviderTestRoot(t, "provider", "update", "openai-prod", "--api-key", "secret", "--server", server.URL)
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "invalid credentials") {
		t.Fatalf("error = %v, want test failure", err)
	}
	if updateCalled {
		t.Fatal("update endpoint was called")
	}
}

func TestProviderUpdateRequiresFlags(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case providerListPath:
			_, _ = w.Write([]byte(`[{
				"id":"provider-id-123",
				"name":"openai-prod",
				"provider":"openai",
				"protocol":"openai-chatcompletions",
				"enabled":true
			}]`))
		case providerPresetsPath:
			_, _ = w.Write([]byte(`[
				{"id":"openai","name":"OpenAI","default_protocol":"openai-chatcompletions","protocols":[
					{"id":"openai-chatcompletions","base_url":"https://api.openai.com/v1"},
					{"id":"openai-responses","base_url":"https://api.openai.com/v1"}
				]}
			]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	root, _ := newProviderTestRoot(t, "provider", "update", "openai-prod", "--server", server.URL)
	err := root.Execute()
	if err == nil {
		t.Fatal("expected missing flags error")
	}
	for _, want := range []string{
		"at least one update flag is required",
		"Usage:",
		"provider update",
		"--name",
		"--api-key",
		"--protocol",
		"--base-url",
		"--models-url",
		"--model",
		"--proxy-url",
		"--enabled",
		"Current protocol: openai-chatcompletions",
		"Available protocols:",
		"openai-chatcompletions",
		"openai-responses",
		"OpenAI Responses",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %v, want it to contain %q", err, want)
		}
	}
}

func TestProviderUpdateRejectsUnknownProtocolWithHint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case providerListPath:
			_, _ = w.Write([]byte(`[{
				"id":"provider-id-123",
				"name":"openai-prod",
				"provider":"openai",
				"protocol":"openai-chatcompletions",
				"enabled":true
			}]`))
		case providerPresetsPath:
			_, _ = w.Write([]byte(`[
				{"id":"openai","name":"OpenAI","default_protocol":"openai-chatcompletions","protocols":[
					{"id":"openai-chatcompletions","base_url":"https://api.openai.com/v1"},
					{"id":"openai-responses","base_url":"https://api.openai.com/v1"}
				]}
			]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	root, _ := newProviderTestRoot(
		t,
		"provider", "update", "openai-prod",
		"--protocol", "not-a-protocol",
		"--server", server.URL,
	)
	err := root.Execute()
	if err == nil {
		t.Fatal("expected unknown protocol error")
	}
	for _, want := range []string{
		`unknown protocol "not-a-protocol"`,
		"Current protocol: openai-chatcompletions",
		"Available protocols:",
		"openai-chatcompletions",
		"openai-responses",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %v, want it to contain %q", err, want)
		}
	}
}

func TestBuildUpdateProviderRequestSwitchesToStaticModels(t *testing.T) {
	existing := providerRow{
		ID:              "provider-id-123",
		Name:            "openai-prod",
		Provider:        "openai",
		Protocol:        "openai-chatcompletions",
		BaseURL:         "https://api.openai.com/v1",
		CredentialsJSON: json.RawMessage(`{"api_key":"sk-old"}`),
		ModelsURL:       "https://api.openai.com/v1/models",
		Enabled:         true,
	}
	draft, body, err := buildUpdateProviderRequest(existing, updateProviderOptions{
		Models:        []string{"gpt-4o", "gpt-4o-mini"},
		ModelsChanged: true,
	})
	if err != nil {
		t.Fatalf("buildUpdateProviderRequest: %v", err)
	}
	if draft.ModelsURL != "" || len(draft.Models) != 2 {
		t.Fatalf("draft = %+v", draft)
	}
	if body["models_url"] != "" {
		t.Fatalf("update body models_url = %#v, want empty string", body["models_url"])
	}
	models, ok := body["models"].([]string)
	if !ok || len(models) != 2 {
		t.Fatalf("update body models = %#v", body["models"])
	}
}

func TestProviderListEmptyResponseOnlyPrintsHeader(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("[]"))
	}))
	defer server.Close()

	root, output := newProviderTestRoot(t, "provider", "ls", "--server", server.URL)
	if err := root.Execute(); err != nil {
		t.Fatalf("execute provider ls: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("output lines = %d, want header only; output=%q", len(lines), output.String())
	}
}

func TestProviderListAlignsLongAndWideContent(t *testing.T) {
	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	var output bytes.Buffer
	err := writeProviders(&output, []providerRow{{
		ID:        "provider-id-123456789",
		Name:      "这是一个非常长的供应商名称",
		Provider:  "custom-vendor",
		Protocol:  "anthropic-messages",
		Enabled:   true,
		UpdatedAt: now.Add(-2 * time.Hour).Format(time.RFC3339),
	}}, now)
	if err != nil {
		t.Fatalf("writeProviders: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("output lines = %d, want 2; output=%q", len(lines), output.String())
	}
	headerValues := []string{"NAME", "PROVIDER", "PROTOCOL", "ENABLED", "UPDATED"}
	rowValues := []string{"这是一个非常长的供应商名称", "custom-vendor", "anthropic-messages", "true", "2 hours ago"}
	for i := range headerValues {
		headerColumn := displayColumn(t, lines[0], headerValues[i])
		rowColumn := displayColumn(t, lines[1], rowValues[i])
		if headerColumn != rowColumn {
			t.Errorf(
				"column %s starts at display column %d in header and %d in row; output=%q",
				headerValues[i],
				headerColumn,
				rowColumn,
				output.String(),
			)
		}
	}
}

func displayColumn(t *testing.T, line, value string) int {
	t.Helper()
	index := strings.Index(line, value)
	if index < 0 {
		t.Fatalf("value %q not found in line %q", value, line)
	}
	return runewidth.StringWidth(line[:index])
}

func TestProviderListAcceptsAddressWithoutScheme(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("[]"))
	}))
	defer server.Close()

	root, _ := newProviderTestRoot(t, "provider", "ls", "--server", strings.TrimPrefix(server.URL, "http://"))
	if err := root.Execute(); err != nil {
		t.Fatalf("execute provider ls: %v", err)
	}
}

func TestProviderListRejectsInvalidServer(t *testing.T) {
	tests := []string{"", "ftp://example.com", "http://", "http://example.com?q=1"}
	for _, server := range tests {
		t.Run(server, func(t *testing.T) {
			root, _ := newProviderTestRoot(t, "provider", "ls", "--server", server)
			if err := root.Execute(); err == nil {
				t.Fatalf("server %q: expected error", server)
			}
		})
	}
}

func TestProviderListReturnsHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not available", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	root, _ := newProviderTestRoot(t, "provider", "ls", "--server", server.URL)
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "server returned 503: not available") {
		t.Fatalf("error = %v, want HTTP 503 detail", err)
	}
}

func TestProviderListRejectsInvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("{not-json"))
	}))
	defer server.Close()

	root, _ := newProviderTestRoot(t, "provider", "ls", "--server", server.URL)
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "decode providers response") {
		t.Fatalf("error = %v, want decode error", err)
	}
}

func TestProviderListTimeout(t *testing.T) {
	previous := providerHTTPClient
	providerHTTPClient = &http.Client{Timeout: 20 * time.Millisecond}
	t.Cleanup(func() { providerHTTPClient = previous })

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(100 * time.Millisecond)
		_, _ = w.Write([]byte("[]"))
	}))
	defer server.Close()

	root, _ := newProviderTestRoot(t, "provider", "ls", "--server", server.URL)
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "request timed out") {
		t.Fatalf("error = %v, want timeout error", err)
	}
}

func TestRedactProviderSecret(t *testing.T) {
	secret := "sk-super-secret"
	err := redactProviderSecret(fmt.Errorf("invalid key: %s", secret), secret)
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("secret not redacted: %v", err)
	}
	if !strings.Contains(err.Error(), "[REDACTED]") {
		t.Fatalf("expected [REDACTED] in error: %v", err)
	}

	if redactProviderSecret(nil, secret) != nil {
		t.Fatal("nil error should remain nil")
	}
	orig := fmt.Errorf("no secret here")
	if redactProviderSecret(orig, "") != orig {
		t.Fatal("empty secret should return original error unchanged")
	}
}

func TestBuildUpdateProviderRequestRejectsExplicitEmptyBaseURL(t *testing.T) {
	existing := providerRow{
		ID:      "provider-id-123",
		Name:    "openai-prod",
		BaseURL: "https://api.openai.com/v1",
	}
	_, _, err := buildUpdateProviderRequest(existing, updateProviderOptions{
		BaseURL:        "",
		BaseURLChanged: true,
	})
	if err == nil || !strings.Contains(err.Error(), "base-url cannot be empty") {
		t.Fatalf("error = %v, want base-url cannot be empty", err)
	}
}

func TestBuildUpdateProviderRequestExistingEmptyBaseURL(t *testing.T) {
	existing := providerRow{
		ID:   "provider-id-123",
		Name: "openai-prod",
		// BaseURL intentionally empty to trigger the late guard
	}
	_, _, err := buildUpdateProviderRequest(existing, updateProviderOptions{
		Enabled:        true,
		EnabledChanged: true,
	})
	if err == nil || !strings.Contains(err.Error(), "base-url cannot be empty") {
		t.Fatalf("error = %v, want base-url cannot be empty", err)
	}
}

func TestTestProviderDraftRejectsInvalidSSEJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {not-valid-json}\n\n"))
	}))
	defer server.Close()

	input := createProviderRequest{Name: "test", Provider: "openai", BaseURL: "https://example.com"}
	err := testProviderDraft(context.Background(), server.Client(), server.URL, "/", input, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "decode provider test event") {
		t.Fatalf("error = %v, want decode error", err)
	}
}

func TestTestProviderDraftNoCompletionEvent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		// Stream ends without a "complete" event
		_, _ = w.Write([]byte("data: {\"type\":\"check\",\"check\":\"config\",\"status\":\"passed\",\"message\":\"ok\"}\n\n"))
	}))
	defer server.Close()

	input := createProviderRequest{Name: "test", Provider: "openai", BaseURL: "https://example.com"}
	err := testProviderDraft(context.Background(), server.Client(), server.URL, "/", input, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "did not return a successful completion event") {
		t.Fatalf("error = %v, want missing completion event error", err)
	}
}

func TestProviderCredentialsAndModelsHandleInvalidJSON(t *testing.T) {
	badJSON := json.RawMessage(`{invalid`)

	creds := providerCredentials(providerRow{CredentialsJSON: badJSON})
	if len(creds) != 0 {
		t.Fatalf("expected empty map for invalid credentials JSON, got %v", creds)
	}

	models := providerModels(providerRow{ModelsJSON: badJSON})
	if models != nil {
		t.Fatalf("expected nil for invalid models JSON, got %v", models)
	}
}

func TestNormalizeProviderServerHTTPS(t *testing.T) {
	got, err := normalizeProviderServer("https://example.com/path/")
	if err != nil {
		t.Fatalf("normalizeProviderServer: %v", err)
	}
	if got != "https://example.com/path" {
		t.Fatalf("normalized = %q, want trailing slash trimmed", got)
	}
}

func TestProviderFormattingHelpers(t *testing.T) {
	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		value string
		want  string
	}{
		{"", "-"},
		{"invalid", "invalid"},
		{now.Add(-30 * time.Second).Format(time.RFC3339), "just now"},
		{now.Add(-time.Minute).Format(time.RFC3339), "1 minute ago"},
		{now.Add(-2 * time.Hour).Format(time.RFC3339), "2 hours ago"},
		{now.Add(-3 * 24 * time.Hour).Format(time.RFC3339), "3 days ago"},
		{now.Add(-60 * 24 * time.Hour).Format(time.RFC3339), "2 months ago"},
	}
	for _, tt := range tests {
		if got := humanizeProviderTime(tt.value, now); got != tt.want {
			t.Errorf("humanizeProviderTime(%q) = %q, want %q", tt.value, got, tt.want)
		}
	}
}
