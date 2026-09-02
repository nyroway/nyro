package bootstrap

import "github.com/nyroway/nyro/go/internal/llm/provider"

func NewLLMProviderCatalog() (*provider.Catalog, error) {
	return provider.NewCatalog(
		provider.Generic(),
		provider.OpenAI(),
		provider.Anthropic(),
		provider.Gemini(),
		provider.DeepSeek(),
		provider.OpenRouter(),
	)
}
