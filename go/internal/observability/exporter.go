package observability

// Signal identifies one of the three observability signal types. Each signal
// has its own independent exporter selection and field configuration — there
// is no shared/global exporter setting.
type Signal string

const (
	SignalLogs    Signal = "logs"
	SignalMetrics Signal = "metrics"
	SignalTraces  Signal = "traces"
)

// ExporterKind identifies an exporter engine. There is deliberately no
// "none"/empty-sentinel kind: an unset (empty string) exporter means the
// signal is disabled, and callers test for that by comparing against "",
// not by looking up a registered kind. ExportersFor therefore never returns
// an entry named "none".
type ExporterKind string

const (
	ExporterKindStdout     ExporterKind = "stdout"
	ExporterKindOTLP       ExporterKind = "otlp"
	ExporterKindPrometheus ExporterKind = "prometheus"
)

// FieldType is the value shape of a FieldDef, used by callers (config
// validation, WebUI form rendering) to interpret Default/Options correctly.
type FieldType string

const (
	FieldTypeString   FieldType = "string"
	FieldTypeNumber   FieldType = "number"
	FieldTypeDuration FieldType = "duration"
	FieldTypeSelect   FieldType = "select"
)

// FieldDef describes one configuration field an exporter accepts. It mirrors
// the shape of provider.CredentialField (a struct describing one field) but
// is otherwise unrelated: exporters are registered in a real map-driven
// registry below, not a hardcoded switch.
type FieldDef struct {
	Name     string
	Type     FieldType
	Label    string
	Required bool
	// Default is the field's default value, always represented as a string
	// (e.g. "5s" for a duration, "http" for a select). Empty means "no
	// default" — this is distinct from Required, since a field can be
	// optional with no default (left unset) or required with no default
	// (caller/seed must always supply it, e.g. otlp's endpoint).
	Default string
	// Options enumerates the valid values for a "select" field (e.g.
	// protocol's ["http","grpc"]). Unused for other Types.
	Options []string
}

// ExporterDef is one (signal, exporter kind) registration: the exporter
// engine available for that signal, plus the fields it accepts.
type ExporterDef struct {
	Signal Signal
	Kind   ExporterKind
	Fields []FieldDef
}

// BuilderFunc constructs the concrete OTel exporter for one (signal, kind)
// registration from its resolved field values (raw strings, keyed by
// FieldDef.Name — e.g. {"endpoint": "http://127.0.0.1:19531", "protocol":
// "http"}). Its return type is intentionally `any`: logs/metrics/traces
// exporters have unrelated OTel SDK types (otlploghttp.Exporter vs
// otlpmetrichttp.Exporter vs otlptracehttp.Exporter, etc.), so the concrete
// construction and type assertion is left to the caller (provider.go,
// Task 4). This task defines only the signature; no BuilderFunc values are
// registered here yet.
type BuilderFunc func(params map[string]string) (any, error)

// otlpFields are the fields accepted by the otlp exporter, identical across
// all three signals. endpoint has no Default: the built-in "point at the
// admin receiver" address is a runtime/seed-provided value, not a registry
// default.
var otlpFields = []FieldDef{
	{Name: "endpoint", Type: FieldTypeString, Label: "Endpoint", Required: true},
	{Name: "protocol", Type: FieldTypeSelect, Label: "Protocol", Options: []string{"http", "grpc"}, Default: "http"},
	{Name: "interval", Type: FieldTypeDuration, Label: "Export Interval", Default: "5s"},
}

// prometheusFields are the fields accepted by the prometheus exporter
// (metrics-only).
var prometheusFields = []FieldDef{
	{Name: "listen", Type: FieldTypeString, Label: "Listen Address", Default: ":9464"},
	{Name: "path", Type: FieldTypeString, Label: "Path", Default: "/metrics"},
}

// stdoutFields are the fields accepted by the stdout exporter: none.
var stdoutFields = []FieldDef{}

// exporterFields maps each exporter kind to its field schema. This is the
// per-engine half of the registry: adding a new engine (e.g. graphite) means
// adding one entry here (plus one entry per applicable signal in
// signalExporters below) — no call site changes.
var exporterFields = map[ExporterKind][]FieldDef{
	ExporterKindStdout:     stdoutFields,
	ExporterKindOTLP:       otlpFields,
	ExporterKindPrometheus: prometheusFields,
}

// signalExporters maps each signal to the exporter kinds valid for it, in
// display order. This is the per-signal half of the registry.
var signalExporters = map[Signal][]ExporterKind{
	SignalLogs:    {ExporterKindStdout, ExporterKindOTLP},
	SignalMetrics: {ExporterKindStdout, ExporterKindOTLP, ExporterKindPrometheus},
	SignalTraces:  {ExporterKindStdout, ExporterKindOTLP},
}

// ExportersFor returns the exporter engines available for signal, each with
// its field schema. It is the backend source of truth later tasks (LoadConfig
// validation, NewProvider construction, WebUI field rendering) query against;
// it is map-driven, not a hardcoded switch, so registering a new engine for a
// signal does not require touching this function.
//
// The returned slice never contains a "none" entry: an empty/absent exporter
// selection means "disabled" and is represented by callers checking for the
// empty string, not by a registered kind.
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
