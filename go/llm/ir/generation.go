package ir

// GenerationConfig holds core generation parameters shared across all
// supported protocols. Optional knobs use pointers to distinguish unset.
type GenerationConfig struct {
	Temperature      *float64
	MaxTokens        *uint32
	TopP             *float64
	Seed             *int64
	Stop             []string
	PresencePenalty  *float64
	FrequencyPenalty *float64
}

// StreamConfig holds streaming configuration.
type StreamConfig struct {
	Enabled      bool
	IncludeUsage bool
}
