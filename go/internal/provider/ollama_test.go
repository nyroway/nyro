package provider_test

import (
	"context"
	"testing"

	"github.com/nyroway/nyro/go/internal/provider"
)

func TestOllamaDefinition(t *testing.T) {
	d, ok := provider.Lookup("ollama")
	if !ok {
		t.Fatal("ollama not found")
	}
	if d.DefaultProtocol != "openai-compatible" {
		t.Errorf("DefaultProtocol = %q, want openai-compatible", d.DefaultProtocol)
	}
	// Ollama serves all three wire formats under /v1.
	if !provider.SupportsProtocol(d, "openai-compatible") {
		t.Error("should support openai-compatible")
	}
	if !provider.SupportsProtocol(d, "openai-responses") {
		t.Error("should support openai-responses")
	}
	if !provider.SupportsProtocol(d, "anthropic-messages") {
		t.Error("should support anthropic-messages")
	}
	if !hasCredentialField(d, "api_key") {
		t.Error("should expose api_key credential")
	}
}

func TestOllamaNoopWhenNoCredential(t *testing.T) {
	p, ok := provider.Get("ollama")
	if !ok {
		t.Fatal("ollama not found")
	}
	// Local Ollama typically needs no API key → empty credentials yields Noop.
	if _, err := p.NewAuthenticator(context.Background(), provider.UpstreamRuntime{}); err != nil {
		t.Errorf("empty credentials should yield Noop, got error: %v", err)
	}
}
