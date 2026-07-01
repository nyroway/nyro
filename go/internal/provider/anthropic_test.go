package provider_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/nyroway/nyro/go/internal/provider"
)

func TestAnthropicDefinition(t *testing.T) {
	d, ok := provider.Lookup("anthropic")
	if !ok {
		t.Fatal("anthropic not found")
	}
	if d.DefaultModel != "claude-sonnet-4-6" {
		t.Errorf("DefaultModel = %q, want claude-sonnet-4-6", d.DefaultModel)
	}
	if d.DefaultProtocol != "anthropic-messages" {
		t.Errorf("DefaultProtocol = %q, want anthropic-messages", d.DefaultProtocol)
	}
	if !provider.SupportsProtocol(d, "anthropic-messages") {
		t.Error("should support anthropic-messages")
	}
	if !hasCredentialField(d, "api_key") {
		t.Error("should expose api_key credential")
	}
	if v, _ := d.Extra["anthropic_version"].(string); v == "" {
		t.Error("should carry anthropic_version in Extra")
	}
}

func TestAnthropicAuthenticator(t *testing.T) {
	p, ok := provider.Get("anthropic")
	if !ok {
		t.Fatal("anthropic not found")
	}
	auth, err := p.NewAuthenticator(context.Background(), provider.UpstreamRuntime{
		CredentialsJSON: json.RawMessage(`{"api_key":"sk-ant"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", nil)
	if err := auth.Apply(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("x-api-key"); got != "sk-ant" {
		t.Errorf("x-api-key = %q, want sk-ant", got)
	}
	if got := req.Header.Get("anthropic-version"); got != "2023-06-01" {
		t.Errorf("anthropic-version = %q, want 2023-06-01", got)
	}
	if got := req.Header.Get("Authorization"); got != "" {
		t.Errorf("Authorization = %q, want empty (uses x-api-key, not Bearer)", got)
	}
}
