package chatcompletions

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nyroway/nyro/go/internal/llm"
)

// TestEncodeOmitsContentForToolOnlyAssistant is the Go port of Rust regression
// #233: an assistant turn carrying only a tool call (ToolUse in content blocks
// + tool_calls) must NOT emit a `content` field. Strict OpenAI upstreams reject
// `content:[]`, and ToolUse must not leak as a `{type:"function"}` content part
// (it lives solely in `tool_calls`).
func TestEncodeOmitsContentForToolOnlyAssistant(t *testing.T) {
	t.Parallel()
	req := llm.NewChatRequest("gpt-4o", []llm.Message{
		{
			Role: llm.RoleAssistant,
			Content: &llm.BlocksContent{Blocks: []llm.ContentBlock{
				&llm.ToolUseBlock{ID: "call_a", Name: "Bash", Input: json.RawMessage(`{"command":"ls"}`)},
			}},
			ToolCalls: []llm.ToolCall{{ID: "call_a", Name: "Bash", Arguments: `{"command":"ls"}`}},
		},
		{
			Role:       llm.RoleTool,
			ToolCallID: "call_a",
			Content:    &llm.TextContent{Text: "file1\nfile2"},
		},
	})
	out, err := requestEncoder{}.Encode(req)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	s := string(out.Body)
	// The ASSISTANT message must not have a "content" field. (The tool message
	// legitimately has content — the test only checks the assistant.)
	if strings.Contains(s, `"role":"assistant","content"`) {
		t.Errorf("assistant content must be omitted for a tool-only turn: %s", s)
	}
	if !strings.Contains(s, `"tool_calls"`) || !strings.Contains(s, `"Bash"`) {
		t.Errorf("tool_calls must be preserved: %s", s)
	}
	// No function part may appear anywhere as a content block.
	if strings.Contains(s, `{"type":"function"}`) {
		t.Errorf("ToolUse leaked as a content function part: %s", s)
	}
}
