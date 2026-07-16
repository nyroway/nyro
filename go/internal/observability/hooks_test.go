package observability

import (
	"context"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/log/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	metricdata "go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/nyroway/nyro/go/internal/plugin"
	"github.com/nyroway/nyro/go/internal/protocol/ir"
	"github.com/nyroway/nyro/go/internal/storage"
)

// captureLogger is a minimal log.Logger wrapper that records every emitted
// log.Record so the test can assert the audit attributes. It embeds the noop
// logger to satisfy the rest of the log.Logger interface.
type captureLogger struct {
	noop.Logger
	records []log.Record
}

func (c *captureLogger) Emit(ctx context.Context, r log.Record) {
	c.records = append(c.records, r.Clone())
}

// runPhase invokes the registered hooks for the given phase with a fresh
// PhaseContext built from bag (ctx is the background context).
func runPhase(t *testing.T, phase plugin.Phase, bag *plugin.ContextBag) plugin.PhaseOutcome {
	t.Helper()
	pctx := &plugin.PhaseContext{Ctx: context.Background(), Bag: bag}
	return plugin.RunPhaseHooks(phase, pctx)
}

func TestHooksOnRequestStoresSpan(t *testing.T) {
	// Isolated harness: a fresh span recorder + tracer, fresh handles, fresh
	// logger capture. We register hooks on the global plugin registry, which
	// is process-wide — but we assert only what THIS invocation produced.
	spanRecorder := tracetest.NewSpanRecorder()
	tracerProvider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spanRecorder))
	tracer := tracerProvider.Tracer("nyro-test")

	reader := sdkmetric.NewManualReader()
	meterProvider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	handles := NewHandles(meterProvider.Meter("nyro-test"))

	cl := &captureLogger{}
	RegisterHooks(newSwappableFromParts(tracer, cl, handles))
	t.Cleanup(func() {
		_ = tracerProvider.Shutdown(context.Background())
		_ = meterProvider.Shutdown(context.Background())
	})

	bag := plugin.NewContextBag()
	out := runPhase(t, plugin.PhaseOnRequest, bag)
	if out != plugin.OutcomeContinue {
		t.Fatalf("OnRequest: want OutcomeContinue, got %v", out)
	}

	// OnRequest must stash BOTH the span context and the span in the bag.
	v, ok := bag.Get(BagSpanCtx)
	if !ok {
		t.Fatalf("OnRequest did not set %s", BagSpanCtx)
	}
	spanCtx, ok := v.(context.Context)
	if !ok || spanCtx == nil {
		t.Fatalf("OnRequest did not set %s as non-nil context.Context", BagSpanCtx)
	}
	v, ok = bag.Get(BagSpan)
	if !ok {
		t.Fatalf("OnRequest did not set %s", BagSpan)
	}
	if _, ok := v.(trace.Span); !ok {
		t.Fatalf("OnRequest did not set %s as trace.Span", BagSpan)
	}
}

