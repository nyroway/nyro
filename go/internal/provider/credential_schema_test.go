package provider

import "testing"

func TestCredentialSchemaFor(t *testing.T) {
	knownProtocols := []string{
		ProtocolOpenAICompatible,
		ProtocolOpenAIResponses,
		ProtocolAnthropicMessages,
		ProtocolGeminiContent,
	}
	for _, p := range knownProtocols {
		t.Run(p, func(t *testing.T) {
			schema := CredentialSchemaFor(p)
			if len(schema.Fields) != 1 {
				t.Fatalf("protocol %q: got %d fields, want 1", p, len(schema.Fields))
			}
			f := schema.Fields[0]
			if f.Name != "api_key" || f.Type != "string" || !f.Required {
				t.Errorf("protocol %q: got field %+v, want {Name:api_key Type:string Required:true}", p, f)
			}
		})
	}

	t.Run("unknown", func(t *testing.T) {
		schema := CredentialSchemaFor("some-unknown-protocol")
		if len(schema.Fields) != 1 {
			t.Fatalf("unknown protocol: got %d fields, want 1", len(schema.Fields))
		}
		f := schema.Fields[0]
		if f.Name != "api_key" || f.Type != "string" || f.Required {
			t.Errorf("unknown protocol: got field %+v, want {Name:api_key Type:string Required:false}", f)
		}
	})

	t.Run("empty", func(t *testing.T) {
		schema := CredentialSchemaFor("")
		if len(schema.Fields) != 1 {
			t.Fatalf("empty protocol: got %d fields, want 1", len(schema.Fields))
		}
		f := schema.Fields[0]
		if f.Name != "api_key" || f.Type != "string" || f.Required {
			t.Errorf("empty protocol: got field %+v, want {Name:api_key Type:string Required:false}", f)
		}
	})
}
