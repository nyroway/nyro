package telemetry

// builderFunc constructs the concrete OTel exporter for one signal and kind.
// Concrete exporter types differ across logs, metrics, and traces, so callers
// perform the signal-specific type assertion.
type builderFunc func(params map[string]string) (any, error)
