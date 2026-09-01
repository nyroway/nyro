package protocol

import "fmt"

type Protocol string

const (
	ProtocolAnthropicMessages     Protocol = "anthropic-messages"
	ProtocolGeminiGenerateContent Protocol = "gemini-generatecontent"
	ProtocolOpenAIChatCompletions Protocol = "openai-chatcompletions"
	ProtocolOpenAIEmbeddings      Protocol = "openai-embeddings"
	ProtocolOpenAIResponses       Protocol = "openai-responses"
)

func (p Protocol) String() string { return string(p) }

type ProtocolInfo struct {
	ID             Protocol `json:"id"`
	DisplayName    string   `json:"displayName"`
	Alias          string   `json:"alias"`
	DefaultBaseURL string   `json:"defaultBaseUrl"`
	Selectable     bool     `json:"selectable"`
}

var protocols = [...]ProtocolInfo{
	{ProtocolAnthropicMessages, "Anthropic Messages", "claude", "https://api.anthropic.com", true},
	{ProtocolGeminiGenerateContent, "Gemini generateContent", "gemini", "https://generativelanguage.googleapis.com", true},
	{ProtocolOpenAIChatCompletions, "OpenAI Chat Completions", "openai", "https://api.openai.com/v1", true},
	{ProtocolOpenAIEmbeddings, "OpenAI Embeddings", "embed", "https://api.openai.com/v1", false},
	{ProtocolOpenAIResponses, "OpenAI Responses", "codex", "https://api.openai.com/v1", true},
}

func Protocols() []ProtocolInfo {
	out := make([]ProtocolInfo, len(protocols))
	copy(out, protocols[:])
	return out
}

func Lookup(p Protocol) (ProtocolInfo, bool) {
	for _, info := range protocols {
		if info.ID == p {
			return info, true
		}
	}
	return ProtocolInfo{}, false
}

func (p Protocol) DisplayName() string {
	if info, ok := Lookup(p); ok {
		return info.DisplayName
	}
	return "Unknown"
}

func ParseProtocol(value string) (Protocol, error) {
	for _, info := range protocols {
		if value == string(info.ID) || info.Alias != "" && value == info.Alias {
			return info.ID, nil
		}
	}
	return "", fmt.Errorf("unknown protocol: %s", value)
}
