package llm_test

import (
	"testing"

	"github.com/nyroway/nyro/go/internal/llm"
)

func TestModelRequestsExposeTheirWorkloadAndMutableModel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		req  llm.ModelRequest
		want llm.Workload
	}{
		{
			name: "chat",
			req:  llm.NewChatRequest("client-chat", nil),
			want: llm.WorkloadChat,
		},
		{
			name: "embedding",
			req:  llm.NewEmbeddingRequest("client-embedding", &llm.TextInput{Text: "hello"}),
			want: llm.WorkloadEmbedding,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.req.Workload(); got != tt.want {
				t.Fatalf("Workload() = %q, want %q", got, tt.want)
			}
			if got := tt.req.ModelID(); got == "" {
				t.Fatal("ModelID() is empty")
			}
			tt.req.SetModelID("upstream-model")
			if got := tt.req.ModelID(); got != "upstream-model" {
				t.Fatalf("ModelID() after SetModelID() = %q, want upstream-model", got)
			}
		})
	}
}

func TestNormalizedErrorFromStatus(t *testing.T) {
	t.Parallel()

	err := llm.ErrorFromStatus(429, "slow down")
	if err.Kind != llm.ErrRateLimitError {
		t.Fatalf("Kind = %q, want %q", err.Kind, llm.ErrRateLimitError)
	}
	if !err.IsRetryable() {
		t.Fatal("429 error is not retryable")
	}
}
