package proxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/nyroway/nyro/go/internal/observability"
	"github.com/nyroway/nyro/go/internal/plugin"
	"github.com/nyroway/nyro/go/internal/protocol/codec"
	"github.com/nyroway/nyro/go/internal/protocol/ids"
	"github.com/nyroway/nyro/go/internal/protocol/ir"
	"github.com/nyroway/nyro/go/internal/router"
	"github.com/nyroway/nyro/go/internal/storage"
	"github.com/nyroway/nyro/go/internal/vendor"
)

// Dispatch is the single orchestration entry point. The ingress shell
// (handleProxy) has already decoded the wire body into IR; Dispatch runs the
// lifecycle phases, resolves the model→backend→provider from the in-memory
// cache, forwards to the upstream (Native path, ingress codec for egress), and
// converts the response back. Ported from dispatch_pipeline.
//
// Telemetry (audit log record + metrics + dispatch span) is emitted by the
// OnLog phase hook, which reads per-request state from a ContextBag populated
// here as the data becomes known. The hook is registered once at process start
// (cmd/gateway); in tests no hooks are registered so Dispatch is telemetry-free
// but otherwise functional.
func (g *Gateway) Dispatch(w http.ResponseWriter, r *http.Request, req *ir.AiRequest, ingress codec.EndpointHandler) {
	started := time.Now()
	rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
	var apiKeyID string
	var usage ir.Usage
	model := storage.Model{}
	provider := storage.Provider{}
	lc := observability.LogCtx{
		Method:         r.Method,
		Path:           r.URL.Path,
		ClientProtocol: ingress.Endpoint().String(),
		ClientModel:    req.Model,
		IsStream:       req.Stream.Enabled,
	}
	bag := plugin.NewContextBag()

	// Defer ordering is LIFO: the bag-populating defer (registered second)
	// runs FIRST, then the OnLog hook defer (registered first) reads the
	// fully-populated bag and emits. This guarantees the hook sees usage,
	// status, provider, etc. even when the request exits early.
	defer plugin.RunPhaseHooks(plugin.PhaseOnLog, &plugin.PhaseContext{Ctx: r.Context(), Bag: bag})
	defer func() {
		bag.Set(string(observability.BagStarted), started)
		bag.Set(string(observability.BagStatus), rec.status)
		bag.Set(string(observability.BagModel), model)
		bag.Set(string(observability.BagProvider), provider)
		bag.Set(string(observability.BagAPIKeyID), apiKeyID)
		bag.Set(string(observability.BagLogCtx), lc)
		bag.Set(string(observability.BagUsage), usage)
		// Record into the in-memory quota sliding window. apiKeyID is empty for
		// unauthenticated/open requests — skip those. For requests that failed
		// before usage was captured, usage is zero so only the request counts.
		if apiKeyID != "" {
			g.Quota.Record(apiKeyID, 1, int64(usage.PromptTokens+usage.CompletionTokens))
		}
	}()

	plugin.RunPhaseHooks(plugin.PhaseOnRequest, &plugin.PhaseContext{Ctx: r.Context(), Request: req, Bag: bag})

	// route: model name → model (with backends) — read from the in-memory cache.
	m := g.snapshot().ModelByName(req.Model)
	if m == nil {
		writeJSONError(rec, http.StatusNotFound, "model not found: "+req.Model)
		return
	}
	model = *m
	bag.Set(string(observability.BagModel), model)
	if !model.IsEnabled {
		writeJSONError(rec, http.StatusServiceUnavailable, "model disabled: "+req.Model)
		return
	}
	if len(model.Targets) == 0 {
		writeJSONError(rec, http.StatusServiceUnavailable, "no backends for model: "+req.Model)
		return
	}

	// inbound auth + OnAccess
	plugin.RunPhaseHooks(plugin.PhaseOnAccess, &plugin.PhaseContext{Ctx: r.Context(), Request: req, Bag: bag})
	if status, msg := checkAccess(g.snapshot(), g.Quota, model, r, &apiKeyID, &lc.APIKeyName); status != 0 {
		writeJSONError(rec, status, msg)
		return
	}

	// select + failover: try each backend (ordered by the balance strategy)
	// until one returns a usable response; fail over on network error or 5xx.
	ordered := g.Router.Select(model.Targets, model.Balance)
	served := false
	for _, target := range ordered {
		p := g.snapshot().ProviderGet(target.ProviderID)
		if p == nil || !p.IsEnabled {
			continue
		}
		actualModel := target.Model
		if actualModel == "" || actualModel == "*" {
			actualModel = req.Model
		}
		req.Model = actualModel

		// resolve egress codec: if the provider speaks a different protocol
		// than the client, encode upstream requests with the egress codec and
		// decode upstream responses with it, then format for the client with
		// the ingress codec (cross-protocol transform).
		egressHandler := ingress
		if proto, parseErr := ids.ParseProtocol(p.Protocol); parseErr == nil {
			if ep, ok := ids.ChatEndpointFor(proto); ok && ep != ingress.Endpoint() {
				if h, found := codec.Get(ep); found {
					egressHandler = h
				}
			}
		}

		// Build the outbound request via the Vendor pipeline (7-step:
		// pre_encode → codec encode → post_encode → auth → build_url) when a
		// vendor is registered; otherwise fallback to direct codec encode.
		v := vendor.Global().Resolve(p.Vendor, p.Protocol)
		var outbound codec.OutboundRequest
		var err error
		if v == nil {
			// Fallback: direct codec encode + protocol-based auth + URL.
			outbound, err = egressHandler.MakeRequestEncoder().Encode(req)
			if err == nil {
				cred := g.resolveCredential(*p)
				if outbound.Headers == nil {
					outbound.Headers = map[string]string{}
				}
				for k, val := range authHeadersFor(p.Protocol, cred) {
					outbound.Headers[k] = val
				}
				outbound.Path = buildUpstreamURL(p.BaseURL, outbound.Path)
			}
		} else {
			pctx := &vendor.ProviderCtx{
				Provider:    vendor.VendorProvider{ID: p.ID, Vendor: p.Vendor, Protocol: p.Protocol, BaseURL: p.BaseURL, AuthMode: p.AuthMode},
				APIKey:      g.resolveCredential(*p),
				ActualModel: actualModel,
			}
			outbound, err = vendor.BuildRequest(v, req, pctx, egressHandler)
		}
		if err != nil {
			writeJSONError(rec, http.StatusInternalServerError, "encode request: "+err.Error())
			return
		}
		plugin.RunPhaseHooks(plugin.PhaseOnUpstream, &plugin.PhaseContext{Ctx: r.Context(), Request: req, Bag: bag})

		client, cErr := g.httpClientFor(p.UseProxy)
		if cErr != nil {
			g.Router.Record(router.KeyOf(target), false, 0)
			continue
		}
		upStart := time.Now()
		resp, err := g.callUpstream(client, r, outbound)
		latencyMs := float64(time.Since(upStart).Microseconds()) / 1000
		if err != nil {
			g.Router.Record(router.KeyOf(target), false, latencyMs)
			continue // network error → next backend
		}
		if resp.StatusCode >= 500 {
			resp.Body.Close()
			g.Router.Record(router.KeyOf(target), false, latencyMs)
			continue // server error → retryable
		}
		// usable response (2xx or 4xx client error) → serve, no more failover.
		// Populate provider + upstream logCtx fields + bag now that they're known.
		provider = *p
		lc.UpstreamModel = actualModel
		lc.UpstreamProtocol = egressHandler.Endpoint().String()
		us := int32(resp.StatusCode)
		lc.UpstreamStatus = &us
		um := int64(latencyMs)
		lc.LatencyUpstreamMs = &um
		bag.Set(string(observability.BagProvider), provider)
		g.Router.Record(router.KeyOf(target), true, latencyMs)
		switch {
		case resp.StatusCode >= 400:
			forwardError(rec, resp)
		case ingress.Endpoint() == ids.OpenAICompatibleEmbeddingsV1:
			copyResponse(rec, resp)
		case req.Stream.Enabled:
			g.serveStream(r.Context(), rec, resp.Body, egressHandler, ingress, &usage, bag)
		default:
			g.serveNonStream(r.Context(), rec, resp.Body, egressHandler, ingress, req, &usage, bag)
		}
		resp.Body.Close()
		served = true
		break
	}
	if !served {
		writeJSONError(rec, http.StatusBadGateway, "all upstream backends failed")
	}
}

