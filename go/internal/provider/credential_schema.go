package provider

// CredentialSchemaFor returns the credential fields AuthenticatorFor requires
// for the given protocol. All four supported protocols currently need a
// single api_key field; this returns per-protocol data now so the schema
// can diverge later (e.g. when Vertex/Bedrock auth variants are added)
// without changing callers.
func CredentialSchemaFor(protocol string) CredentialSchema {
	switch protocol {
	case ProtocolOpenAICompatible, ProtocolOpenAIResponses, ProtocolAnthropicMessages, ProtocolGeminiContent:
		return CredentialSchema{Fields: []CredentialField{{Name: "api_key", Type: "string", Required: true}}}
	default:
		return CredentialSchema{Fields: []CredentialField{{Name: "api_key", Type: "string", Required: false}}}
	}
}
