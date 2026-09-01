package bootstrap

import (
	"github.com/nyroway/nyro/go/internal/llm/protocol"
	"github.com/nyroway/nyro/go/internal/llm/protocol/anthropic/messages"
	"github.com/nyroway/nyro/go/internal/llm/protocol/gemini/generatecontent"
	"github.com/nyroway/nyro/go/internal/llm/protocol/openai/chatcompletions"
	"github.com/nyroway/nyro/go/internal/llm/protocol/openai/embeddings"
	"github.com/nyroway/nyro/go/internal/llm/protocol/openai/responses"
)

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
