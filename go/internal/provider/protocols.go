package provider

import "github.com/nyroway/nyro/go/internal/protocol/ids"

// Protocol IDs are owned by protocol/ids (see the cloud-routing notes there).
// These untyped aliases exist so provider code and storage rows, which carry
// protocols as plain strings, can compare without conversions.
const (
	ProtocolOpenAICompatible  = string(ids.ProtocolOpenAICompatible)
	ProtocolOpenAIResponses   = string(ids.ProtocolOpenAIResponses)
	ProtocolAnthropicMessages = string(ids.ProtocolAnthropicMessages)
	ProtocolGoogleGemini      = string(ids.ProtocolGoogleGemini)
	ProtocolBedrockConverse   = string(ids.ProtocolBedrockConverse)
	ProtocolAzureOpenAI       = string(ids.ProtocolAzureOpenAI)
)
