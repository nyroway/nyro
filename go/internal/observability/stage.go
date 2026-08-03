package observability

import (
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/nyroway/nyro/go/internal/pipeline"
	"github.com/nyroway/nyro/go/internal/storage"
	"github.com/nyroway/nyro/go/llm/ir"
)

// Exchange keys for the state the dispatcher hands to this Stage. They live in
// Exchange.Ext rather than as Exchange fields because only telemetry reads
// them, and pipeline (layer 0) must not know about observability's types.
const (
	// ExtLogCtx holds the LogCtx the dispatcher fills in as protocol, model,
	// and upstream details become known.
	ExtLogCtx = "obs.logctx"
	// ExtRoute and ExtUpstream hold the resolved storage rows.
	ExtRoute    = "obs.route"
	ExtUpstream = "obs.upstream"
)

// Stage is the terminal telemetry Stage: it starts the per-request span on the
// way in and, on the way out, records metrics, emits the structured audit
// LogRecord, and ends the span.
//
// It belongs first in the chain so its outbound half runs last, after every
// other Stage has unwound. The emit is deferred inside Handle, which is what
// guarantees a request rejected by a later Stage — or one that never reached an
// upstream at all — is still reported, with zero values standing in for the
// fields the exchange never reached.
//
// The Stage holds no tracer or logger of its own: it reads the current bundle
// from a SwappableProvider on every request, so a config-sync hot reload can
// replace the pipeline underneath it without rebuilding the chain.
type Stage struct {
	target *atomic.Pointer[SwappableProvider]
}

// NewStage returns a telemetry Stage reading from target. Passing the pointer
// rather than the provider is deliberate: RegisterObservability re-points it on
// a later call, and the Stage picks that up without being rebuilt.
func NewStage(target *atomic.Pointer[SwappableProvider]) pipeline.Stage {
	return Stage{target: target}
}

func (s Stage) Name() string { return "observability" }

// Handle starts the span, runs the rest of the chain, and emits on the way out.
func (s Stage) Handle(ex *pipeline.Exchange, next func() error) error {
	sp := s.target.Load()
	if sp == nil {
		return next() // observability not wired: stay inert
	}
	active := sp.load()

	ctx, span := active.tracer.Start(ex.Ctx, "dispatch")
	ex.Ctx = ctx

	// Deferred so the emit happens however the chain unwinds — a normal
	// response, a short circuit from a later Stage, or an error.
	defer s.emit(ex, active, span)

	return next()
}

// OnDelta accumulates streaming usage. The dispatcher also tracks usage for
// the response it writes; this keeps the telemetry view correct for exchanges
// that end mid-stream.
func (s Stage) OnDelta(ex *pipeline.Exchange, d ir.StreamDelta) {
	if u, ok := d.(*ir.UsageDelta); ok {
		ex.Usage = u.Usage
	}
}

