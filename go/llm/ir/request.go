package ir

// AiRequest is the unified ingress IR consumed by all codec encoders and the
// dispatcher. Ported from AiRequest.
//
// Fields map the FIELD_HOMING categories: [IR] core fields live directly on
// the struct; protocol-specific fields live on the matching ProtocolExt.
type AiRequest struct {
	// ── Core ──
	Model    string
	Messages []Message
	System   string // optional (extracted from a leading system message or top-level field)

	// ── Generation / streaming ──
	Generation GenerationConfig
	Stream     StreamConfig

	// ── Tools ──
	Tools                    []ToolSpec // optional (nil = absent)
	ToolChoice               ToolChoice // optional interface (nil = absent)
	ParallelToolCalls        *bool
	DisableParallelToolCalls *bool

	// ── Reasoning / output / safety ──
	Reasoning      ReasoningConfig
	ResponseFormat ResponseFormat   // optional interface (nil = absent)
	SafetySettings []SafetySettings // optional (Google; ignored elsewhere)

	// ── Protocol extension ──
	Ext ProtocolExt // optional interface (nil = absent)

	// ── Metadata / vendor bag ──
	Meta RequestMetadata
}

// NewAiRequest constructs an AiRequest with the minimal required fields.
func NewAiRequest(model string, messages []Message) *AiRequest {
	return &AiRequest{
		Model:    model,
		Messages: messages,
	}
}

// Modalities returns the OpenAIChatExt modalities, if present.
func (r *AiRequest) Modalities() []string {
	if e, ok := r.Ext.(*OpenAIChatExt); ok {
		return e.Modalities
	}
	return nil
}
