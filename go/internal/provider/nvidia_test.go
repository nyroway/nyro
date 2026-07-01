package provider_test

import (
	"testing"

	"github.com/nyroway/nyro/go/internal/provider"
)

func TestNVIDIADefinition(t *testing.T) {
	d, ok := provider.Lookup("nvidia")
	if !ok {
		t.Fatal("nvidia not found")
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