// emit records metrics, writes the audit LogRecord, and ends the span. It runs
// exactly once per request, from Handle's defer.
func (s Stage) emit(ex *pipeline.Exchange, active *activeSet, span trace.Span) {
	lc, _ := ex.GetExt(ExtLogCtx).(LogCtx)
	route, _ := ex.GetExt(ExtRoute).(storage.Route)
	upstream, _ := ex.GetExt(ExtUpstream).(storage.Upstream)

	statusClass := classify(ex.Status)
	latencyMs := time.Since(ex.Started).Milliseconds()

	// --- metrics (one request, in/out tokens, latency) ---
	// NOTE on OTel API: the metric instrument Add/Record methods take a
	// variadic ...AddOption / ...RecordOption, NOT raw attribute.KeyValue. We
	// wrap the attributes in metric.WithAttributes (which satisfies both option
	// types via the shared attrOpt).
	if active.handles != nil {
		reqAttrs := metric.WithAttributes(
			attribute.String("model", route.Model),
			attribute.String("provider", upstream.Name),
			attribute.String("apikey", ex.ConsumerID),
			attribute.String("status_class", statusClass),
		)
		active.handles.requests.Add(ex.Ctx, 1, reqAttrs)

		tokenIn := metric.WithAttributes(
			attribute.String("model", route.Model),
			attribute.String("apikey", ex.ConsumerID),
			attribute.String("direction", "in"),
		)
		tokenOut := metric.WithAttributes(
			attribute.String("model", route.Model),
			attribute.String("apikey", ex.ConsumerID),
			attribute.String("direction", "out"),
		)
		active.handles.tokens.Add(ex.Ctx, int64(ex.Usage.PromptTokens), tokenIn)
		active.handles.tokens.Add(ex.Ctx, int64(ex.Usage.CompletionTokens), tokenOut)

		active.handles.latency.Record(ex.Ctx, float64(latencyMs), metric.WithAttributes(
			attribute.String("model", route.Model),
			attribute.String("provider", upstream.Name),
		))
	}

	// --- structured audit log record ---
	// NOTE on OTel API: log.Record exposes AddAttributes (not SetAttributes in
	// this SDK version) and the attributes must be log.KeyValue (constructed via
	// log.String/log.Int64/log.Bool/...), not attribute.KeyValue. Attribute
	// keys match the admin receiver's logRecordFromOTLP contract.
	var rec log.Record
	rec.SetTimestamp(time.Now())
	rec.AddAttributes(
		log.String("nyro.log.id", NewRequestID()),
		log.Int64("nyro.log.created_ms", ex.Started.UnixMilli()),
		log.String("nyro.client_protocol", lc.ClientProtocol),
		log.String("nyro.upstream_protocol", lc.UpstreamProtocol),
		log.String("nyro.provider_id", upstream.ID),
		log.String("nyro.provider_name", upstream.Name),
		log.String("nyro.model_id", route.ID),
		log.String("nyro.model_name", route.Model),
		log.String("nyro.client_model", lc.ClientModel),
		log.String("nyro.upstream_model", lc.UpstreamModel),
		log.String("nyro.method", lc.Method),
		log.String("nyro.path", lc.Path),
		log.String("nyro.api_key_id", ex.ConsumerID),
		log.String("nyro.api_key_name", lc.APIKeyName),
		log.String("nyro.api_key_preview", lc.APIKeyPreview),
		log.Int("nyro.client_status", ex.Status),
		log.Int64("nyro.latency_total_ms", latencyMs),
		log.Int64("nyro.input_tokens", int64(ex.Usage.PromptTokens)),
		log.Int64("nyro.output_tokens", int64(ex.Usage.CompletionTokens)),
		log.Int64("nyro.cache_read_tokens", cacheRead(ex.Usage)),
		log.Bool("nyro.is_stream", lc.IsStream),
	)
	rec.AddAttributes(upstreamLogAttrs(lc)...)
	active.logger.Emit(ex.Ctx, rec)

	// --- finish the span (status for 5xx, then End) ---
	if span != nil {
		if ex.Status >= 500 {
			span.SetStatus(codes.Error, "upstream error")
		}
		span.SetAttributes(attribute.Int("nyro.client_status", ex.Status))
		span.End()
	}
}

// cacheRead returns the cache-read token count, treating a nil pointer as 0.
func cacheRead(u ir.Usage) int64 {
	if u.CacheReadTokens != nil {
		return int64(*u.CacheReadTokens)
	}
	return 0
}

// upstreamLogAttrs builds the optional upstream audit attributes for the audit
// LogRecord. nyro.upstream_status and nyro.latency_upstream_ms are emitted only
// when their LogCtx pointer is non-nil AND the dereferenced value is non-zero —
// matching the admin receiver's lookupInt contract (a zero int is treated as
// absent). A nil pointer (no upstream status/latency captured) yields no
// attribute, so the receiver leaves the parquet optional column null rather
// than writing a spurious 0.
func upstreamLogAttrs(lc LogCtx) []log.KeyValue {
	var attrs []log.KeyValue
	if lc.UpstreamStatus != nil && *lc.UpstreamStatus != 0 {
		attrs = append(attrs, log.Int64("nyro.upstream_status", int64(*lc.UpstreamStatus)))
	}
	if lc.LatencyUpstreamMs != nil && *lc.LatencyUpstreamMs != 0 {
		attrs = append(attrs, log.Int64("nyro.latency_upstream_ms", *lc.LatencyUpstreamMs))
	}
	return attrs
}

// classify maps an HTTP status to its 2xx/4xx/5xx class label.
func classify(status int) string {
	switch {
	case status >= 500:
		return "5xx"
	case status >= 400:
		return "4xx"
	default:
		return "2xx"
	}
}

// stageTarget is the SwappableProvider the telemetry Stage reads on every
// request. RegisterObservability re-points it; the Stage loads (never captures)
// it, which is what makes repeated registration safe.
//
// A nil value means nothing has been registered yet; the Stage then no-ops so a
// request served before (or without) observability wiring still succeeds.
var stageTarget atomic.Pointer[SwappableProvider]

// RegisterObservability points the telemetry Stage at sp.
//
// Calling it more than once per process is safe and idempotent: it simply
// re-points the target. This matters because a single process can assemble more
// than one data plane over its lifetime — `nyro serve` embeds one alongside
// the control plane, and tests build many.
//
// This is distinct from hot-reload: an obs config change swaps the pipeline
// inside sp (SwappableProvider.Swap) without touching the target at all.
// RegisterObservability is for replacing sp itself.
func RegisterObservability(sp *SwappableProvider) {
	stageTarget.Store(sp)
}

// NewRegisteredStage returns the telemetry Stage bound to the process-wide
// target that RegisterObservability points. Building a chain before
// registration is fine: the Stage stays inert until a provider shows up.
func NewRegisteredStage() pipeline.Stage {
	return NewStage(&stageTarget)
}
