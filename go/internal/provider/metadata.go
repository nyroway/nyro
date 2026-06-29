// Package provider holds vendor metadata (presets) for the WebUI provider
// dropdown + per-vendor auth/URL configuration. Ported from
// crates/nyro-core/src/provider/{metadata.rs,*/mod.rs}.
package provider

// OAuthConfig holds the OAuth flow endpoints for a channel.
type OAuthConfig struct {
	AuthBaseURL  string `json:"authBaseUrl,omitempty"`
	AuthorizeURL string `json:"authorizeUrl,omitempty"`
	TokenURL     string `json:"tokenUrl,omitempty"`
	ClientID     string `json:"clientId,omitempty"`
	RedirectURI  string `json:"redirectUri,omitempty"`
	Scope        string `json:"scope,omitempty"`
}

// ChannelDef is one channel under a vendor (e.g. "default", "codex").
type ChannelDef struct {
	ID           string            `json:"id"`
	Label        string            `json:"label"`
	BaseURLs     map[string]string `json:"baseUrls"` // protocol → base_url
	AuthMode     string            `json:"authMode"` // "apikey" | "oauth"
	ModelsSource string            `json:"modelsSource,omitempty"`
	StaticModels []string          `json:"staticModels,omitempty"`
	OAuth        *OAuthConfig      `json:"oauth,omitempty"`
}

// VendorMetadata is one vendor entry in the preset list.
type VendorMetadata struct {
	ID              string       `json:"id"`
	Label           string       `json:"label"`
	Icon            string       `json:"icon"`
	DefaultProtocol string       `json:"defaultProtocol"`
	Channels        []ChannelDef `json:"channels"`
}

