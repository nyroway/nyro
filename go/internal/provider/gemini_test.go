package provider_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/nyroway/nyro/go/internal/provider"
)

func TestGeminiDefinition(t *testing.T) {
	d, ok := provider.Lookup("gemini")
	if !ok {
		t.Fatal("gemini not found")
	}
	if d.DefaultModel != "gemini-2.0-flash" {
		t.Errorf("DefaultModel = %q, want gemini-2.0-flash", d.DefaultModel)
	}
	if d.DefaultProtocol != "google-gemini" {
		t.Errorf("DefaultProtocol = %q, want google-gemini", d.DefaultProtocol)
	}
	if !provider.SupportsProtocol(d, "google-gemini") || !provider.SupportsProtocol(d, "openai-chat") {
		t.Error("should support google-gemini and openai-chat")
	}
	if !hasCredentialField(d, "api_key") {
		t.Error("should expose api_key credential")
	}
}

// TestGeminiAuthenticatorFixedRegardlessOfProtocol asserts that a
// gemini-provider upstream's outbound auth is fixed to x-goog-api-key by its
// Definition.Auth == "gemini" scheme, regardless of which protocol the
// upstream is configured with (google-gemini or the
// openai-chat-compatible endpoint) — dispatch is provider-scheme
// first, not protocol-first.
func TestGeminiAuthenticatorFixedRegardlessOfProtocol(t *testing.T) {
	creds := json.RawMessage(`{"api_key":"AIza-test"}`)

	auth, err := provider.AuthenticatorFor("gemini", "google-gemini", provider.UpstreamRuntime{
		Protocol:        "google-gemini",
		CredentialsJSON: creds,
	})
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPost, "https://generativelanguage.googleapis.com/v1beta/models/gemini-2.0-flash:generateContent", nil)
	if err := auth.Apply(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("x-goog-api-key"); got != "AIza-test" {
		t.Fatalf("google-gemini: x-goog-api-key = %q, want AIza-test", got)
	}

	auth2, err := provider.AuthenticatorFor("gemini", "openai-chat", provider.UpstreamRuntime{
		Protocol:        "openai-chat",
		CredentialsJSON: creds,
	})
	if err != nil {
		t.Fatal(err)
	}
	req2, _ := http.NewRequest(http.MethodPost, "https://generativelanguage.googleapis.com/v1beta/openai/chat/completions", nil)
	if err := auth2.Apply(context.Background(), req2); err != nil {
		t.Fatal(err)
	}
	if got := req2.Header.Get("x-goog-api-key"); got != "AIza-test" {
		t.Fatalf("gemini + openai-chat: x-goog-api-key = %q, want AIza-test (fixed scheme, not protocol-driven)", got)
	}
	if got := req2.Header.Get("Authorization"); got != "" {
		t.Fatalf("gemini + openai-chat: Authorization = %q, want empty", got)
	}
}
