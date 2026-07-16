package anthropic

import (
	"testing"

	"github.com/nyroway/nyro/go/internal/protocol/ir"
)

// feed runs the decoder over a sequence of SSE data payloads and returns the
// final aggregated UsageDelta (the one emitted at message_stop).
func feed(t *testing.T, payloads []string) ir.Usage {
	t.Helper()
	d := &streamResponseDecoder{}
	var last ir.Usage
	seen := false
	for _, p := range payloads {
		deltas, err := d.ParseChunk(p)
		if err != nil {
			t.Fatalf("ParseChunk(%q): %v", p, err)
		}
		for _, dl := range deltas {
			if u, ok := dl.(*ir.UsageDelta); ok {
				last = u.Usage
				seen = true
			}
		}
	}
	if !seen {
		t.Fatal("no UsageDelta emitted")
	}
	return last
}

// TestUsageFromMessageDelta_Zhipu reproduces the exact GLM/Zhipu wire shape:
// message_start reports usage all-zero and the real input/output counts arrive
// only in the final message_delta. The decoder must surface input_tokens=11.
func TestUsageFromMessageDelta_Zhipu(t *testing.T) {
	u := feed(t, []string{
		`{"type":"message_start","message":{"id":"msg_1","model":"glm-5.2","usage":{"input_tokens":0,"output_tokens":0}}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":11,"output_tokens":81,"cache_read_input_tokens":0}}`,
		`{"type":"message_stop"}`,
	})
	if u.PromptTokens != 11 {
		t.Errorf("PromptTokens = %d; want 11 (input_tokens from message_delta)", u.PromptTokens)
	}
	if u.CompletionTokens != 81 {
		t.Errorf("CompletionTokens = %d; want 81", u.CompletionTokens)
	}
}

// TestUsageFromMessageStart_Anthropic guards the real-Anthropic shape: input in
// message_start, message_delta carries only output_tokens. The message_start
// value must be preserved (not clobbered to 0 by the absent delta field).
func TestUsageFromMessageStart_Anthropic(t *testing.T) {
	u := feed(t, []string{
		`{"type":"message_start","message":{"id":"msg_1","model":"claude","usage":{"input_tokens":50}}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":10}}`,
		`{"type":"message_stop"}`,
	})
	if u.PromptTokens != 50 {
		t.Errorf("PromptTokens = %d; want 50 (preserved from message_start)", u.PromptTokens)
	}
	if u.CompletionTokens != 10 {
		t.Errorf("CompletionTokens = %d; want 10", u.CompletionTokens)
	}
}
