package ir

import "testing"

func TestAiErrorKindIsRetryable(t *testing.T) {
	t.Parallel()
	retryable := []AiErrorKind{
		ErrRateLimitError, ErrServerError, ErrServiceUnavailable,
		ErrTimeout, ErrModelNotAvailable, ErrUnexpectedEOF, ErrStreamMidError,
	}
	for _, k := range retryable {
		if !k.IsRetryable() {
			t.Errorf("%s should be retryable", k)
		}
	}
	nonRetryable := []AiErrorKind{
		ErrAuthenticationError, ErrAuthorizationError, ErrQuotaExceeded,
		ErrInvalidRequest, ErrContentFiltered,
	}
	for _, k := range nonRetryable {
		if k.IsRetryable() {
			t.Errorf("%s should not be retryable", k)
		}
	}
}

func TestAiErrorFromStatus(t *testing.T) {
	t.Parallel()
	e := AiErrorFromStatus(429, "slow down")
	if e.Kind != ErrRateLimitError {
		t.Errorf("kind = %s, want rate_limit_error", e.Kind)
	}
	if e.StatusCode == nil || *e.StatusCode != 429 {
		t.Errorf("status = %v, want 429", e.StatusCode)
	}
	if !e.IsRetryable() {
		t.Error("429 should be retryable")
	}
	if e.Error() == "" {
		t.Error("Error() should be non-empty")
	}
}

func TestStreamDeltaTypeSwitch(t *testing.T) {
	t.Parallel()
	var d StreamDelta = &TextDelta{Text: "hi"}
	switch v := d.(type) {
	case *TextDelta:
		if v.Text != "hi" {
			t.Errorf("got %q", v.Text)
		}
	default:
		t.Fatalf("expected *TextDelta, got %T", d)
	}
}

func TestToText(t *testing.T) {
	t.Parallel()
	if got := ToText(&TextContent{Text: "hello"}); got != "hello" {
		t.Errorf("text content: got %q", got)
	}
	blocks := &BlocksContent{Blocks: []ContentBlock{
		&TextBlock{Text: "a"},
		&ToolUseBlock{ID: "x"}, // ignored by ToText
		&TextBlock{Text: "b"},
	}}
	if got := ToText(blocks); got != "ab" {
		t.Errorf("blocks content: got %q", got)
	}
}

func TestNewAiRequestDefaults(t *testing.T) {
	t.Parallel()
	r := NewAiRequest("gpt-4", []Message{{Role: RoleUser, Content: &TextContent{Text: "hi"}}})
	if r.Model != "gpt-4" {
		t.Errorf("model = %q", r.Model)
	}
	if r.Stream.Enabled || r.Generation.Temperature != nil {
		t.Errorf("unexpected non-zero defaults: %+v", r)
	}
	if r.Modalities() != nil {
		t.Error("modalities should be nil without OpenAIChatExt")
	}
}

func TestAiResponseIsError(t *testing.T) {
	t.Parallel()
	r := NewAiResponse("id", "m")
	if r.IsError() {
		t.Error("new response should not be an error")
	}
	r.Error = NewAiError(ErrInvalidRequest, "bad")
	if !r.IsError() {
		t.Error("should be an error after setting Error")
	}
}
