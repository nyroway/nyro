package provider_test

import (
	"testing"

	"github.com/nyroway/nyro/go/internal/provider"
)

func TestZhipuAIDefinition(t *testing.T) {
	d, ok := provider.Lookup("zhipuai")
	if !ok {
		t.Fatal("zhipuai not found")
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
