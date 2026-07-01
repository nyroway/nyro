package provider_test

import (
	"testing"

	"github.com/nyroway/nyro/go/internal/provider"
)

func TestAzureFoundryDefinition(t *testing.T) {
	d, ok := provider.Lookup("azure-foundry")
	if !ok {
		t.Fatal("azure-foundry not found")
	}
	if d.DefaultModel != "" {
		t.Errorf("DefaultModel = %q, want empty deployment-specific default", d.DefaultModel)
	}
	if d.DefaultProtocol != "azure-openai" {
		t.Errorf("DefaultProtocol = %q, want azure-openai", d.DefaultProtocol)
	}
	if !provider.SupportsProtocol(d, "azure-openai") {
		t.Error("should support azure-openai")
	}
	if !hasCredentialField(d, "resource_name") || !hasCredentialField(d, "api_version") {
		t.Error("should expose resource_name and api_version credentials")
	}
}
