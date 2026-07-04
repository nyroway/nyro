package provider

import "github.com/nyroway/nyro/go/internal/protocol/ids"

// Protocol IDs are owned by protocol/ids (see the cloud-routing notes there).
// These untyped aliases exist so provider code and storage rows, which carry
// protocols as plain strings, can compare without conversions.
const (
	ProtocolOpenAIChatCompletions = string(ids.ProtocolOpenAIChatCompletions)
	ProtocolOpenAIResponses       = string(ids.ProtocolOpenAIResponses)
	ProtocolAnthropicMessages     = string(ids.ProtocolAnthropicMessages)
	ProtocolGeminiGenerateContent = string(ids.ProtocolGeminiGenerateContent)
	ProtocolGeminiInteractions    = string(ids.ProtocolGeminiInteractions)
	ProtocolBedrockConverse       = string(ids.ProtocolBedrockConverse)
	ProtocolAzureModelInference   = string(ids.ProtocolAzureModelInference)
)