func TestHooksOnLogRecordsMetricsTokensAndSpan(t *testing.T) {
	spanRecorder := tracetest.NewSpanRecorder()
	tracerProvider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spanRecorder))
	tracer := tracerProvider.Tracer("nyro-test")

	reader := sdkmetric.NewManualReader()
	meterProvider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	handles := NewHandles(meterProvider.Meter("nyro-test"))

	cl := &captureLogger{}
	RegisterHooks(newSwappableFromParts(tracer, cl, handles))
	t.Cleanup(func() {
		_ = tracerProvider.Shutdown(context.Background())
		_ = meterProvider.Shutdown(context.Background())
	})

	// Seed the bag with the request state the dispatcher (T3.3) will set.
	bag := plugin.NewContextBag()
	model := storage.Route{ID: "m1", Model: "gpt-test"}
	provider := storage.Upstream{ID: "p1", Name: "openai"}
	cacheRead := uint32(7)
	usage := ir.Usage{PromptTokens: 100, CompletionTokens: 50, CacheReadTokens: &cacheRead}
	started := time.Now().Add(-25 * time.Millisecond) // pretend 25ms elapsed upstream
	status := 200
	lc := LogCtx{
		ClientProtocol:   "openai",
		UpstreamProtocol: "openai",
		ClientModel:      "gpt-test",
		UpstreamModel:    "gpt-4o",
		Method:           "POST",
		Path:             "/v1/chat/completions",
		APIKeyName:       "test-key",
		IsStream:         true,
	}
	bag.Set(BagModel, model)
	bag.Set(BagProvider, provider)
	bag.Set(BagAPIKeyID, "ak1")
	bag.Set(BagUsage, usage)
	bag.Set(BagStarted, started)
	bag.Set(BagStatus, status)
	bag.Set(BagLogCtx, lc)

	// Run the full lifecycle: OnRequest (starts span + stores it) then OnLog.
	if out := runPhase(t, plugin.PhaseOnRequest, bag); out != plugin.OutcomeContinue {
		t.Fatalf("OnRequest: want OutcomeContinue, got %v", out)
	}
	if out := runPhase(t, plugin.PhaseOnLog, bag); out != plugin.OutcomeContinue {
		t.Fatalf("OnLog: want OutcomeContinue, got %v", out)
	}

	// --- metrics: collect and assert ---
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("reader.Collect: %v", err)
	}

	requestTotal, tokenIn, tokenOut := sumCounter(t, &rm, "nyro_requests_total"), int64(0), int64(0)
	// tokens are recorded as two Add()s on the same counter with distinct
	// "direction" attributes; sumCounter gives the grand total.
	tokenTotal := sumCounter(t, &rm, "nyro_tokens_total")

	if requestTotal != 1 {
		t.Errorf("nyro_requests_total: want 1, got %d", requestTotal)
	}
	wantTokens := int64(usage.PromptTokens + usage.CompletionTokens)
	if tokenTotal != wantTokens {
		t.Errorf("nyro_tokens_total: want %d, got %d", wantTokens, tokenTotal)
	}
	// silence unused (tokenIn/tokenOut kept for clarity of intent)
	_ = tokenIn
	_ = tokenOut

	// histogram: latency recorded once with the model/provider attributes.
	hist := findHistogram(t, &rm, "nyro_request_latency_ms")
	if hist.Count != 1 {
		t.Errorf("nyro_request_latency_ms count: want 1, got %d", hist.Count)
	}

	// --- span: OnLog must have ended exactly one span ("dispatch"). ---
	ended := spanRecorder.Ended()
	if len(ended) != 1 {
		t.Fatalf("ended spans: want 1, got %d (%v)", len(ended), ended)
	}
	if ended[0].Name() != "dispatch" {
		t.Errorf("span name: want dispatch, got %q", ended[0].Name())
	}

	// --- log: exactly one audit LogRecord emitted with the expected attrs. ---
	if len(cl.records) != 1 {
		t.Fatalf("emitted log records: want 1, got %d", len(cl.records))
	}
	rec := cl.records[0]
	attrMap := logRecordAttrs(t, &rec)
	assertLogAttr(t, attrMap, "nyro.model_name", "gpt-test")
	assertLogAttr(t, attrMap, "nyro.provider_name", "openai")
	assertLogAttr(t, attrMap, "nyro.client_model", "gpt-test")
	assertLogAttr(t, attrMap, "nyro.upstream_model", "gpt-4o")
	assertLogAttr(t, attrMap, "nyro.method", "POST")
	assertLogAttr(t, attrMap, "nyro.path", "/v1/chat/completions")
	assertLogAttr(t, attrMap, "nyro.api_key_id", "ak1")
	assertLogAttr(t, attrMap, "nyro.api_key_name", "test-key")
	assertLogAttr(t, attrMap, "nyro.is_stream", true)
	assertLogAttrInt64(t, attrMap, "nyro.input_tokens", 100)
	assertLogAttrInt64(t, attrMap, "nyro.output_tokens", 50)
	assertLogAttrInt64(t, attrMap, "nyro.cache_read_tokens", 7)
	// nyro.log.id must be a non-empty req_ identifier.
	if id := attrMap["nyro.log.id"].AsString(); id == "" || len(id) < 4 {
		t.Errorf("nyro.log.id: want non-empty, got %q", id)
	}
}

