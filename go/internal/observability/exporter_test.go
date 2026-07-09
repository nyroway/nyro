package observability

import (
	"reflect"
	"testing"
)

func kindsOf(defs []ExporterDef) []ExporterKind {
	out := make([]ExporterKind, len(defs))
	for i, d := range defs {
		out[i] = d.Kind
	}
	return out
}

func TestExportersFor_ValidEngineSets(t *testing.T) {
	tests := []struct {
		signal Signal
		want   []ExporterKind
	}{
		{SignalLogs, []ExporterKind{ExporterKindStdout, ExporterKindOTLP}},
		{SignalMetrics, []ExporterKind{ExporterKindStdout, ExporterKindOTLP, ExporterKindPrometheus}},
		{SignalTraces, []ExporterKind{ExporterKindStdout, ExporterKindOTLP}},
	}
	for _, tt := range tests {
		t.Run(string(tt.signal), func(t *testing.T) {
			got := kindsOf(ExportersFor(tt.signal))
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ExportersFor(%s) kinds = %v, want %v", tt.signal, got, tt.want)
			}
		})
	}
}

func TestExportersFor_NoNoneSentinel(t *testing.T) {
	for _, signal := range []Signal{SignalLogs, SignalMetrics, SignalTraces} {
		for _, def := range ExportersFor(signal) {
			if def.Kind == "none" {
				t.Errorf("ExportersFor(%s) contains a 'none' entry; empty string must represent disabled instead", signal)
			}
		}
	}
}

func TestExportersFor_MetricsOnlyPrometheus(t *testing.T) {
	for _, signal := range []Signal{SignalLogs, SignalTraces} {
		for _, def := range ExportersFor(signal) {
			if def.Kind == ExporterKindPrometheus {
				t.Errorf("ExportersFor(%s) unexpectedly contains prometheus (metrics-only engine)", signal)
			}
		}
	}
	found := false
	for _, def := range ExportersFor(SignalMetrics) {
		if def.Kind == ExporterKindPrometheus {
			found = true
		}
	}
	if !found {
		t.Error("ExportersFor(metrics) must contain prometheus")
	}
}

func fieldByName(fields []FieldDef, name string) (FieldDef, bool) {
	for _, f := range fields {
		if f.Name == name {
			return f, true
		}
	}
	return FieldDef{}, false
}

func defByKind(defs []ExporterDef, kind ExporterKind) (ExporterDef, bool) {
	for _, d := range defs {
		if d.Kind == kind {
			return d, true
		}
	}
	return ExporterDef{}, false
}

func TestOTLPFieldSchema(t *testing.T) {
	def, ok := defByKind(ExportersFor(SignalTraces), ExporterKindOTLP)
	if !ok {
		t.Fatal("expected otlp in ExportersFor(traces)")
	}
	if len(def.Fields) != 3 {
		t.Fatalf("otlp fields = %d, want 3", len(def.Fields))
	}

	endpoint, ok := fieldByName(def.Fields, "endpoint")
	if !ok {
		t.Fatal("otlp missing endpoint field")
	}
	if endpoint.Type != FieldTypeString || !endpoint.Required || endpoint.Default != "" {
		t.Errorf("endpoint field = %+v, want string/required/no-default", endpoint)
	}

	protocol, ok := fieldByName(def.Fields, "protocol")
	if !ok {
		t.Fatal("otlp missing protocol field")
	}
	if protocol.Type != FieldTypeSelect || protocol.Default != "http" {
		t.Errorf("protocol field = %+v, want select/default http", protocol)
	}
	if !reflect.DeepEqual(protocol.Options, []string{"http", "grpc"}) {
		t.Errorf("protocol options = %v, want [http grpc]", protocol.Options)
	}

	interval, ok := fieldByName(def.Fields, "interval")
	if !ok {
		t.Fatal("otlp missing interval field")
	}
	if interval.Type != FieldTypeDuration || interval.Required || interval.Default != "5s" {
		t.Errorf("interval field = %+v, want duration/optional/default 5s", interval)
	}
}

func TestPrometheusFieldSchema(t *testing.T) {
	def, ok := defByKind(ExportersFor(SignalMetrics), ExporterKindPrometheus)
	if !ok {
		t.Fatal("expected prometheus in ExportersFor(metrics)")
	}
	if len(def.Fields) != 2 {
		t.Fatalf("prometheus fields = %d, want 2", len(def.Fields))
	}

	listen, ok := fieldByName(def.Fields, "listen")
	if !ok {
		t.Fatal("prometheus missing listen field")
	}
	if listen.Type != FieldTypeString || listen.Default != ":9464" {
		t.Errorf("listen field = %+v, want string/default :9464", listen)
	}

	path, ok := fieldByName(def.Fields, "path")
	if !ok {
		t.Fatal("prometheus missing path field")
	}
	if path.Type != FieldTypeString || path.Default != "/metrics" {
		t.Errorf("path field = %+v, want string/default /metrics", path)
	}
}

func TestStdoutFieldSchema(t *testing.T) {
	for _, signal := range []Signal{SignalLogs, SignalMetrics, SignalTraces} {
		def, ok := defByKind(ExportersFor(signal), ExporterKindStdout)
		if !ok {
			t.Fatalf("expected stdout in ExportersFor(%s)", signal)
		}
		if def.Fields == nil {
			t.Errorf("stdout fields for %s is nil, want empty non-nil slice", signal)
		}
		if len(def.Fields) != 0 {
			t.Errorf("stdout fields for %s = %v, want empty", signal, def.Fields)
		}
	}
}

func TestExportersFor_UnknownSignal(t *testing.T) {
	if got := ExportersFor(Signal("bogus")); len(got) != 0 {
		t.Errorf("ExportersFor(bogus) = %v, want empty", got)
	}
}
