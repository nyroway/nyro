package provider_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/nyroway/nyro/go/internal/provider"
)

func TestAuthenticatorForOpenAICompatibleAndResponsesUseBearer(t *testing.T) {
	for _, protocol := range []string{"openai-chat", "openai-responses"} {
		auth, err := provider.AuthenticatorFor("openai", protocol, provider.UpstreamRuntime{
			CredentialsJSON: json.RawMessage(`{"api_key":"sk-test"}`),
		})
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", protocol, err)
		}
		req, _ := http.NewRequest(http.MethodPost, "https://example.com/v1/chat/completions", nil)
		if err := auth.Apply(context.Background(), req); err != nil {
			t.Fatalf("%s: Apply error: %v", protocol, err)
		}
		if got := req.Header.Get("Authorization"); got != "Bearer sk-test" {
			t.Errorf("%s: Authorization = %q, want Bearer sk-test", protocol, got)
		}
	}
}

func TestAuthenticatorForOpenAICompatibleMissingAPIKeyErrors(t *testing.T) {
	_, err := provider.AuthenticatorFor("openai", "openai-chat", provider.UpstreamRuntime{
		CredentialsJSON: json.RawMessage(`{}`),
	})
	if err == nil {
		t.Fatal("expected error for missing api_key")
	}
}

func TestAuthenticatorForAnthropicMessagesSetsHeaders(t *testing.T) {
	auth, err := provider.AuthenticatorFor("anthropic", "anthropic-messages", provider.UpstreamRuntime{
		CredentialsJSON: json.RawMessage(`{"api_key":"anthropic-key"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", nil)
	if err := auth.Apply(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("x-api-key"); got != "anthropic-key" {
		t.Errorf("x-api-key = %q, want anthropic-key", got)
	}
	if got := req.Header.Get("anthropic-version"); got != provider.DefaultAnthropicVersion {
		t.Errorf("anthropic-version = %q, want %q", got, provider.DefaultAnthropicVersion)
	}
}

func TestAuthenticatorForAnthropicMessagesMissingAPIKeyErrors(t *testing.T) {
	_, err := provider.AuthenticatorFor("anthropic", "anthropic-messages", provider.UpstreamRuntime{
		CredentialsJSON: json.RawMessage(`{}`),
	})
	if err == nil {
		t.Fatal("expected error for missing api_key")
	}
}

func TestAuthenticatorForGeminiContentSetsFixedHeader(t *testing.T) {
	auth, err := provider.AuthenticatorFor("gemini", "google-gemini", provider.UpstreamRuntime{
		CredentialsJSON: json.RawMessage(`{"api_key":"gemini-key"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPost, "https://generativelanguage.googleapis.com/v1beta/models", nil)
	if err := auth.Apply(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("x-goog-api-key"); got != "gemini-key" {
		t.Errorf("x-goog-api-key = %q, want gemini-key", got)
	}
	if got := req.Header.Get("Authorization"); got != "" {
		t.Errorf("Authorization should be unset for google-gemini, got %q", got)
	}
}

func TestAuthenticatorForGeminiContentMissingAPIKeyErrors(t *testing.T) {
	_, err := provider.AuthenticatorFor("gemini", "google-gemini", provider.UpstreamRuntime{
		CredentialsJSON: json.RawMessage(`{}`),
	})
	if err == nil {
		t.Fatal("expected error for missing api_key")
	}
}

func TestAuthenticatorForUnknownProtocolFallsBackToBearerWithCredentials(t *testing.T) {
	auth, err := provider.AuthenticatorFor("", "some-unknown-protocol", provider.UpstreamRuntime{
		CredentialsJSON: json.RawMessage(`{"api_key":"fallback-key"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPost, "https://example.com/", nil)
	if err := auth.Apply(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer fallback-key" {
		t.Errorf("Authorization = %q, want Bearer fallback-key", got)
	}
}

func TestAuthenticatorForUnknownProtocolNoopWhenNoCredentials(t *testing.T) {
	auth, err := provider.AuthenticatorFor("some-unknown-provider", "some-unknown-protocol", provider.UpstreamRuntime{})
	if err != nil {
		t.Fatalf("empty credentials should yield Noop, got error: %v", err)
	}
	req, _ := http.NewRequest(http.MethodPost, "https://example.com/", nil)
	if err := auth.Apply(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("Authorization"); got != "" {
		t.Errorf("Authorization should be unset for noop authenticator, got %q", got)
	}
}

func TestAuthenticatorForEmptyProtocolFallsBackLikeUnknown(t *testing.T) {
	auth, err := provider.AuthenticatorFor("", "", provider.UpstreamRuntime{})
	if err != nil {
		t.Fatalf("empty protocol + no credentials should yield Noop, got error: %v", err)
	}
	if _, ok := auth.(provider.NoopAuthenticator); !ok {
		t.Errorf("expected NoopAuthenticator, got %T", auth)
	}
}

// TestAuthenticatorForUnknownProviderKnownProtocolUsesProtocolFallback covers
// "custom" upstreams and legacy rows whose provider id doesn't match any
// registered preset: with an unregistered/empty providerID but a known
// protocol, AuthenticatorFor must still resolve the matching scheme via the
// protocol fallback in authSchemeFor, not fall through to the bare-bearer/
// noop default.
func TestAuthenticatorForUnknownProviderKnownProtocolUsesProtocolFallback(t *testing.T) {
	auth, err := provider.AuthenticatorFor("some-unknown-provider", "anthropic-messages", provider.UpstreamRuntime{
		CredentialsJSON: json.RawMessage(`{"api_key":"fallback-anthropic-key"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", nil)
	if err := auth.Apply(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("x-api-key"); got != "fallback-anthropic-key" {
		t.Errorf("x-api-key = %q, want fallback-anthropic-key", got)
	}
	if got := req.Header.Get("anthropic-version"); got != provider.DefaultAnthropicVersion {
		t.Errorf("anthropic-version = %q, want %q", got, provider.DefaultAnthropicVersion)
	}
}

func TestAuthenticatorForUnknownProviderUsesProtocolFallback(t *testing.T) {
	auth, err := provider.AuthenticatorFor("some-unknown-provider", "google-gemini", provider.UpstreamRuntime{
		CredentialsJSON: json.RawMessage(`{"api_key":"legacy-gemini-key"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPost, "https://generativelanguage.googleapis.com/v1beta/models", nil)
	if err := auth.Apply(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("x-goog-api-key"); got != "legacy-gemini-key" {
		t.Errorf("x-goog-api-key = %q, want legacy-gemini-key", got)
	}
	if got := req.Header.Get("Authorization"); got != "" {
		t.Errorf("Authorization should be unset for Gemini protocol fallback, got %q", got)
	}
}
