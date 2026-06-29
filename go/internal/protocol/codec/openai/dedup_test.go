package openai

import (
	"testing"

	"github.com/nyroway/nyro/go/internal/protocol/ir"
)

func TestNormalizeToolCallIDs(t *testing.T) {
	t.Parallel()
	msgs := []ir.Message{
		{Role: ir.RoleAssistant, ToolCalls: []ir.ToolCall{{ID: "call_a", Name: "Bash"}}},
		{Role: ir.RoleTool, ToolCallID: "call_a", Content: &ir.TextContent{Text: "result1"}},
		{Role: ir.RoleAssistant, ToolCalls: []ir.ToolCall{{ID: "call_a", Name: "Bash"}}}, // duplicate
		{Role: ir.RoleTool, ToolCallID: "call_a", Content: &ir.TextContent{Text: "result2"}},
	}
	out := normalizeToolCallIDs(msgs)
	// First occurrence keeps original ID.
	if out[0].ToolCalls[0].ID != "call_a" {
		t.Errorf("first ID should be unchanged: %s", out[0].ToolCalls[0].ID)
	}
	// Second occurrence should be remapped.
	if out[2].ToolCalls[0].ID == "call_a" {
		t.Error("duplicate ID should be remapped")
	}
	// The matching tool_result should reference the new ID.
	if out[3].ToolCallID != out[2].ToolCalls[0].ID {
		t.Errorf("tool_result ID %q should match remapped %q", out[3].ToolCallID, out[2].ToolCalls[0].ID)
	}
	// Non-duplicate case: no-op.
	clean := []ir.Message{
		{Role: ir.RoleUser, Content: &ir.TextContent{Text: "hi"}},
	}
	result := normalizeToolCallIDs(clean)
	if len(result) != 1 || result[0].Role != ir.RoleUser {
		t.Error("normalize should be a no-op for messages without tool calls")
	}

	// Orphan pruning: last assistant with tool_calls that have no results.
	orphan := []ir.Message{
		{Role: ir.RoleUser, Content: &ir.TextContent{Text: "hi"}},
		{Role: ir.RoleAssistant, ToolCalls: []ir.ToolCall{{ID: "orphan_call", Name: "Bash"}}},
	}
	normalized := normalizeToolCallIDs(orphan)
	if len(normalized[1].ToolCalls) != 0 {
		t.Errorf("orphan tool_call should be pruned, got %d calls", len(normalized[1].ToolCalls))
	}
}