// TestHooksOnLogEmitsUpstreamAuditAttrs asserts the OnLog hook emits the two
// optional upstream audit attributes (nyro.upstream_status and
// nyro.latency_upstream_ms) when LogCtx carries non-nil, non-zero pointers —
// closing the data-loss gap vs the legacy audit (T3.2 review finding). Also
// verifies a nil-pointer LogCtx omits them (parquet optional column stays null).
func TestHooksOnLogEmitsUpstreamAuditAttrs(t *testing.T) {
	spanRecorder := tracetest.NewSpanRecorder()
	tracerProvider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spanRecorder))
	tracer := tracerProvider.Tracer("nyro-test")

	reader := sdkmetric.NewManualReader()
	meterProvider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	handles := NewHandles(meterProvider.Meter("nyro-test"))

	cl := &captureLogger{}
	RegisterHooks(newSwappableFromParts(tracer, cl, handles))
	t.Cleanup(func() {
		_ = tracerProvider.Shutdown(context.Background())
		_ = meterProvider.Shutdown(context.Background())
	})
	_ = reader // silence unused; metrics are not the focus of this test

	// --- case 1: non-nil upstream status + latency → both attributes emitted. ---
	upstreamStatus := int32(429)
	upstreamLatency := int64(312)
	lc := LogCtx{
		UpstreamStatus:    &upstreamStatus,
		LatencyUpstreamMs: &upstreamLatency,
	}
	bag := plugin.NewContextBag()
	bag.Set(BagModel, storage.Route{ID: "m", Model: "gpt-test"})
	bag.Set(BagProvider, storage.Upstream{ID: "p", Name: "openai"})
	bag.Set(BagAPIKeyID, "ak")
	bag.Set(BagUsage, ir.Usage{})
	bag.Set(BagStarted, time.Now().Add(-5*time.Millisecond))
	bag.Set(BagStatus, 200)
	bag.Set(BagLogCtx, lc)

	runPhase(t, plugin.PhaseOnRequest, bag)
	runPhase(t, plugin.PhaseOnLog, bag)

	if len(cl.records) != 1 {
		t.Fatalf("case 1 emitted log records: want 1, got %d", len(cl.records))
	}
	attrMap := logRecordAttrs(t, &cl.records[0])
	assertLogAttrInt64(t, attrMap, "nyro.upstream_status", 429)
	assertLogAttrInt64(t, attrMap, "nyro.latency_upstream_ms", 312)

	// --- case 2: nil pointers → attributes must be absent. ---
	cl.records = nil
	bag2 := plugin.NewContextBag()
	bag2.Set(BagModel, storage.Route{ID: "m", Model: "gpt-test"})
	bag2.Set(BagProvider, storage.Upstream{ID: "p", Name: "openai"})
	bag2.Set(BagAPIKeyID, "ak")
	bag2.Set(BagUsage, ir.Usage{})
	bag2.Set(BagStarted, time.Now().Add(-5*time.Millisecond))
	bag2.Set(BagStatus, 200)
	bag2.Set(BagLogCtx, LogCtx{}) // nil UpstreamStatus / LatencyUpstreamMs

	runPhase(t, plugin.PhaseOnRequest, bag2)
	runPhase(t, plugin.PhaseOnLog, bag2)

	if len(cl.records) != 1 {
		t.Fatalf("case 2 emitted log records: want 1, got %d", len(cl.records))
	}
	attrMap2 := logRecordAttrs(t, &cl.records[0])
	for _, key := range []string{"nyro.upstream_status", "nyro.latency_upstream_ms"} {
		if _, ok := attrMap2[key]; ok {
			t.Errorf("case 2: nil-pointer LogCtx must omit %q, but it was emitted", key)
		}
	}
}

