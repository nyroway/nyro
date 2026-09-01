package provider

import "github.com/nyroway/nyro/go/internal/llm/protocol"

// Protocol IDs are owned by protocol/ids (see the cloud-routing notes there).
// These untyped aliases exist so provider code and storage rows, which carry
// protocols as plain strings, can compare without conversions.
const (
	ProtocolOpenAIChatCompletions = string(protocol.ProtocolOpenAIChatCompletions)
	ProtocolOpenAIEmbeddings      = string(protocol.ProtocolOpenAIEmbeddings)
	ProtocolOpenAIResponses       = string(protocol.ProtocolOpenAIResponses)
	ProtocolAnthropicMessages     = string(protocol.ProtocolAnthropicMessages)
	ProtocolGeminiGenerateContent = string(protocol.ProtocolGeminiGenerateContent)
)
