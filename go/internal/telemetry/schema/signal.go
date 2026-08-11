package schema

// Signal identifies one of the three independently configured telemetry
// signal types.
type Signal string

const (
	SignalLogs    Signal = "logs"
	SignalMetrics Signal = "metrics"
	SignalTraces  Signal = "traces"
)
