package llm

// EmbeddingInput is the normalized input accepted by an embedding model.
type EmbeddingInput interface{ embeddingInput() }

// TextInput is one text value to embed.
type TextInput struct{ Text string }

func (*TextInput) embeddingInput() {}

// TextBatchInput is a batch of text values to embed.
type TextBatchInput struct{ Texts []string }

func (*TextBatchInput) embeddingInput() {}

// TokenInput is one token sequence to embed.
type TokenInput struct{ Tokens []int }

func (*TokenInput) embeddingInput() {}

// TokenBatchInput is a batch of token sequences to embed.
type TokenBatchInput struct{ Batches [][]int }

func (*TokenBatchInput) embeddingInput() {}

// EmbeddingRequest is the canonical request for an embedding workload.
type EmbeddingRequest struct {
	Model      string
	Input      EmbeddingInput
	Dimensions *uint32
	Meta       RequestMetadata
}

// NewEmbeddingRequest constructs an EmbeddingRequest with its required model
// and normalized input.
func NewEmbeddingRequest(model string, input EmbeddingInput) *EmbeddingRequest {
	return &EmbeddingRequest{Model: model, Input: input}
}

func (*EmbeddingRequest) Workload() Workload { return WorkloadEmbedding }

// ModelID returns the model selected for the request.
func (r *EmbeddingRequest) ModelID() string { return r.Model }

// SetModelID replaces the model selected for the request.
func (r *EmbeddingRequest) SetModelID(model string) { r.Model = model }
