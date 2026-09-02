package chatcompletions

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nyroway/nyro/go/internal/llm"
	"github.com/nyroway/nyro/go/internal/llm/protocol"
)

func TestConstructorsExposeChatCompletionsEndpoint(t *testing.T) {
	t.Parallel()
	if got := NewIngress().Endpoint(); got != protocol.OpenAIChatCompletionsV1 {
		t.Errorf("ingress endpoint = %v", got)
	}
	if got := NewEgress().Endpoint(); got != protocol.OpenAIChatCompletionsV1 {
		t.Errorf("egress endpoint = %v", got)
	}
}

func TestEgressDecodeErrorPreservesOpenAISemantics(t *testing.T) {
	codec := NewEgress().(protocol.ChatEgressCodec)
	body := []byte(`{"error":{"message":"maximum context is 8k","type":"invalid_request_error","code":"context_length_exceeded"}}`)

	got, err := codec.DecodeError(protocol.WireResponse{Status: 400, Body: body})

	if err != nil {
		t.Fatalf("DecodeError: %v", err)
	}
	if got.Kind != llm.ErrContextLengthExceeded || got.Message != "maximum context is 8k" || got.StatusCode == nil || *got.StatusCode != 400 || string(got.Raw) != string(body) {
		t.Fatalf("decoded error = %+v", got)
	}
}

func TestRequestRoundTrip(t *testing.T) {
	t.Parallel()
	in := `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],` +
		`"temperature":0.7,"max_tokens":100,"stream":true,` +
		`"stream_options":{"include_usage":true},` +
		`"tools":[{"type":"function","function":{"name":"get_weather","description":"w","parameters":{"type":"object"}}}],` +
		`"tool_choice":"auto"}`

	req, err := requestDecoder{}.Decode([]byte(in))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if req.Model != "gpt-4o" {
		t.Errorf("model=%q", req.Model)
	}
	if len(req.Messages) != 1 || llm.ToText(req.Messages[0].Content) != "hi" {
		t.Errorf("messages wrong: %+v", req.Messages)
	}
	if req.Generation.Temperature == nil || *req.Generation.Temperature != 0.7 {
		t.Errorf("temp=%v", req.Generation.Temperature)
	}
	if req.Generation.MaxTokens == nil || *req.Generation.MaxTokens != 100 {
		t.Errorf("max_tokens=%v", req.Generation.MaxTokens)
	}
	if !req.Stream.Enabled || !req.Stream.IncludeUsage {
		t.Errorf("stream config=%+v", req.Stream)
	}
	if len(req.Tools) != 1 || req.Tools[0].Name != "get_weather" {
		t.Errorf("tools=%+v", req.Tools)
	}
	if _, ok := req.ToolChoice.(*llm.AutoToolChoice); !ok {
		t.Errorf("tool_choice type=%T", req.ToolChoice)
	}

	out, err := requestEncoder{}.Encode(req)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if out.Path != "/v1/chat/completions" || !out.Stream {
		t.Errorf("outbound=%+v", out)
	}
	var w chatRequest
	if err := json.Unmarshal(out.Body, &w); err != nil {
		t.Fatalf("re-decode: %v", err)
	}
	if w.Model != "gpt-4o" || w.Temperature == nil || *w.Temperature != 0.7 {
		t.Errorf("wire round-trip wrong: %+v", w)
	}
}

func TestStreamDecode(t *testing.T) {
	t.Parallel()
	d := &streamResponseDecoder{}
	chunks := []string{
		`{"id":"c1","model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant"}}]}`,
		`{"id":"c1","model":"gpt-4o","choices":[{"index":0,"delta":{"content":"Hel"}}]}`,
		`{"id":"c1","model":"gpt-4o","choices":[{"index":0,"delta":{"content":"lo"}}]}`,
		`{"id":"c1","model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		"[DONE]",
	}
	var text strings.Builder
	var sawStart, sawDone bool
	for _, c := range chunks {
		deltas, err := d.ParseChunk(c)
		if err != nil {
			t.Fatalf("parse %q: %v", c, err)
		}
		for _, dl := range deltas {
			switch v := dl.(type) {
			case *llm.MessageStartDelta:
				sawStart = true
				if v.ID != "c1" || v.Model != "gpt-4o" {
					t.Errorf("start=%+v", v)
				}
			case *llm.TextDelta:
				text.WriteString(v.Text)
			case *llm.DoneDelta:
				sawDone = true
				if v.StopReason != "stop" {
					t.Errorf("stop=%q", v.StopReason)
				}
			}
		}
	}
	if !sawStart {
		t.Error("no MessageStart delta")
	}
	if text.String() != "Hello" {
		t.Errorf("text=%q", text.String())
	}
	if !sawDone {
		t.Error("no Done delta")
	}
}

func TestStreamDecodeErrorEnvelopeAsCanonicalError(t *testing.T) {
	payload := `{"error":{"message":"request rate exceeded","type":"rate_limit_error"}}`
	deltas, err := (&streamResponseDecoder{}).ParseChunk(payload)

	if err != nil {
		t.Fatalf("ParseChunk: %v", err)
	}
	if len(deltas) != 1 {
		t.Fatalf("deltas = %#v, want one terminal error", deltas)
	}
	terminal, ok := deltas[0].(*llm.StreamErrorDelta)
	if !ok || terminal.Error == nil || terminal.Error.Kind != llm.ErrRateLimitError || terminal.Error.Message != "request rate exceeded" || string(terminal.Error.Raw) != payload {
		t.Fatalf("terminal delta = %#v", deltas[0])
	}
}

func TestStreamEncode(t *testing.T) {
	t.Parallel()
	e := &streamResponseEncoder{}
	deltas := []llm.StreamDelta{
		&llm.MessageStartDelta{ID: "c1", Model: "gpt-4o"},
		&llm.TextDelta{Text: "Hi"},
	}
	sse, err := e.FormatDeltas(deltas)
	if err != nil {
		t.Fatalf("format: %v", err)
	}
	if len(sse) != 2 {
		t.Fatalf("got %d frames, want 2", len(sse))
	}

	done, err := e.FormatDone(llm.Usage{PromptTokens: 5, CompletionTokens: 2, TotalTokens: 7})
	if err != nil {
		t.Fatalf("done: %v", err)
	}
	if len(done) != 2 || done[1].Data != "[DONE]" {
		t.Errorf("done frames=%+v", done)
	}
	var chunk chatCompletionChunk
	if err := json.Unmarshal([]byte(done[0].Data), &chunk); err != nil {
		t.Fatalf("usage chunk parse: %v", err)
	}
	if chunk.Usage == nil || chunk.Usage.TotalTokens != 7 {
		t.Errorf("usage=%+v", chunk.Usage)
	}
}

func TestNonStreamResponse(t *testing.T) {
	t.Parallel()
	body := `{"id":"r1","object":"chat.completion","model":"gpt-4o",` +
		`"choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],` +
		`"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`

	resp, err := responseDecoder{}.Parse([]byte(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if resp.Content != "hello" || resp.StopReason != "stop" || resp.Usage.TotalTokens != 5 {
		t.Errorf("resp=%+v", resp)
	}

	out, err := responseEncoder{}.Format(resp)
	if err != nil {
		t.Fatalf("format: %v", err)
	}
	var w chatCompletion
	if err := json.Unmarshal(out, &w); err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	if w.Object != "chat.completion" || len(w.Choices) != 1 || w.Choices[0].Message.Role != "assistant" {
		t.Errorf("wire=%+v", w)
	}
}
