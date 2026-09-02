package telemetry

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

	configsnapshot "github.com/nyroway/nyro/go/internal/config/snapshot"
	"github.com/nyroway/nyro/go/internal/llm"
	"github.com/nyroway/nyro/go/internal/llm/pipeline"
	"github.com/nyroway/nyro/go/internal/llm/protocol"
	"github.com/nyroway/nyro/go/internal/security/authn"
)

// captureLogger is a minimal log.Logger wrapper that records every emitted
// log.Record so the test can assert the audit attributes. It embeds the noop
// logger to satisfy the rest of the log.Logger interface.
type captureLogger struct {
	noop.Logger
	records []log.Record
}

type testLogContext struct {
	ConsumerKeyName    string
	ConsumerKeyPreview string
	ClientProtocol     string
	UpstreamProtocol   string
	ClientModel        string
	UpstreamModel      string
	Method             string
	Path               string
	IsStream           bool
	UpstreamStatus     *int32
	LatencyUpstreamMs  *int64
}

func (c *captureLogger) Emit(ctx context.Context, r log.Record) {
	c.records = append(c.records, r.Clone())
}

// runStage drives one exchange through the telemetry phase and its finalizer.
func runStage(t *testing.T, ex *pipeline.Exchange) {
	t.Helper()
	stage := NewRegisteredPhase()
	outcome, finalizer := stage.Apply(context.Background(), ex)
	if outcome.Decision != pipeline.Continue {
		t.Fatalf("Phase.Apply decision = %v, want Continue", outcome.Decision)
	}
	if finalizer == nil {
		t.Fatal("Phase.Apply returned a nil finalizer")
	}
	if err := finalizer(context.Background(), ex, pipeline.Completion{
		Response: ex.Response,
		Error:    ex.Error,
		Usage:    ex.Usage,
		Streamed: ex.Streamed,
	}); err != nil {
		t.Fatalf("telemetry finalizer returned %v, want nil", err)
	}
}

// newExchange builds an Exchange carrying the state the dispatcher publishes.
func newExchange(route configsnapshot.Route, upstream configsnapshot.Upstream, consumerID string, usage llm.Usage, started time.Time, status int, lc testLogContext) *pipeline.Exchange {
	currentModel := lc.ClientModel
	if lc.UpstreamModel != "" {
		currentModel = lc.UpstreamModel
	}
	return &pipeline.Exchange{
		Request: llm.NewChatRequest(currentModel, nil),
		Source: protocol.Endpoint{
			Protocol: protocol.Protocol(lc.ClientProtocol),
		},
		Identity: authn.Identity{
			Subject:           consumerID,
			CredentialName:    lc.ConsumerKeyName,
			CredentialPreview: lc.ConsumerKeyPreview,
		},
		Route: pipeline.LogicalRoute{ID: route.ID, Model: route.Model},
		Target: pipeline.Target{
			UpstreamID:        upstream.ID,
			UpstreamName:      upstream.Name,
			Model:             lc.UpstreamModel,
			Endpoint:          protocol.Endpoint{Protocol: protocol.Protocol(lc.UpstreamProtocol)},
			UpstreamStatus:    lc.UpstreamStatus,
			UpstreamLatencyMs: lc.LatencyUpstreamMs,
		},
		RequestInfo: pipeline.RequestInfo{
			ClientModel: lc.ClientModel,
			Operation:   lc.Method,
			Resource:    lc.Path,
		},
		Usage:    usage,
		Status:   status,
		Started:  started,
		Streamed: lc.IsStream,
	}
}

