package ir

// ReasoningEffort is the effort level for reasoning / thinking models.
// Sealed union: discrete levels plus a token-budget variant.
// Ported from ReasoningEffort (serde rename_all = "lowercase").
type ReasoningEffort interface{ reasoningEffort() }

// ReasoningNone disables reasoning.
type ReasoningNone struct{}

func (*ReasoningNone) reasoningEffort() {}

// ReasoningMinimal is the minimal effort level.
type ReasoningMinimal struct{}

func (*ReasoningMinimal) reasoningEffort() {}

// ReasoningLow is the low effort level.
type ReasoningLow struct{}

func (*ReasoningLow) reasoningEffort() {}

// ReasoningMedium is the medium effort level.
type ReasoningMedium struct{}

func (*ReasoningMedium) reasoningEffort() {}

// ReasoningHigh is the high effort level.
type ReasoningHigh struct{}

func (*ReasoningHigh) reasoningEffort() {}

// ReasoningXhigh is the extra-high effort level.
type ReasoningXhigh struct{}

func (*ReasoningXhigh) reasoningEffort() {}

// ReasoningBudget sets a token budget for thinking (Anthropic budget_tokens).
type ReasoningBudget struct{ Budget uint32 }

func (*ReasoningBudget) reasoningEffort() {}

// ReasoningConfig holds reasoning / extended-thinking configuration.
// Normalized from OpenAI reasoning.effort/summary and Anthropic thinking.
type ReasoningConfig struct {
	Enabled      bool
	BudgetTokens *uint32
	Effort       ReasoningEffort // optional (nil = absent)
	Display      string          // optional (Anthropic "summarized" | "omitted")
}
