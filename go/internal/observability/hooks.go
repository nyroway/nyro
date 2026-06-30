package observability

import (
	"context"
	"time"

	"github.com/nyroway/nyro/go/internal/plugin"
	"github.com/nyroway/nyro/go/internal/protocol/ir"
	"github.com/nyroway/nyro/go/internal/storage"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// Bag key contract. Exported (plain strings) so the proxy dispatcher can set
// them when wiring T3.3. plugin.ContextBag uses string keys.
const (
	BagSpan     = "obs.span"
	BagSpanCtx  = "obs.ctx"
	BagModel    = "obs.model"
	BagProvider = "obs.provider"
	BagAPIKeyID = "obs.api_key_id"
	BagLogCtx   = "obs.logctx"
	BagUsage    = "obs.usage"
	BagStarted  = "obs.started"
	BagStatus   = "obs.status"
)

// onRequestHook starts the per-request "dispatch" span and stashes the span +
// span-context in the bag for the OnLog hook to finish.
type onRequestHook struct{ tracer trace.Tracer }

func (h onRequestHook) Name() string        { return "obs.on_request" }
func (h onRequestHook) Phase() plugin.Phase { return plugin.PhaseOnRequest }
func (h onRequestHook) Run(pctx *plugin.PhaseContext) plugin.PhaseOutcome {
	ctx, span := h.tracer.Start(pctx.Ctx, "dispatch")
	pctx.Bag.Set(BagSpanCtx, ctx)
	pctx.Bag.Set(BagSpan, span)
	return plugin.OutcomeContinue
}

// onLogHook is terminal: it reads the per-request state the dispatcher left in
// the bag, records the metrics, emits the structured audit LogRecord, and ends
// the span started by onRequestHook.
type onLogHook struct {
	logger  log.Logger
	handles *Handles
}

func (h onLogHook) Name() string        { return "obs.on_log" }
func (h onLogHook) Phase() plugin.Phase { return plugin.PhaseOnLog }
func (h onLogHook) Run(pctx *plugin.PhaseContext) plugin.PhaseOutcome {
	bag := pctx.Bag
	span, _ := pluginGet(bag, BagSpan).(trace.Span)
	spanCtx, _ := pluginGet(bag, BagSpanCtx).(context.Context)
	model, _ := pluginGet(bag, BagModel).(storage.Model)
	provider, _ := pluginGet(bag, BagProvider).(storage.Provider)
	lc, _ := pluginGet(bag, BagLogCtx).(LogCtx) // LogCtx moved into observability (Step 1)
	usage, _ := pluginGet(bag, BagUsage).(ir.Usage)
	started, _ := pluginGet(bag, BagStarted).(time.Time)
	status, _ := pluginGet(bag, BagStatus).(int)
	apiKeyID, _ := pluginGet(bag, BagAPIKeyID).(string)

	// If OnRequest never ran (bag has no span ctx), fall back to the phase
	// context so metric/log Emit still works rather than passing nil.
	emitCtx := spanCtx
	if emitCtx == nil {
		emitCtx = pctx.Ctx
	}

	statusClass := classify(status)
	latencyMs := time.Since(started).Milliseconds()

	// --- metrics (one request, in/out tokens, latency) ---
	// NOTE on OTel API: the metric instrument Add/Record methods take a
	// variadic ...AddOption / ...RecordOption, NOT raw attribute.KeyValue. We
	// wrap the attributes in metric.WithAttributes (which satisfies both option
	// types via the shared attrOpt).
	if h.handles != nil {
		reqAttrs := metric.WithAttributes(
			attribute.String("model", model.Name),
			attribute.String("provider", provider.Name),
			attribute.String("apikey", apiKeyID),
			attribute.String("status_class", statusClass),
		)
		h.handles.requests.Add(emitCtx, 1, reqAttrs)

		tokenIn := metric.WithAttributes(
			attribute.String("model", model.Name),
			attribute.String("apikey", apiKeyID),
			attribute.String("direction", "in"),
		)
		tokenOut := metric.WithAttributes(
			attribute.String("model", model.Name),
			attribute.String("apikey", apiKeyID),
			attribute.String("direction", "out"),
		)
		h.handles.tokens.Add(emitCtx, int64(usage.PromptTokens), tokenIn)
		h.handles.tokens.Add(emitCtx, int64(usage.CompletionTokens), tokenOut)

		h.handles.latency.Record(emitCtx, float64(latencyMs), metric.WithAttributes(
			attribute.String("model", model.Name),
			attribute.String("provider", provider.Name),
		))
	}

	// --- structured audit log record (replaces appendLog) ---
	// NOTE on OTel API: log.Record exposes AddAttributes (not SetAttributes in
	// this SDK version) and the attributes must be log.KeyValue (constructed via
	// log.String/log.Int64/log.Bool/...), not attribute.KeyValue. Attribute
	// keys match the admin receiver's logRecordFromOTLP contract (Task 2.1).
	var rec log.Record
	rec.SetTimestamp(time.Now())
	rec.AddAttributes(
		log.String("nyro.log.id", NewRequestID()),
		log.Int64("nyro.log.created_ms", started.UnixMilli()),
		log.String("nyro.client_protocol", lc.ClientProtocol),
		log.String("nyro.upstream_protocol", lc.UpstreamProtocol),
		log.String("nyro.provider_id", provider.ID),
		log.String("nyro.provider_name", provider.Name),
		log.String("nyro.model_id", model.ID),
		log.String("nyro.model_name", model.Name),
		log.String("nyro.client_model", lc.ClientModel),
		log.String("nyro.upstream_model", lc.UpstreamModel),
		log.String("nyro.method", lc.Method),
		log.String("nyro.path", lc.Path),
		log.String("nyro.api_key_id", apiKeyID),
		log.String("nyro.api_key_name", lc.APIKeyName),
		log.Int("nyro.client_status", status),
		log.Int64("nyro.latency_total_ms", latencyMs),
		log.Int64("nyro.input_tokens", int64(usage.PromptTokens)),
		log.Int64("nyro.output_tokens", int64(usage.CompletionTokens)),
		log.Int64("nyro.cache_read_tokens", cacheRead(usage)),
		log.Bool("nyro.is_stream", lc.IsStream),
	)
	// Optional upstream audit fields. Emitted only when the pointer is non-nil
	// AND non-zero — matching the admin receiver's lookupInt semantics (a 0
	// int is treated as absent, same as client_status/latency_total_ms). A nil
	// pointer (no upstream status/latency captured, e.g. a request that never
	// reached the upstream) omits the attribute entirely so the receiver leaves
	// the parquet optional column null.
	rec.AddAttributes(upstreamLogAttrs(lc)...)
	h.logger.Emit(emitCtx, rec)

	// --- finish the span (status for 5xx, then End) ---
	if span != nil {
		if status >= 500 {
			span.SetStatus(codes.Error, "upstream error")
		}
		span.SetAttributes(attribute.Int("nyro.client_status", status))
		span.End()
	}
	return plugin.OutcomeContinue
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

// RegisterHooks wires the OnRequest/OnLog phase hooks into the package-level
// plugin registry. It is NOT called from an init(): instrumentation stays inert
// until the dispatcher/cmd calls it (T3.3/T3.4), so importing observability has
// no process-wide side effects.
func RegisterHooks(tracer trace.Tracer, logger log.Logger, handles *Handles) {
	plugin.Register(onRequestHook{tracer: tracer})
	plugin.Register(onLogHook{logger: logger, handles: handles})
}

// pluginGet is a tiny helper so the type-assertion chain reads without repeating
// the two-value Get call. Returns nil if the key is absent.
func pluginGet(bag *plugin.ContextBag, key string) any {
	v, _ := bag.Get(key)
	return v
}
