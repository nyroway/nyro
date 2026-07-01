package provider_test

import (
	"testing"

	"github.com/nyroway/nyro/go/internal/provider"
)

func TestDeepSeekDefinition(t *testing.T) {
	d, ok := provider.Lookup("deepseek")
	if !ok {
		t.Fatal("deepseek not found")
	}
	if d.DefaultModel != "deepseek-chat" {
		t.Errorf("DefaultModel = %q, want deepseek-chat", d.DefaultModel)
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
