// Package schema defines telemetry configuration shared across configuration and runtime code.
package schema

// ExporterKind identifies an exporter engine. An empty value means the signal
// is disabled; there is deliberately no registered "none" exporter.
type ExporterKind string

const (
	ExporterKindStdout     ExporterKind = "stdout"
	ExporterKindOTLP       ExporterKind = "otlp"
	ExporterKindPrometheus ExporterKind = "prometheus"
)

// FieldType is the value shape of an exporter configuration field.
type FieldType string

const (
	FieldTypeString   FieldType = "string"
	FieldTypeNumber   FieldType = "number"
	FieldTypeDuration FieldType = "duration"
	FieldTypeSelect   FieldType = "select"
)

// FieldDef describes one configuration field accepted by an exporter.
type FieldDef struct {
	Name     string
	Type     FieldType
	Label    string
	Required bool
	Default  string
	Options  []string
}

// ExporterDef describes one exporter registered for one signal.
type ExporterDef struct {
	Signal Signal
	Kind   ExporterKind
	Fields []FieldDef
}

var exporterFields = map[ExporterKind][]FieldDef{
	ExporterKindStdout: {},
	ExporterKindOTLP: {
		{Name: "endpoint", Type: FieldTypeString, Label: "Endpoint", Required: true},
		{Name: "protocol", Type: FieldTypeSelect, Label: "Protocol", Options: []string{"http", "grpc"}, Default: "http"},
		{Name: "interval", Type: FieldTypeDuration, Label: "Export Interval", Default: "5s"},
	},
	ExporterKindPrometheus: {
		{Name: "listen", Type: FieldTypeString, Label: "Listen Address", Default: ":9464"},
		{Name: "path", Type: FieldTypeString, Label: "Path", Default: "/metrics"},
	},
}

var signalExporters = map[Signal][]ExporterKind{
	SignalLogs:    {ExporterKindStdout, ExporterKindOTLP},
	SignalMetrics: {ExporterKindStdout, ExporterKindOTLP, ExporterKindPrometheus},
	SignalTraces:  {ExporterKindStdout, ExporterKindOTLP},
}

// ExportersFor returns the exporter definitions available for signal in
// display order. Unknown signals return an empty slice.
func ExportersFor(signal Signal) []ExporterDef {
	kinds := signalExporters[signal]
	defs := make([]ExporterDef, 0, len(kinds))
	for _, kind := range kinds {
		defs = append(defs, ExporterDef{
			Signal: signal,
			Kind:   kind,
			Fields: exporterFields[kind],
		})
	}
	return defs
}
