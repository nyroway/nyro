package provider

// AWSBedrockProvider implements AWS Bedrock provider behavior.
type AWSBedrockProvider struct {
	DefaultProvider
}

func init() {
	Register(AWSBedrockProvider{DefaultProvider: DefaultProvider{
		Def: Definition{
			ID:              "aws-bedrock",
			Name:            "AWS Bedrock",
			DefaultProtocol: ProtocolBedrockConverse,
			DefaultModel:    "anthropic.claude-3-haiku-20240307-v1:0",
			Protocols: []Protocol{
				// Claude on Bedrock: InvokeModel with an anthropic Messages body
				// (adds anthropic_version="bedrock-*", model in URL). Transport SigV4.
				{ID: ProtocolAnthropicMessages, BaseURL: "https://bedrock-runtime.{region}.amazonaws.com"},
				// Non-Claude / unified: Converse API (cross-model schema). Transport SigV4.
				{ID: ProtocolBedrockConverse, BaseURL: "https://bedrock-runtime.{region}.amazonaws.com"},
			},
			Models: ModelDiscovery{Kind: KindStatic},
			Credentials: CredentialSchema{Fields: []CredentialField{
				{Name: "region", Type: "string", Required: true},
				{Name: "credential_source", Type: "enum", Required: true, Default: "default_chain", Values: []string{"default_chain", "static"}},
				{Name: "access_key_id", Type: "secret", Env: "AWS_ACCESS_KEY_ID", RequiredWhen: map[string]any{"credential_source": "static"}},
				{Name: "secret_access_key", Type: "secret", Env: "AWS_SECRET_ACCESS_KEY", RequiredWhen: map[string]any{"credential_source": "static"}},
				{Name: "session_token", Type: "secret", Env: "AWS_SESSION_TOKEN"},
			}},
		},
	}})
}
