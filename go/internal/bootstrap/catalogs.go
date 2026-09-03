package bootstrap

import (
	"github.com/nyroway/nyro/go/internal/llm/protocol"
	"github.com/nyroway/nyro/go/internal/llm/protocol/anthropic/messages"
	"github.com/nyroway/nyro/go/internal/llm/protocol/gemini/generatecontent"
	"github.com/nyroway/nyro/go/internal/llm/protocol/openai/chatcompletions"
	"github.com/nyroway/nyro/go/internal/llm/protocol/openai/embeddings"
	"github.com/nyroway/nyro/go/internal/llm/protocol/openai/responses"
	"github.com/nyroway/nyro/go/internal/llm/provider"
)

// NewLLMProtocolCatalog explicitly enumerates every LLM wire protocol compiled
// into Nyro. Importing a codec never registers it implicitly.
func NewLLMProtocolCatalog() (*protocol.Catalog, error) {
	return protocol.NewCatalog(
		[]protocol.IngressCodec{
			chatcompletions.NewIngress(),
			responses.NewIngress(),
			embeddings.NewIngress(),
			messages.NewIngress(),
			generatecontent.NewIngress(),
		},
		[]protocol.EgressCodec{
			chatcompletions.NewEgress(),
			responses.NewEgress(),
			embeddings.NewEgress(),
			messages.NewEgress(),
			generatecontent.NewEgress(),
		},
	)
}

// NewLLMProviderCatalog explicitly enumerates every LLM Provider compiled into
// Nyro. Unknown configured IDs use the generic Provider definition.
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