func TestPhaseStartsAndFinalizesSpan(t *testing.T) {
	// Isolated harness: a fresh span recorder + tracer, fresh handles, fresh
	// logger capture. The target is process-wide — but we assert only what
	// THIS invocation produced.
	spanRecorder := tracetest.NewSpanRecorder()
	tracerProvider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spanRecorder))
	tracer := tracerProvider.Tracer("nyro-test")

	reader := sdkmetric.NewManualReader()
	meterProvider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	handles := NewHandles(meterProvider.Meter("nyro-test"))

	cl := &captureLogger{}
	RegisterProvider(newSwappableFromParts(tracer, cl, handles))
	t.Cleanup(func() {
		_ = tracerProvider.Shutdown(context.Background())
		_ = meterProvider.Shutdown(context.Background())
	})

	ex := &pipeline.Exchange{Started: time.Now()}
	stage := NewRegisteredPhase()
	outcome, finalizer := stage.Apply(context.Background(), ex)
	if outcome.Decision != pipeline.Continue || finalizer == nil {
		t.Fatalf("Apply = (%v, %v), want Continue and a finalizer", outcome, finalizer)
	}
	if got := len(spanRecorder.Started()); got != 1 {
		t.Fatalf("started spans = %d, want 1", got)
	}
	if got := len(spanRecorder.Ended()); got != 0 {
		t.Fatalf("ended spans before finalization = %d, want 0", got)
	}
	if err := finalizer(context.Background(), ex, pipeline.Completion{}); err != nil {
		t.Fatalf("finalizer: %v", err)
	}
	if got := len(spanRecorder.Ended()); got != 1 {
		t.Errorf("ended spans = %d, want 1", got)
	}
}

func TestStageRecordsMetricsTokensAndSpan(t *testing.T) {
	spanRecorder := tracetest.NewSpanRecorder()
	tracerProvider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spanRecorder))
	tracer := tracerProvider.Tracer("nyro-test")

	reader := sdkmetric.NewManualReader()
	meterProvider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	handles := NewHandles(meterProvider.Meter("nyro-test"))

	cl := &captureLogger{}
	RegisterProvider(newSwappableFromParts(tracer, cl, handles))
	t.Cleanup(func() {
		_ = tracerProvider.Shutdown(context.Background())
		_ = meterProvider.Shutdown(context.Background())
	})

	// Seed the exchange with the request state the dispatcher publishes.
	model := configsnapshot.Route{ID: "m1", Model: "gpt-test"}
	provider := configsnapshot.Upstream{ID: "p1", Name: "openai"}
	cacheRead := uint32(7)
	usage := llm.Usage{PromptTokens: 100, CompletionTokens: 50, CacheReadTokens: &cacheRead}
	started := time.Now().Add(-25 * time.Millisecond) // pretend 25ms elapsed upstream
	lc := testLogContext{
		ClientProtocol:   "openai",
		UpstreamProtocol: "openai",
		ClientModel:      "gpt-test",
		UpstreamModel:    "gpt-4o",
		Method:           "POST",
		Path:             "/v1/chat/completions",
		ConsumerKeyName:  "test-key",
		IsStream:         true,
	}
	runStage(t, newExchange(model, provider, "ak1", usage, started, 200, lc))

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
	assertLogAttr(t, attrMap, "nyro.route.id", "m1")
	assertLogAttr(t, attrMap, "nyro.route.model", "gpt-test")
	assertLogAttr(t, attrMap, "nyro.upstream.id", "p1")
	assertLogAttr(t, attrMap, "nyro.upstream.name", "openai")
	assertLogAttr(t, attrMap, "nyro.client_model", "gpt-test")
	assertLogAttr(t, attrMap, "nyro.upstream_model", "gpt-4o")
	assertLogAttr(t, attrMap, "nyro.method", "POST")
	assertLogAttr(t, attrMap, "nyro.path", "/v1/chat/completions")
	assertLogAttr(t, attrMap, "nyro.consumer.id", "ak1")
	assertLogAttr(t, attrMap, "nyro.consumer_key.name", "test-key")
	assertLogAttrInt64(t, attrMap, "http.response.status_code", 200)
	assertLogAttr(t, attrMap, "nyro.is_stream", true)
	assertLogAttrInt64(t, attrMap, "nyro.input_tokens", 100)
	assertLogAttrInt64(t, attrMap, "nyro.output_tokens", 50)
	assertLogAttrInt64(t, attrMap, "nyro.cache_read_tokens", 7)
	// nyro.log.id must be a non-empty req_ identifier.
	if id := attrMap["nyro.log.id"].AsString(); id == "" || len(id) < 4 {
		t.Errorf("nyro.log.id: want non-empty, got %q", id)
	}
	for _, oldKey := range []string{"nyro.provider_id", "nyro.model_name", "nyro.api_key_id", "nyro.client_status"} {
		if _, ok := attrMap[oldKey]; ok {
			t.Errorf("legacy log attr %q must not be emitted", oldKey)
		}
	}
}

