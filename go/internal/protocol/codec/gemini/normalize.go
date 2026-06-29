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