// callUpstream sends the outbound HTTP request (without writing to the
// client), so the dispatcher can fail over before committing a response.
// The outbound already has the full URL + auth headers set by the vendor
// pipeline or the fallback path.
func (g *Gateway) callUpstream(client *http.Client, r *http.Request, outbound codec.OutboundRequest) (*http.Response, error) {
	upstreamReq, err := http.NewRequestWithContext(r.Context(), outbound.Method, outbound.Path, bytes.NewReader(outbound.Body))
	if err != nil {
		return nil, err
	}
	for k, v := range outbound.Headers {
		upstreamReq.Header.Set(k, v)
	}
	if upstreamReq.Header.Get("Content-Type") == "" {
		upstreamReq.Header.Set("Content-Type", "application/json")
	}
	return client.Do(upstreamReq)
}

// serveStream decodes upstream SSE → IR deltas → re-encodes to client SSE in
// real time, flushing after each frame.
func (g *Gateway) serveStream(ctx context.Context, w http.ResponseWriter, upstream io.Reader, decHandler codec.EndpointHandler, encHandler codec.EndpointHandler, outUsage *ir.Usage, bag *plugin.ContextBag) {
	flusher, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	if flusher != nil {
		flusher.Flush()
	}

	dec := decHandler.MakeStreamResponseDecoder()
	enc := encHandler.MakeStreamResponseEncoder()

	scanner := bufio.NewScanner(upstream)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)

	var usage ir.Usage
	for scanner.Scan() {
		line := scanner.Text()
		// Only process "data:" lines (Anthropic pairs event:/data:; data carries the type).
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" {
			continue
		}
		deltas, err := dec.ParseChunk(payload)
		if err != nil {
			continue
		}
		for _, d := range deltas {
			if u, ok := d.(*ir.UsageDelta); ok {
				usage = u.Usage
			}
			// Per-delta OnResponse: run hooks on every delta (not just the first).
			plugin.RunPhaseHooks(plugin.PhaseOnResponse, &plugin.PhaseContext{Ctx: ctx, Delta: d, Bag: bag})
		}
		frames, _ := enc.FormatDeltas(deltas)
		writeSSE(w, frames, flusher)
	}
	for _, d := range dec.Finish() {
		frames, _ := enc.FormatDeltas([]ir.StreamDelta{d})
		writeSSE(w, frames, flusher)
	}
	done, _ := enc.FormatDone(usage)
	writeSSE(w, done, flusher)
	*outUsage = usage
}

