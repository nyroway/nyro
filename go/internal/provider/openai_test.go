package provider_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/nyroway/nyro/go/internal/provider"
)

func TestOpenAIDefinition(t *testing.T) {
	d, ok := provider.Lookup("openai")
	if !ok {
		t.Fatal("openai not found")
	}
	if d.DefaultModel != "gpt-4o-mini" {
		t.Errorf("DefaultModel = %q, want gpt-4o-mini", d.DefaultModel)
	}
	if d.DefaultProtocol != "openai-compatible" {
		t.Errorf("DefaultProtocol = %q, want openai-compatible", d.DefaultProtocol)
	}
	if !provider.SupportsProtocol(d, "openai-compatible") {
		t.Error("should support openai-compatible")
	}
	if !provider.SupportsProtocol(d, "openai-responses") {
		t.Error("should support openai-responses")
	}
	if !hasCredentialField(d, "api_key") {
		t.Error("should expose api_key credential")
	}
}

func TestOpenAIAuthenticator(t *testing.T) {
	p, ok := provider.Get("openai")
	if !ok {
		t.Fatal("openai not found")
	}
	auth, err := p.NewAuthenticator(context.Background(), provider.UpstreamRuntime{
		CredentialsJSON: json.RawMessage(`{"api_key":"sk-test"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPost, "https://api.openai.com/v1/chat/completions", nil)
	if err := auth.Apply(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer sk-test" {
		t.Errorf("Authorization = %q, want Bearer sk-test", got)
	}
}
