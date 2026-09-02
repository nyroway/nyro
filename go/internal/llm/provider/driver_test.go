package provider

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nyroway/nyro/go/internal/llm/protocol"
)

func TestBuiltinDriversPrepareProviderSpecificAuthentication(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		registration Registration
		protocol     string
		wantHeader   string
		wantValue    string
	}{
		{name: "openai", registration: OpenAI(), protocol: ProtocolOpenAIChatCompletions, wantHeader: "Authorization", wantValue: "Bearer test-key"},
		{name: "anthropic", registration: Anthropic(), protocol: ProtocolAnthropicMessages, wantHeader: "x-api-key", wantValue: "test-key"},
		{name: "gemini", registration: Gemini(), protocol: ProtocolOpenAIChatCompletions, wantHeader: "x-goog-api-key", wantValue: "test-key"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			request, err := test.registration.Factory().Prepare(context.Background(), UpstreamRuntime{
				Protocol: test.protocol, BaseURL: "https://example.com/v1",
				CredentialsJSON: json.RawMessage(`{"api_key":"test-key"}`),
			}, protocol.WireRequest{Method: "POST", Path: "/v1/chat/completions", Body: []byte(`{}`)})
			if err != nil {
				t.Fatalf("Prepare(): %v", err)
			}
			if got := request.URL; got != "https://example.com/v1/chat/completions" {
				t.Fatalf("URL = %q", got)
			}
			if got := request.Headers[test.wantHeader]; got != test.wantValue {
				t.Fatalf("%s = %q, want %q", test.wantHeader, got, test.wantValue)
			}
			if got := request.Headers["Content-Type"]; got != "application/json" {
				t.Fatalf("Content-Type = %q", got)
			}
		})
	}
}

func TestGenericDriverUsesProtocolAuthenticationFallback(t *testing.T) {
	t.Parallel()
	driver := Generic().Factory()
	request, err := driver.Prepare(context.Background(), UpstreamRuntime{
		Protocol: ProtocolAnthropicMessages, BaseURL: "https://example.com",
		CredentialsJSON: json.RawMessage(`{"api_key":"fallback-key"}`),
	}, protocol.WireRequest{Method: "POST", Path: "/v1/messages"})
	if err != nil {
		t.Fatalf("Prepare(): %v", err)
	}
	if got := request.Headers["x-api-key"]; got != "fallback-key" {
		t.Fatalf("x-api-key = %q", got)
	}
}

func TestGenericDriverNormalizesProtocolAliasForAuthentication(t *testing.T) {
	t.Parallel()
	request, err := Generic().Factory().Prepare(context.Background(), UpstreamRuntime{
		Protocol: "claude", BaseURL: "https://example.com",
		CredentialsJSON: json.RawMessage(`{"api_key":"alias-key"}`),
	}, protocol.WireRequest{Method: "POST", Path: "/v1/messages"})
	if err != nil {
		t.Fatalf("Prepare(): %v", err)
	}
	if got := request.Headers["x-api-key"]; got != "alias-key" {
		t.Fatalf("x-api-key = %q", got)
	}
}

func TestGenericDriverAllowsUnknownProtocolWithoutCredentials(t *testing.T) {
	t.Parallel()
	request, err := Generic().Factory().Prepare(context.Background(), UpstreamRuntime{
		Protocol: "custom", BaseURL: "https://example.com",
	}, protocol.WireRequest{Method: "POST", Path: "/generate"})
	if err != nil {
		t.Fatalf("Prepare(): %v", err)
	}
	if _, exists := request.Headers["Authorization"]; exists {
		t.Fatal("Prepare() added authentication without credentials")
	}
}

func TestBuiltinDriverRejectsMissingAPIKey(t *testing.T) {
	t.Parallel()
	_, err := OpenAI().Factory().Prepare(context.Background(), UpstreamRuntime{
		BaseURL: "https://example.com", CredentialsJSON: json.RawMessage(`{}`),
	}, protocol.WireRequest{Method: "POST", Path: "/v1/chat/completions"})
	if err == nil {
		t.Fatal("Prepare() accepted missing api_key")
	}
}
