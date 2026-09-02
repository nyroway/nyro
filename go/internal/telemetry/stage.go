package telemetry

import (
	"context"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/nyroway/nyro/go/internal/llm"
	"github.com/nyroway/nyro/go/internal/llm/pipeline"
)

// Phase observes the start of a request and finalizes its metrics, structured
// audit record, and span after the other phases have completed.
//
// It occupies the mandatory Observe position, so its Finalizer runs last. A
// request rejected by a later phase, or one that never reaches an upstream, is
// still reported with zero values for state it never reached.
//
// Phase holds no tracer or logger of its own: it reads the current bundle
// from a SwappableProvider on every request, so a config-sync hot reload can
// replace the pipeline underneath it without rebuilding the chain.
type Phase struct {
	target *atomic.Pointer[SwappableProvider]
}

// NewPhase returns a telemetry Phase reading from target. Passing the pointer
// rather than the provider is deliberate: RegisterProvider re-points it on
// a later call, and the Phase picks that up without being rebuilt.
func NewPhase(target *atomic.Pointer[SwappableProvider]) pipeline.Phase {
	return Phase{target: target}
}

func (s Phase) Name() string { return "observe" }

// Apply starts the span and returns a Finalizer that emits after all request
// phases have completed.
func (s Phase) Apply(ctx context.Context, ex *pipeline.Exchange) (pipeline.Outcome, pipeline.Finalizer) {
	sp := s.target.Load()
	if sp == nil {
		return pipeline.Outcome{Decision: pipeline.Continue}, nil
	}
	active := sp.load()

	spanCtx, span := active.tracer.Start(ctx, "dispatch")
	return pipeline.Outcome{Decision: pipeline.Continue}, func(context.Context, *pipeline.Exchange, pipeline.Completion) error {
		s.emit(spanCtx, ex, active, span)
		return nil
	}
}

// OnDelta accumulates streaming usage. The dispatcher also tracks usage for
// the response it writes; this keeps the telemetry view correct for exchanges
// that end mid-stream.
func (s Phase) OnDelta(_ context.Context, ex *pipeline.Exchange, d llm.StreamDelta) {
	if u, ok := d.(*llm.UsageDelta); ok {
		ex.Usage = u.Usage
	}
}

