package provider_test

import (
	"testing"

	"github.com/nyroway/nyro/go/internal/provider"
)

func TestOpenRouterDefinition(t *testing.T) {
	d, ok := provider.Lookup("openrouter")
	if !ok {
		t.Fatal("openrouter not found")
	}
	if d.DefaultProtocol != "openai-chatcompletions" {
		t.Errorf("DefaultProtocol = %q, want openai-chatcompletions", d.DefaultProtocol)
	}
	// OpenRouter serves all three wire formats under /api/v1.
	if !provider.SupportsProtocol(d, "openai-chatcompletions") {
		t.Error("should support openai-chatcompletions")
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