// TestStageEmitsUpstreamAuditAttrs asserts the Phase emits the two optional
// upstream audit attributes when target state carries non-nil, non-zero
// pointers, and omits them otherwise.
func TestStageEmitsUpstreamAuditAttrs(t *testing.T) {
	spanRecorder := tracetest.NewSpanRecorder()
	tracerProvider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spanRecorder))
	tracer := tracerProvider.Tracer("nyro-test")

	reader := sdkmetric.NewManualReader()
	meterProvider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	handles := NewHandles(meterProvider.Meter("nyro-test"))

	cl := &captureLogger{}
	RegisterProvider(newSwappableFromParts(tracer, cl, handles))
	t.Cleanup(func() {
		_ = tracerProvider.Shutdown(context.Background())
		_ = meterProvider.Shutdown(context.Background())
	})
	_ = reader // silence unused; metrics are not the focus of this test

	route := configsnapshot.Route{ID: "m", Model: "gpt-test"}
	upstream := configsnapshot.Upstream{ID: "p", Name: "openai"}

	// --- case 1: non-nil upstream status + latency → both attributes emitted. ---
	upstreamStatus := int32(429)
	upstreamLatency := int64(312)
	lc := testLogContext{
		UpstreamStatus:    &upstreamStatus,
		LatencyUpstreamMs: &upstreamLatency,
	}
	runStage(t, newExchange(route, upstream, "ak", llm.Usage{}, time.Now().Add(-5*time.Millisecond), 200, lc))

	if len(cl.records) != 1 {
		t.Fatalf("case 1 emitted log records: want 1, got %d", len(cl.records))
	}
	attrMap := logRecordAttrs(t, &cl.records[0])
	assertLogAttrInt64(t, attrMap, "nyro.upstream.status_code", 429)
	assertLogAttrInt64(t, attrMap, "nyro.latency_upstream_ms", 312)

	// --- case 2: nil pointers → attributes must be absent. ---
	cl.records = nil
	runStage(t, newExchange(route, upstream, "ak", llm.Usage{}, time.Now().Add(-5*time.Millisecond), 200, testLogContext{}))

	if len(cl.records) != 1 {
		t.Fatalf("case 2 emitted log records: want 1, got %d", len(cl.records))
	}
	attrMap2 := logRecordAttrs(t, &cl.records[0])
	for _, key := range []string{"nyro.upstream.status_code", "nyro.latency_upstream_ms"} {
		if _, ok := attrMap2[key]; ok {
			t.Errorf("case 2: nil target value must omit %q, but it was emitted", key)
		}
	}
}

func TestStage5xxMarksSpanError(t *testing.T) {
	spanRecorder := tracetest.NewSpanRecorder()
	tracerProvider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spanRecorder))
	tracer := tracerProvider.Tracer("nyro-test")

	reader := sdkmetric.NewManualReader()
	meterProvider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	handles := NewHandles(meterProvider.Meter("nyro-test"))

	cl := &captureLogger{}
	RegisterProvider(newSwappableFromParts(tracer, cl, handles))
	t.Cleanup(func() {
		_ = tracerProvider.Shutdown(context.Background())
		_ = meterProvider.Shutdown(context.Background())
	})

	runStage(t, newExchange(
		configsnapshot.Route{ID: "m", Model: "gpt-test"},
		configsnapshot.Upstream{ID: "p", Name: "openai"},
		"ak", llm.Usage{}, time.Now().Add(-10*time.Millisecond), 503, testLogContext{},
	))

	ended := spanRecorder.Ended()
	if len(ended) != 1 {
		t.Fatalf("ended spans: want 1, got %d", len(ended))
	}
	if st := ended[0].Status(); st.Code != codes.Error {
		t.Errorf("5xx span status code: want Error, got %d", st.Code)
	}
	// The request metric uses the standard exact HTTP response code.
	var rm metricdata.ResourceMetrics
	_ = reader.Collect(context.Background(), &rm)
	if got := counterAttrInt64(t, &rm, "nyro_requests_total", "http.response.status_code"); got != 503 {
		t.Errorf("http.response.status_code: want 503, got %d", got)
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

// counterAttrInt64 returns one integer attribute from the first data point of
// the named int64 counter.
func counterAttrInt64(t *testing.T, rm *metricdata.ResourceMetrics, name, key string) int64 {
	t.Helper()
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			if s, ok := m.Data.(metricdata.Sum[int64]); ok && len(s.DataPoints) > 0 {
				val, _ := s.DataPoints[0].Attributes.Value(attribute.Key(key))
				return val.AsInt64()
			}
		}
	}
	return 0
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

