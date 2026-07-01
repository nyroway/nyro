package provider_test

import (
	"testing"

	"github.com/nyroway/nyro/go/internal/provider"
)

func TestAWSBedrockDefinition(t *testing.T) {
	d, ok := provider.Lookup("aws-bedrock")
	if !ok {
		t.Fatal("aws-bedrock not found")
	}
	if d.DefaultModel != "anthropic.claude-3-haiku-20240307-v1:0" {
		t.Errorf("DefaultModel = %q", d.DefaultModel)
	}
	if d.DefaultProtocol != "bedrock-converse" {
		t.Errorf("DefaultProtocol = %q, want bedrock-converse", d.DefaultProtocol)
	}
	if !provider.SupportsProtocol(d, "bedrock-converse") {
		t.Error("should support bedrock-converse")
	}
	if !hasCredentialField(d, "credential_source") || !hasCredentialField(d, "region") {
		t.Error("should expose credential_source and region credentials")
	}
}
