package gemini

import "strings"

// normalizeGeminiFinishReason maps Gemini SCREAMING_SNAKE finishReasons to the
// IR/Anthropic/OpenAI convention (stop/length/content_filter).
func normalizeGeminiFinishReason(s string) string {
	switch s {
	case "STOP":
		return "stop"
	case "MAX_TOKENS":
		return "length"
	case "SAFETY", "RECITATION":
		return "content_filter"
	default:
		return strings.ToLower(s)
	}
}

// denormalizeGeminiFinishReason maps a canonical IR stop reason (which may use
// OpenAI-style "stop"/"tool_calls"/"length" or Anthropic-style
// "end_turn"/"tool_use"/"max_tokens", depending on the source codec) back to
// Gemini's SCREAMING_SNAKE finishReason. Gemini has no tool-call finish reason,
// so tool stops become STOP — the tool is signalled by the functionCall part.
func denormalizeGeminiFinishReason(s string) string {
	switch s {
	case "stop", "tool_calls", "tool_use", "end_turn":
		return "STOP"
	case "length", "max_tokens":
		return "MAX_TOKENS"
	case "content_filter":
		return "SAFETY"
	case "":
		return ""
	default:
		return strings.ToUpper(s)
	}
}
