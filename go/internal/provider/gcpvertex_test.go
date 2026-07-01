package provider_test

import (
	"testing"

	"github.com/nyroway/nyro/go/internal/provider"
)

func TestGCPVertexDefinition(t *testing.T) {
	d, ok := provider.Lookup("gcp-vertex")
	if !ok {
		t.Fatal("gcp-vertex not found")
	}
	if d.DefaultModel != "gemini-2.0-flash" {
		t.Errorf("DefaultModel = %q", d.DefaultModel)
	}
	if d.DefaultProtocol != "google-gemini" {
		t.Errorf("DefaultProtocol = %q, want google-gemini", d.DefaultProtocol)
	}
	if !provider.SupportsProtocol(d, "google-gemini") || !provider.SupportsProtocol(d, "openai-compatible") {
		t.Error("should support google-gemini and openai-compatible")
	}
	if !hasCredentialField(d, "project_id") || !hasCredentialField(d, "location") {
		t.Error("should expose project_id and location credentials")
	}
}