// TestRegisterProviderIsIdempotentAndRepoints pins the contract that
// makes an embedded data plane safe: a process may register more than once
// (`nyro serve` assembles a data plane alongside the control plane, tests
// assemble many), and doing so must neither double-emit nor keep an old
// provider alive.
//
// Under the phase model this was a real hazard — the registry appended, so a
// second registration meant every request produced two spans and two log
// records. The Phase holds a single re-pointable target instead, which makes
// double-emission structurally impossible; the test stays because the
// re-pointing half of the contract is still load-bearing.
func TestRegisterProviderIsIdempotentAndRepoints(t *testing.T) {
	newRecorder := func() (*tracetest.SpanRecorder, trace.Tracer, func()) {
		rec := tracetest.NewSpanRecorder()
		tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
		return rec, tp.Tracer("nyro-test"), func() { _ = tp.Shutdown(context.Background()) }
	}

	reader := sdkmetric.NewManualReader()
	meterProvider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	handles := NewHandles(meterProvider.Meter("nyro-test"))
	t.Cleanup(func() { _ = meterProvider.Shutdown(context.Background()) })

	firstRec, firstTracer, stopFirst := newRecorder()
	t.Cleanup(stopFirst)
	secondRec, secondTracer, stopSecond := newRecorder()
	t.Cleanup(stopSecond)

	RegisterProvider(newSwappableFromParts(firstTracer, &captureLogger{}, handles))
	RegisterProvider(newSwappableFromParts(secondTracer, &captureLogger{}, handles))

	runStage(t, &pipeline.Exchange{Started: time.Now()})

	// Exactly one span, and it belongs to the most recently registered
	// provider: registering twice must not leave two live targets.
	if got := len(secondRec.Started()); got != 1 {
		t.Errorf("spans started on the current provider = %d; want 1", got)
	}
	if got := len(firstRec.Started()); got != 0 {
		t.Errorf("spans started on the displaced provider = %d; want 0 (it should no longer be wired)", got)
	}
}

// TestStageEmitsOnShortCircuit verifies telemetry finalization for a request
// rejected before Dispatch.
func TestStageEmitsOnShortCircuit(t *testing.T) {
	spanRecorder := tracetest.NewSpanRecorder()
	tracerProvider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spanRecorder))
	tracer := tracerProvider.Tracer("nyro-test")

	reader := sdkmetric.NewManualReader()
	meterProvider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	handles := NewHandles(meterProvider.Meter("nyro-test"))

	cl := &captureLogger{}
	RegisterProvider(newSwappableFromParts(tracer, cl, handles))
	t.Cleanup(func() {
		_ = tracerProvider.Shutdown(context.Background())
		_ = meterProvider.Shutdown(context.Background())
	})

	// A rejected request: no route, no upstream, no usage — only a status.
	ex := &pipeline.Exchange{Started: time.Now(), Status: 401}
	stage := NewRegisteredPhase()
	outcome, finalizer := stage.Apply(context.Background(), ex)
	if outcome.Decision != pipeline.Continue || finalizer == nil {
		t.Fatalf("Apply = (%v, %v), want Continue and a finalizer", outcome, finalizer)
	}
	if err := finalizer(context.Background(), ex, pipeline.Completion{
		Error: llm.NewError(llm.ErrAuthenticationError, "missing API key"),
	}); err != nil {
		t.Fatalf("finalizer: %v", err)
	}

	if len(cl.records) != 1 {
		t.Fatalf("emitted log records = %d, want 1 — a rejected request must still be audited", len(cl.records))
	}
	attrs := logRecordAttrs(t, &cl.records[0])
	assertLogAttrInt64(t, attrs, "http.response.status_code", 401)
	// Fields the exchange never reached come through as zero values.
	assertLogAttr(t, attrs, "nyro.route.model", "")
	assertLogAttrInt64(t, attrs, "nyro.input_tokens", 0)

	if got := len(spanRecorder.Ended()); got != 1 {
		t.Errorf("ended spans = %d, want 1", got)
	}
}
