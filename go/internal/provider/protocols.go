package provider

// Wire-format protocol IDs. A protocol ID identifies the request/response
// message schema (the codec); it is independent of transport (authentication,
// URL structure, query parameters), which is owned by the provider's
// Authenticator and URL construction.
//
// Cloud protocol routing — which protocol to use for a given model on each cloud:
//
//	AWS Bedrock (SigV4 auth throughout):
//	  - Claude            → anthropic-messages  (InvokeModel; adds anthropic_version="bedrock-*", model in URL)
//	  - any model (unify) → bedrock-converse    (Converse API; cross-model unified schema)
//
//	Azure (api-key header or Azure AD):
//	  - OpenAI GPT/o (Azure OpenAI Service) → azure-openai        (deployment in path, api-version query)
//	  - Claude (AI Foundry serverless)      → anthropic-messages  (Foundry anthropic endpoint)
//	  - Foundry non-Claude (Llama/Mistral)  → openai-compatible   (AI Model Inference API)
//
//	GCP Vertex AI (OAuth / service-account):
//	  - Gemini            → google-gemini       (generateContent)
//	  - Claude            → anthropic-messages  (rawPredict; model in path)
//	  - some 3rd-party    → openai-compatible   (/endpoints/openapi; partial coverage)
//	  - other 3rd-party   → publisher-native via rawPredict (no unified layer)
//
// anthropic-messages is the common denominator: Claude on all three clouds
// accepts the anthropic Messages body — only the transport differs.
const (
	ProtocolOpenAICompatible  = "openai-compatible"
	ProtocolOpenAIResponses   = "openai-responses"
	ProtocolAnthropicMessages = "anthropic-messages"
	ProtocolGoogleGemini      = "google-gemini"
	ProtocolBedrockConverse   = "bedrock-converse"
	ProtocolAzureOpenAI       = "azure-openai"
)