// Presets is the ordered list of vendor presets for the WebUI.
var Presets = []VendorMetadata{
	{ID: "openai", Label: "OpenAI", Icon: "openai", DefaultProtocol: "openai-compatible", Channels: []ChannelDef{
		{ID: "default", Label: "Default", BaseURLs: map[string]string{"openai-compatible": "https://api.openai.com/v1"}, AuthMode: "apikey", ModelsSource: "https://api.openai.com/v1/models"},
		{ID: "codex", Label: "Codex", BaseURLs: map[string]string{"openai-responses": "https://chatgpt.com/backend-api/codex"}, AuthMode: "oauth", ModelsSource: "https://chatgpt.com/backend-api/codex/models", OAuth: &OAuthConfig{AuthBaseURL: "https://auth.openai.com", AuthorizeURL: "https://auth.openai.com/oauth/authorize", TokenURL: "https://auth.openai.com/oauth/token", ClientID: "app_EMoamEEZ73f0CkXaXp7hrann", RedirectURI: "http://localhost:1455/auth/callback", Scope: "openid profile email offline_access"}},
	}},
	{ID: "anthropic", Label: "Anthropic", Icon: "anthropic", DefaultProtocol: "anthropic-messages", Channels: []ChannelDef{
		{ID: "default", Label: "Default", BaseURLs: map[string]string{"anthropic-messages": "https://api.anthropic.com"}, AuthMode: "apikey", ModelsSource: "https://api.anthropic.com/v1/models"},
		{ID: "claude-code", Label: "Claude Code", BaseURLs: map[string]string{"anthropic-messages": "https://api.anthropic.com"}, AuthMode: "oauth", ModelsSource: "ai://models.dev/anthropic", StaticModels: []string{"claude-opus-4-6", "claude-sonnet-4-6", "claude-opus-4-5-20251101", "claude-sonnet-4-5-20250929", "claude-sonnet-4-20250514", "claude-opus-4-1-20250805", "claude-opus-4-20250514", "claude-haiku-4-5-20251001", "claude-3-5-haiku-20241022"}, OAuth: &OAuthConfig{AuthBaseURL: "https://claude.ai", AuthorizeURL: "https://claude.ai/oauth/authorize", TokenURL: "https://console.anthropic.com/v1/oauth/token", ClientID: "9d1c250a-e61b-44d9-88ed-5944d1962f5e", RedirectURI: "https://platform.claude.com/oauth/code/callback", Scope: "user:inference user:profile"}},
	}},
	{ID: "google", Label: "Google", Icon: "google", DefaultProtocol: "google-gemini", Channels: []ChannelDef{
		{ID: "default", Label: "Default", BaseURLs: map[string]string{"google-gemini": "https://generativelanguage.googleapis.com", "openai-compatible": "https://generativelanguage.googleapis.com/v1beta/openai"}, AuthMode: "apikey", ModelsSource: "https://generativelanguage.googleapis.com/v1beta/models"},
	}},
	{ID: "deepseek", Label: "DeepSeek", Icon: "deepseek", DefaultProtocol: "openai-compatible", Channels: []ChannelDef{
		{ID: "default", Label: "Default", BaseURLs: map[string]string{"openai-compatible": "https://api.deepseek.com/v1", "anthropic-messages": "https://api.deepseek.com/anthropic"}, AuthMode: "apikey", ModelsSource: "https://api.deepseek.com/v1/models"},
	}},
	{ID: "moonshotai", Label: "Moonshot AI", Icon: "moonshotai", DefaultProtocol: "openai-compatible", Channels: []ChannelDef{
		{ID: "default", Label: "Default", BaseURLs: map[string]string{"openai-compatible": "https://api.moonshot.ai/v1", "anthropic-messages": "https://api.moonshot.ai/anthropic"}, AuthMode: "apikey"},
		{ID: "cn", Label: "China", BaseURLs: map[string]string{"openai-compatible": "https://api.moonshot.cn/v1"}, AuthMode: "apikey"},
	}},
	{ID: "zhipuai", Label: "Zhipu AI", Icon: "zhipuai", DefaultProtocol: "openai-compatible", Channels: []ChannelDef{
		{ID: "default", Label: "Default", BaseURLs: map[string]string{"openai-compatible": "https://open.bigmodel.cn/api/paas/v4", "anthropic-messages": "https://open.bigmodel.cn/api/anthropic"}, AuthMode: "apikey"},
		{ID: "coding", Label: "Coding", BaseURLs: map[string]string{"openai-compatible": "https://open.bigmodel.cn/api/coding/paas/v4"}, AuthMode: "apikey"},
	}},
	{ID: "xai", Label: "xAI", Icon: "xai", DefaultProtocol: "openai-compatible", Channels: []ChannelDef{
		{ID: "default", Label: "Default", BaseURLs: map[string]string{"openai-compatible": "https://api.x.ai/v1"}, AuthMode: "apikey", ModelsSource: "https://api.x.ai/v1/models"},
	}},
	{ID: "openrouter", Label: "OpenRouter", Icon: "openrouter", DefaultProtocol: "openai-compatible", Channels: []ChannelDef{
		{ID: "default", Label: "Default", BaseURLs: map[string]string{"openai-compatible": "https://openrouter.ai/api/v1"}, AuthMode: "apikey", ModelsSource: "https://openrouter.ai/api/v1/models"},
	}},
	{ID: "nvidia", Label: "NVIDIA", Icon: "nvidia", DefaultProtocol: "openai-compatible", Channels: []ChannelDef{
		{ID: "default", Label: "Default", BaseURLs: map[string]string{"openai-compatible": "https://integrate.api.nvidia.com/v1"}, AuthMode: "apikey"},
	}},
	{ID: "minimax", Label: "MiniMax", Icon: "minimax", DefaultProtocol: "openai-compatible", Channels: []ChannelDef{
		{ID: "default", Label: "Default", BaseURLs: map[string]string{"openai-compatible": "https://api.minimax.io/v1", "anthropic-messages": "https://api.minimax.io/anthropic"}, AuthMode: "apikey"},
		{ID: "cn", Label: "China", BaseURLs: map[string]string{"openai-compatible": "https://api.minimaxi.com/v1"}, AuthMode: "apikey"},
	}},
	{ID: "vertexai", Label: "Vertex AI", Icon: "vertexai", DefaultProtocol: "google-gemini", Channels: []ChannelDef{
		{ID: "default", Label: "Default", BaseURLs: map[string]string{"google-gemini": "https://aiplatform.googleapis.com/v1/projects/{project}/locations/global", "openai-compatible": "https://aiplatform.googleapis.com/v1/projects/{project}/locations/global/endpoints/openapi"}, AuthMode: "apikey"},
	}},
	{ID: "zai", Label: "Z.ai", Icon: "zai", DefaultProtocol: "openai-compatible", Channels: []ChannelDef{
		{ID: "default", Label: "Default", BaseURLs: map[string]string{"openai-compatible": "https://api.z.ai/api/paas/v4", "anthropic-messages": "https://api.z.ai/api/anthropic"}, AuthMode: "apikey"},
		{ID: "coding", Label: "Coding", BaseURLs: map[string]string{"openai-compatible": "https://api.z.ai/api/coding/paas/v4"}, AuthMode: "apikey"},
	}},
	{ID: "ollama", Label: "Ollama", Icon: "ollama", DefaultProtocol: "openai-compatible", Channels: []ChannelDef{
		{ID: "default", Label: "Default", BaseURLs: map[string]string{"openai-compatible": "http://127.0.0.1:11434/v1"}, AuthMode: "apikey"},
	}},
	{ID: "custom", Label: "Custom", Icon: "custom", DefaultProtocol: "openai-compatible", Channels: []ChannelDef{
		{ID: "default", Label: "Default", BaseURLs: map[string]string{"openai-compatible": ""}, AuthMode: "apikey"},
	}},
}

// FindPreset returns the vendor metadata for a vendor id.
func FindPreset(vendorID string) (VendorMetadata, bool) {
	for _, v := range Presets {
		if v.ID == vendorID {
			return v, true
		}
	}
	return VendorMetadata{}, false
}