// emit records metrics, writes the audit LogRecord, and ends the span.
func (s Phase) emit(ctx context.Context, ex *pipeline.Exchange, active *activeSet, span trace.Span) {
	latencyMs := time.Since(ex.Started).Milliseconds()

	// --- metrics (one request, in/out tokens, latency) ---
	// NOTE on OTel API: the metric instrument Add/Record methods take a
	// variadic ...AddOption / ...RecordOption, NOT raw attribute.KeyValue. We
	// wrap the attributes in metric.WithAttributes (which satisfies both option
	// types via the shared attrOpt).
	if active.handles != nil {
		reqAttrs := metric.WithAttributes(
			attribute.String("nyro.route.id", ex.Route.ID),
			attribute.String("nyro.route.model", ex.Route.Model),
			attribute.String("nyro.upstream.id", ex.Target.UpstreamID),
			attribute.String("nyro.upstream.name", ex.Target.UpstreamName),
			attribute.String("nyro.consumer.id", ex.Identity.Subject),
			attribute.Int("http.response.status_code", ex.Status),
		)
		active.handles.requests.Add(ctx, 1, reqAttrs)

		tokenIn := metric.WithAttributes(
			attribute.String("nyro.route.id", ex.Route.ID),
			attribute.String("nyro.route.model", ex.Route.Model),
			attribute.String("nyro.consumer.id", ex.Identity.Subject),
			attribute.String("direction", "in"),
		)
		tokenOut := metric.WithAttributes(
			attribute.String("nyro.route.id", ex.Route.ID),
			attribute.String("nyro.route.model", ex.Route.Model),
			attribute.String("nyro.consumer.id", ex.Identity.Subject),
			attribute.String("direction", "out"),
		)
		active.handles.tokens.Add(ctx, int64(ex.Usage.PromptTokens), tokenIn)
		active.handles.tokens.Add(ctx, int64(ex.Usage.CompletionTokens), tokenOut)

		active.handles.latency.Record(ctx, float64(latencyMs), metric.WithAttributes(
			attribute.String("nyro.route.id", ex.Route.ID),
			attribute.String("nyro.route.model", ex.Route.Model),
			attribute.String("nyro.upstream.id", ex.Target.UpstreamID),
			attribute.String("nyro.upstream.name", ex.Target.UpstreamName),
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
		log.String("nyro.client_protocol", string(ex.Source.Protocol)),
		log.String("nyro.upstream_protocol", string(ex.Target.Endpoint.Protocol)),
		log.String("nyro.upstream.id", ex.Target.UpstreamID),
		log.String("nyro.upstream.name", ex.Target.UpstreamName),
		log.String("nyro.route.id", ex.Route.ID),
		log.String("nyro.route.model", ex.Route.Model),
		log.String("nyro.client_model", ex.RequestInfo.ClientModel),
		log.String("nyro.upstream_model", ex.Target.Model),
		log.String("nyro.method", ex.RequestInfo.Operation),
		log.String("nyro.path", ex.RequestInfo.Resource),
		log.String("nyro.consumer.id", ex.Identity.Subject),
		log.String("nyro.consumer_key.name", ex.Identity.CredentialName),
		log.String("nyro.consumer_key.preview", ex.Identity.CredentialPreview),
		log.Int("http.response.status_code", ex.Status),
		log.Int64("nyro.latency_total_ms", latencyMs),
		log.Int64("nyro.input_tokens", int64(ex.Usage.PromptTokens)),
		log.Int64("nyro.output_tokens", int64(ex.Usage.CompletionTokens)),
		log.Int64("nyro.cache_read_tokens", cacheRead(ex.Usage)),
		log.Bool("nyro.is_stream", ex.Streamed),
	)
	rec.AddAttributes(upstreamLogAttrs(ex.Target)...)
	active.logger.Emit(ctx, rec)

	// --- finish the span (status for 5xx, then End) ---
	if span != nil {
		if ex.Status >= 500 {
			span.SetStatus(codes.Error, "upstream error")
		}
		span.SetAttributes(attribute.Int("http.response.status_code", ex.Status))
		span.End()
	}
}

// cacheRead returns the cache-read token count, treating a nil pointer as 0.
func cacheRead(u llm.Usage) int64 {
	if u.CacheReadTokens != nil {
		return int64(*u.CacheReadTokens)
	}
	return 0
}

// upstreamLogAttrs builds optional upstream audit attributes. Nil or zero
// target values are omitted.
func upstreamLogAttrs(target pipeline.Target) []log.KeyValue {
	var attrs []log.KeyValue
	if target.UpstreamStatus != nil && *target.UpstreamStatus != 0 {
		attrs = append(attrs, log.Int64("nyro.upstream.status_code", int64(*target.UpstreamStatus)))
	}
	if target.UpstreamLatencyMs != nil && *target.UpstreamLatencyMs != 0 {
		attrs = append(attrs, log.Int64("nyro.latency_upstream_ms", *target.UpstreamLatencyMs))
	}
	return attrs
}

// stageTarget is the SwappableProvider the telemetry Phase reads on every
// request. RegisterProvider re-points it; the Phase loads (never captures)
// it, which is what makes repeated registration safe.
//
// A nil value means nothing has been registered yet; the Phase then no-ops so a
// request served before (or without) observability wiring still succeeds.
var stageTarget atomic.Pointer[SwappableProvider]

// RegisterProvider points the telemetry Phase at sp.
//
// Calling it more than once per process is safe and idempotent: it simply
// re-points the target. This matters because a single process can assemble more
// than one data plane over its lifetime — `nyro serve` embeds one alongside
// the control plane, and tests build many.
//
// This is distinct from hot-reload: an obs config change swaps the pipeline
// inside sp (SwappableProvider.Swap) without touching the target at all.
// RegisterProvider is for replacing sp itself.
func RegisterProvider(sp *SwappableProvider) {
	stageTarget.Store(sp)
}

// NewRegisteredPhase returns the telemetry Phase bound to the process-wide
// target that RegisterProvider points. Building a Runner before
// registration is fine: the Phase stays inert until a provider shows up.
func NewRegisteredPhase() pipeline.Phase {
	return NewPhase(&stageTarget)
}
