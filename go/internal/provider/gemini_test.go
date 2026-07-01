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
	if !provider.SupportsProtocol(d, "google-gemini") || !provider.SupportsProtocol(d, "openai-compatible") {
		t.Error("should support google-gemini and openai-compatible")
	}
	if !hasCredentialField(d, "api_key") {
		t.Error("should expose api_key credential")
	}
}

func TestGeminiAuthenticatorSwitchesByProtocol(t *testing.T) {
	p, ok := provider.Get("gemini")
	if !ok {
		t.Fatal("gemini not found")
	}
	creds := json.RawMessage(`{"api_key":"AIza-test"}`)

	// google-gemini → x-goog-api-key
	auth, err := p.NewAuthenticator(context.Background(), provider.UpstreamRuntime{
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

	// openai-compatible → Bearer
	auth2, err := p.NewAuthenticator(context.Background(), provider.UpstreamRuntime{
		Protocol:        "openai-compatible",
		CredentialsJSON: creds,
	})
	if err != nil {
		t.Fatal(err)
	}
	req2, _ := http.NewRequest(http.MethodPost, "https://generativelanguage.googleapis.com/v1beta/openai/chat/completions", nil)
	if err := auth2.Apply(context.Background(), req2); err != nil {
		t.Fatal(err)
	}
	if got := req2.Header.Get("Authorization"); got != "Bearer AIza-test" {
		t.Fatalf("openai-compatible: Authorization = %q, want Bearer AIza-test", got)
	}
}
