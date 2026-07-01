package provider_test

import (
	"context"
	"testing"

	"github.com/nyroway/nyro/go/internal/provider"
)

func TestCustomDefinition(t *testing.T) {
	d, ok := provider.Lookup("custom")
	if !ok {
		t.Fatal("custom not found")
	}
	if d.DefaultModel != "" {
		t.Errorf("DefaultModel = %q, want empty", d.DefaultModel)
	}
	if d.DefaultProtocol != "openai-compatible" {
		t.Errorf("DefaultProtocol = %q, want openai-compatible", d.DefaultProtocol)
	}
	if !provider.SupportsProtocol(d, "openai-compatible") {
		t.Error("should support openai-compatible")
	}
	if !hasCredentialField(d, "api_key") {
		t.Error("should expose api_key credential")
	}
}

func TestCustomNoopWhenNoCredential(t *testing.T) {
	p, ok := provider.Get("custom")
	if !ok {
		t.Fatal("custom not found")
	}
	// No credentials → NoopAuthenticator (no error): a custom upstream may run
	// unauthenticated (e.g. a local server with no API key).
	if _, err := p.NewAuthenticator(context.Background(), provider.UpstreamRuntime{}); err != nil {
		t.Errorf("empty credentials should yield Noop, got error: %v", err)
	}
}
