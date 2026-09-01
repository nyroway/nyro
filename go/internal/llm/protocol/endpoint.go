package protocol

import (
	"fmt"

	"github.com/nyroway/nyro/go/internal/llm"
)

type Endpoint struct {
	Protocol Protocol
	Workload llm.Workload
	Version  string
}

func (e Endpoint) String() string {
	return fmt.Sprintf("%s/%s", e.Protocol, e.Version)
}

var (
	AnthropicMessagesV1 = Endpoint{
		Protocol: ProtocolAnthropicMessages,
		Workload: llm.WorkloadChat,
		Version:  "v1",
	}
	GeminiGenerateContentV1Beta = Endpoint{
		Protocol: ProtocolGeminiGenerateContent,
		Workload: llm.WorkloadChat,
		Version:  "v1beta",
	}
	OpenAIChatCompletionsV1 = Endpoint{
		Protocol: ProtocolOpenAIChatCompletions,
		Workload: llm.WorkloadChat,
		Version:  "v1",
	}
	OpenAIEmbeddingsV1 = Endpoint{
		Protocol: ProtocolOpenAIEmbeddings,
		Workload: llm.WorkloadEmbedding,
		Version:  "v1",
	}
	OpenAIResponsesV1 = Endpoint{
		Protocol: ProtocolOpenAIResponses,
		Workload: llm.WorkloadChat,
		Version:  "v1",
	}
)

type IngressRoute struct {
	Method  string
	Pattern string
}

type Capabilities struct {
	IngressRoutes     []IngressRoute
	Streaming         bool
	OpaquePassthrough bool
}
