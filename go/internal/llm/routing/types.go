package routing

// Target is one in-memory route candidate. Runtime projects it from a
// persisted/config-synchronized route binding before selection.
type Target struct {
	UpstreamID string
	Model      string
	Weight     int32
	Priority   int32
}

// Strategy selects how route targets are ordered.
type Strategy string

const (
	StrategyWeighted Strategy = "weighted"
	StrategyPriority Strategy = "priority"
	StrategyCooldown Strategy = "cooldown"
	StrategyLatency  Strategy = "latency"
)
