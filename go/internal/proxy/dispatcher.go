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
// lifecycle phases, resolves the model→backend→provider from Storage, forwards
// to the upstream (Native path, ingress codec for egress), and converts the
// response back. Ported from dispatch_pipeline.
func (g *Gateway) Dispatch(w http.ResponseWriter, r *http.Request, req *ir.AiRequest, ingress codec.EndpointHandler) {
	started := time.Now()
	rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
	var apiKeyID string
	var usage ir.Usage
	model := storage.Model{}
	provider := storage.Provider{}
	lc := logCtx{
		method:         r.Method,
		path:           r.URL.Path,
		clientProtocol: ingress.Endpoint().String(),
		clientModel:    req.Model,
		isStream:       req.Stream.Enabled,
	}

	defer plugin.RunPhaseHooks(plugin.PhaseOnLog, &plugin.PhaseContext{Ctx: r.Context()})
	defer func() { g.appendLog(model, provider, apiKeyID, started, rec.status, usage, lc) }()

	plugin.RunPhaseHooks(plugin.PhaseOnRequest, &plugin.PhaseContext{Ctx: r.Context(), Request: req})

	// route: model name → model (with backends) — read from the in-memory cache.
	m := g.snapshot().ModelByName(req.Model)
	if m == nil {
		writeJSONError(rec, http.StatusNotFound, "model not found: "+req.Model)
		return
	}
	model = *m
	if !model.IsEnabled {
		writeJSONError(rec, http.StatusServiceUnavailable, "model disabled: "+req.Model)
		return
	}
	if len(model.Targets) == 0 {
		writeJSONError(rec, http.StatusServiceUnavailable, "no backends for model: "+req.Model)
		return
	}

	// inbound auth + OnAccess
	plugin.RunPhaseHooks(plugin.PhaseOnAccess, &plugin.PhaseContext{Ctx: r.Context(), Request: req})
	if status, msg := checkAccess(g.snapshot(), g.Storage, model, r, &apiKeyID, &lc.apiKeyName); status != 0 {
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
		plugin.RunPhaseHooks(plugin.PhaseOnUpstream, &plugin.PhaseContext{Ctx: r.Context(), Request: req})

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
		// usable response (2xx or 4xx client error) → serve, no more failover
		provider = *p
		lc.upstreamModel = actualModel
		lc.upstreamProtocol = egressHandler.Endpoint().String()
		us := int32(resp.StatusCode)
		lc.upstreamStatus = &us
		um := int64(latencyMs)
		lc.latencyUpstreamMs = &um
		g.Router.Record(router.KeyOf(target), true, latencyMs)
		switch {
		case resp.StatusCode >= 400:
			forwardError(rec, resp)
		case ingress.Endpoint() == ids.OpenAICompatibleEmbeddingsV1:
			copyResponse(rec, resp)
		case req.Stream.Enabled:
			g.serveStream(r.Context(), rec, resp.Body, egressHandler, ingress, &usage)
		default:
			g.serveNonStream(r.Context(), rec, resp.Body, egressHandler, ingress, req, &usage)
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
func (g *Gateway) serveStream(ctx context.Context, w http.ResponseWriter, upstream io.Reader, decHandler codec.EndpointHandler, encHandler codec.EndpointHandler, outUsage *ir.Usage) {
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
			plugin.RunPhaseHooks(plugin.PhaseOnResponse, &plugin.PhaseContext{Ctx: ctx, Delta: d})
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
func (g *Gateway) serveNonStream(ctx context.Context, w http.ResponseWriter, upstream io.Reader, decHandler codec.EndpointHandler, encHandler codec.EndpointHandler, req *ir.AiRequest, outUsage *ir.Usage) {
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
	plugin.RunPhaseHooks(plugin.PhaseOnResponse, &plugin.PhaseContext{Ctx: ctx, Request: req, Response: resp})
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
