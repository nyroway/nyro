package provider

// GCPVertexProvider implements Google Cloud Vertex AI provider behavior.
type GCPVertexProvider struct {
	DefaultProvider
}

func init() {
	Register(GCPVertexProvider{DefaultProvider: DefaultProvider{
		Def: Definition{
			ID:              "gcp-vertex",
			Name:            "GCP Vertex AI",
			DefaultProtocol: ProtocolGoogleGemini,
			DefaultModel:    "gemini-2.0-flash",
			Protocols: []Protocol{
				{ID: ProtocolGoogleGemini, BaseURL: "https://{location}-aiplatform.googleapis.com/v1/projects/{project_id}/locations/{location}"},
				// Claude via rawPredict (anthropic Messages body, model in path).
				{ID: ProtocolAnthropicMessages, BaseURL: "https://{location}-aiplatform.googleapis.com/v1/projects/{project_id}/locations/{location}"},
				{ID: ProtocolOpenAICompatible, BaseURL: "https://{location}-aiplatform.googleapis.com/v1/projects/{project_id}/locations/{location}/endpoints/openapi"},
			},
			Models: ModelDiscovery{Kind: KindStatic},
			Credentials: CredentialSchema{Fields: []CredentialField{
				{Name: "project_id", Type: "string", Required: true},
				{Name: "location", Type: "string", Required: true},
				{Name: "credential_source", Type: "enum", Required: true, Default: "default_chain", Values: []string{"default_chain", "service_account_json"}},
				{Name: "service_account_json", Type: "secret", Env: "GOOGLE_APPLICATION_CREDENTIALS_JSON", RequiredWhen: map[string]any{"credential_source": "service_account_json"}},
			}},
		},
	}})
}