// serveNonStream decodes the full upstream body → AiResponse → formats it.
func (g *Gateway) serveNonStream(ctx context.Context, w http.ResponseWriter, upstream io.Reader, decHandler codec.EndpointHandler, encHandler codec.EndpointHandler, req *ir.AiRequest, outUsage *ir.Usage, bag *plugin.ContextBag) {
	raw, err := io.ReadAll(upstream)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "read upstream: "+err.Error())
		return
	}
	resp, err := decHandler.MakeResponseDecoder().Parse(raw)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "parse upstream: "+err.Error())
		return
	}
	plugin.RunPhaseHooks(plugin.PhaseOnResponse, &plugin.PhaseContext{Ctx: ctx, Request: req, Response: resp, Bag: bag})
	out, err := encHandler.MakeResponseEncoder().Format(resp)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "format response: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	*outUsage = resp.Usage
	_, _ = w.Write(out)
}

func writeSSE(w io.Writer, frames []codec.SSE, flusher http.Flusher) {
	for _, f := range frames {
		_, _ = w.Write(f.Bytes())
	}
	if flusher != nil {
		flusher.Flush()
	}
}

func forwardError(w http.ResponseWriter, resp *http.Response) {
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// copyResponse writes an upstream response to the client verbatim (status +
// all headers + body), used for passthrough endpoints like embeddings.
func copyResponse(w http.ResponseWriter, resp *http.Response) {
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	body, _ := json.Marshal(map[string]any{
		"error": map[string]any{"message": message, "type": "gateway_error"},
	})
	_, _ = w.Write(body)
}