func TestHooksOnLog5xxMarksSpanError(t *testing.T) {
	spanRecorder := tracetest.NewSpanRecorder()
	tracerProvider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spanRecorder))
	tracer := tracerProvider.Tracer("nyro-test")

	reader := sdkmetric.NewManualReader()
	meterProvider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	handles := NewHandles(meterProvider.Meter("nyro-test"))

	cl := &captureLogger{}
	RegisterHooks(newSwappableFromParts(tracer, cl, handles))
	t.Cleanup(func() {
		_ = tracerProvider.Shutdown(context.Background())
		_ = meterProvider.Shutdown(context.Background())
	})

	bag := plugin.NewContextBag()
	bag.Set(BagModel, storage.Route{ID: "m", Model: "gpt-test"})
	bag.Set(BagProvider, storage.Upstream{ID: "p", Name: "openai"})
	bag.Set(BagAPIKeyID, "ak")
	bag.Set(BagUsage, ir.Usage{})
	bag.Set(BagStarted, time.Now().Add(-10*time.Millisecond))
	bag.Set(BagStatus, 503)
	bag.Set(BagLogCtx, LogCtx{})

	runPhase(t, plugin.PhaseOnRequest, bag)
	runPhase(t, plugin.PhaseOnLog, bag)

	ended := spanRecorder.Ended()
	if len(ended) != 1 {
		t.Fatalf("ended spans: want 1, got %d", len(ended))
	}
	if st := ended[0].Status(); st.Code != codes.Error {
		t.Errorf("5xx span status code: want Error, got %d", st.Code)
	}
	// status_class label should be "5xx".
	var rm metricdata.ResourceMetrics
	_ = reader.Collect(context.Background(), &rm)
	if got := counterAttrClass(t, &rm, "nyro_requests_total"); got != "5xx" {
		t.Errorf("status_class: want 5xx, got %q", got)
	}
}

// --- metric helpers ---

// sumCounter sums every data point value across all scopes for the named int64
// counter. Returns 0 if not found.
func sumCounter(t *testing.T, rm *metricdata.ResourceMetrics, name string) int64 {
	t.Helper()
	var total int64
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			if s, ok := m.Data.(metricdata.Sum[int64]); ok {
				for _, dp := range s.DataPoints {
					total += dp.Value
				}
			}
		}
	}
	return total
}

// findHistogram returns the first Histogram data point aggregate for the named
// float64 histogram.
func findHistogram(t *testing.T, rm *metricdata.ResourceMetrics, name string) *metricdata.HistogramDataPoint[float64] {
	t.Helper()
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			if h, ok := m.Data.(metricdata.Histogram[float64]); ok && len(h.DataPoints) > 0 {
				return &h.DataPoints[0]
			}
		}
	}
	t.Fatalf("histogram %q not found", name)
	return nil
}

// counterAttrClass returns the value of the status_class attribute on the first
// data point of the named int64 counter (empty if absent).
func counterAttrClass(t *testing.T, rm *metricdata.ResourceMetrics, name string) string {
	t.Helper()
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			if s, ok := m.Data.(metricdata.Sum[int64]); ok && len(s.DataPoints) > 0 {
				val, _ := s.DataPoints[0].Attributes.Value(attribute.Key("status_class"))
				return val.AsString()
			}
		}
	}
	return ""
}

// --- log helpers ---

// logRecordAttrs walks a log.Record and collects its attributes into a map.
func logRecordAttrs(t *testing.T, rec *log.Record) map[string]log.Value {
	t.Helper()
	m := map[string]log.Value{}
	rec.WalkAttributes(func(kv log.KeyValue) bool {
		m[kv.Key] = kv.Value
		return true
	})
	return m
}

func assertLogAttr(t *testing.T, m map[string]log.Value, key string, want any) {
	t.Helper()
	v, ok := m[key]
	if !ok {
		t.Errorf("log attr %q missing", key)
		return
	}
	got := typedLogValue(v)
	if got != want {
		t.Errorf("log attr %q: want %v, got %v", key, want, got)
	}
}

func assertLogAttrInt64(t *testing.T, m map[string]log.Value, key string, want int64) {
	t.Helper()
	v, ok := m[key]
	if !ok {
		t.Errorf("log attr %q missing", key)
		return
	}
	n := v.AsInt64()
	if n != want {
		t.Errorf("log attr %q: want %d, got %v", key, want, v)
	}
}

// typedLogValue converts a log.Value to a plain Go value for == comparison.
func typedLogValue(v log.Value) any {
	switch v.Kind() {
	case log.KindString:
		return v.AsString()
	case log.KindInt64:
		return v.AsInt64()
	case log.KindBool:
		return v.AsBool()
	case log.KindFloat64:
		return v.AsFloat64()
	}
	return v
}

// Compile-time: ensure captureLogger satisfies log.Logger (and embeds noop for
// the unimplemented methods).
var _ log.Logger = (*captureLogger)(nil)

// keep imports referenced for the assert helpers above.
var _ attribute.KeyValue = attribute.KeyValue{}
