// Manual frontend mirror of go/internal/observability/exporter.go's exporter
// registry (Signal / ExporterKind / FieldDef / ExporterDef / ExportersFor).
//
// This is intentionally a static, hand-maintained copy — not fetched from the
// backend at runtime — because the WebUI runs in two modes (Tauri IPC and
// HTTP-against-admin) and neither should require a new command/endpoint just
// to read a schema that changes rarely. Keep field names, defaults, and
// labels in exact sync with exporter.go; if that file changes, update this
// file in the same change.
//
// There is deliberately no "none" exporter kind: an empty string exporter
// selection means the signal is disabled, mirroring exporter.go's contract.

export type Signal = "logs" | "metrics" | "traces";

export const SIGNALS: Signal[] = ["logs", "metrics", "traces"];

export type ExporterKind = "stdout" | "otlp" | "prometheus";

export type FieldType = "string" | "number" | "duration" | "select";

export interface FieldDef {
  name: string;
  type: FieldType;
  label: string;
  required?: boolean;
  /** Default value, always a string (e.g. "5s", "http"). Empty = no default. */
  default?: string;
  /** Valid values for a "select" field. Unused otherwise. */
  options?: string[];
}

export interface ExporterDef {
  kind: ExporterKind;
  fields: FieldDef[];
}

// otlpFields are the fields accepted by the otlp exporter, identical across
// all three signals. endpoint has no default: the "point at the built-in
// admin receiver" address is a runtime value (see settings.tsx's
// builtInOtlpEndpoint), not a registry default.
const otlpFields: FieldDef[] = [
  { name: "endpoint", type: "string", label: "Endpoint", required: true },
  { name: "protocol", type: "select", label: "Protocol", options: ["http", "grpc"], default: "http" },
  { name: "interval", type: "duration", label: "Export Interval", default: "5s" },
];

// prometheusFields are the fields accepted by the prometheus exporter
// (metrics-only).
const prometheusFields: FieldDef[] = [
  { name: "listen", type: "string", label: "Listen Address", default: ":9464" },
  { name: "path", type: "string", label: "Path", default: "/metrics" },
];

// stdoutFields are the fields accepted by the stdout exporter: none.
const stdoutFields: FieldDef[] = [];

const exporterFields: Record<ExporterKind, FieldDef[]> = {
  stdout: stdoutFields,
  otlp: otlpFields,
  prometheus: prometheusFields,
};

// signalExporters maps each signal to the exporter kinds valid for it, in
// display order.
const signalExporters: Record<Signal, ExporterKind[]> = {
  logs: ["stdout", "otlp"],
  metrics: ["stdout", "otlp", "prometheus"],
  traces: ["stdout", "otlp"],
};

// exportersFor returns the exporter engines available for signal, each with
// its field schema. Mirrors observability.ExportersFor exactly.
export function exportersFor(signal: Signal): ExporterDef[] {
  return signalExporters[signal].map((kind) => ({ kind, fields: exporterFields[kind] }));
}

export function exporterKindLabel(kind: ExporterKind): string {
  switch (kind) {
    case "stdout":
      return "stdout";
    case "otlp":
      return "OTLP";
    case "prometheus":
      return "Prometheus";
    default:
      return kind;
  }
}

// settingKey builds the storage key for one field of one (signal, engine)
// registration: obs_<signal>_<engine>_<field>. The exporter-selection key
// itself is obs_<signal>_exporter (built separately, it has no engine/field).
export function settingKey(signal: Signal, kind: ExporterKind, field: string): string {
  return `obs_${signal}_${kind}_${field}`;
}

export function exporterSettingKey(signal: Signal): string {
  return `obs_${signal}_exporter`;
}

export function retentionSettingKey(signal: Signal): string {
  return `obs_${signal}_retention_days`;
}

export function flushIntervalSettingKey(signal: Signal): string {
  return `obs_${signal}_flush_interval`;
}
