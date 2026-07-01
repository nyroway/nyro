package provider

// AzureFoundryProvider implements Azure Foundry provider behavior.
type AzureFoundryProvider struct {
	DefaultProvider
}

func init() {
	Register(AzureFoundryProvider{DefaultProvider: DefaultProvider{
		Def: Definition{
			ID:              "azure-foundry",
			Name:            "Azure Foundry",
			DefaultProtocol: ProtocolAzureOpenAI,
			Protocols: []Protocol{
				// OpenAI models via Azure OpenAI Service (deployment in path, api-version query).
				{ID: ProtocolAzureOpenAI, BaseURL: "https://{resource_name}.openai.azure.com"},
				// Claude via AI Foundry anthropic endpoint. Exact URL refined at implementation time.
				{ID: ProtocolAnthropicMessages, BaseURL: "https://{resource_name}.openai.azure.com"},
				// Foundry non-Claude serverless via AI Model Inference API. Exact URL refined at implementation time.
				{ID: ProtocolOpenAICompatible, BaseURL: "https://{resource_name}.openai.azure.com"},
			},
			Models: ModelDiscovery{Kind: KindStatic},
			Credentials: CredentialSchema{Fields: []CredentialField{
				{Name: "resource_name", Type: "string", Required: true},
				{Name: "api_version", Type: "string", Required: true, Default: "2024-10-21"},
				{Name: "credential_source", Type: "enum", Required: true, Default: "default_chain", Values: []string{"default_chain", "client_secret", "managed_identity"}},
				{Name: "tenant_id", Type: "string", Env: "AZURE_TENANT_ID", RequiredWhen: map[string]any{"credential_source": "client_secret"}},
				{Name: "client_id", Type: "string", Env: "AZURE_CLIENT_ID", RequiredWhen: map[string]any{"credential_source": []string{"client_secret", "managed_identity"}}},
				{Name: "client_secret", Type: "secret", Env: "AZURE_CLIENT_SECRET", RequiredWhen: map[string]any{"credential_source": "client_secret"}},
			}},
		},
	}})
}
