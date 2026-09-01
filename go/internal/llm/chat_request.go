package llm

// ChatRequest is the canonical request shared by supported chat-style model
// protocols.
//
// Fields map the FIELD_HOMING categories: [IR] core fields live directly on
// the struct; protocol-specific fields live on the matching ProtocolExt.
type ChatRequest struct {
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

// NewChatRequest constructs a ChatRequest with the minimal required fields.
func NewChatRequest(model string, messages []Message) *ChatRequest {
	return &ChatRequest{
		Model:    model,
		Messages: messages,
	}
}

func (*ChatRequest) Workload() Workload { return WorkloadChat }

// ModelID returns the model selected for the request.
func (r *ChatRequest) ModelID() string { return r.Model }

// SetModelID replaces the model selected for the request.
func (r *ChatRequest) SetModelID(model string) { r.Model = model }

// Modalities returns the OpenAIChatExt modalities, if present.
func (r *ChatRequest) Modalities() []string {
	if e, ok := r.Ext.(*OpenAIChatExt); ok {
		return e.Modalities
	}
	return nil
}
