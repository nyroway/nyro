package provider_test

import (
	"testing"

	"github.com/nyroway/nyro/go/internal/provider"
)

func TestMoonshotAIDefinition(t *testing.T) {
	d, ok := provider.Lookup("moonshotai")
	if !ok {
		t.Fatal("moonshotai not found")
	}
	if d.DefaultProtocol != "openai-compatible" {
		t.Errorf("DefaultProtocol = %q, want openai-compatible", d.DefaultProtocol)
	}
	if !provider.SupportsProtocol(d, "openai-compatible") || !provider.SupportsProtocol(d, "anthropic-messages") {
		t.Error("should support openai-compatible and anthropic-messages")
	}
	if !hasCredentialField(d, "api_key") {
		t.Error("should expose api_key credential")
	}
}
