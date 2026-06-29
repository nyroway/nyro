// Package protocol implements the wire-protocol axis of the gateway.
//
// It holds:
//   - the canonical internal representation (AiRequest / AiResponse /
//     AiStreamDelta) that every protocol decodes into and encodes out of,
//   - the six codec interfaces (request decode/encode, response parse/format,
//     stream decode/encode),
//   - the protocol/endpoint identity registry,
//   - protocol negotiation deciding Native (passthrough) vs Transform.
//
// Ported from crates/nyro-core/src/protocol. Unlike Ollama (OpenAI/Anthropic
// only, no Gemini), this package MUST support OpenAI-compatible chat,
// OpenAI Responses, Anthropic Messages, and Google Gemini.
package protocol
