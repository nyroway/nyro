package provider

import "github.com/nyroway/nyro/go/internal/llm/protocol"

const (
	ProtocolOpenAIChatCompletions = string(protocol.ProtocolOpenAIChatCompletions)
	ProtocolOpenAIEmbeddings      = string(protocol.ProtocolOpenAIEmbeddings)
	ProtocolOpenAIResponses       = string(protocol.ProtocolOpenAIResponses)
	ProtocolAnthropicMessages     = string(protocol.ProtocolAnthropicMessages)
	ProtocolGeminiGenerateContent = string(protocol.ProtocolGeminiGenerateContent)
)
