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
	if d.DefaultProtocol != "openai-chatcompletions" {
		t.Errorf("DefaultProtocol = %q, want openai-chatcompletions", d.DefaultProtocol)
	}
	if !provider.SupportsProtocol(d, "openai-chatcompletions") || !provider.SupportsProtocol(d, "anthropic-messages") {
		t.Error("should support openai-chatcompletions and anthropic-messages")
	}
	if !hasCredentialField(d, "api_key") {
		t.Error("should expose api_key credential")
	}
}
