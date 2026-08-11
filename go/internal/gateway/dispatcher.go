package gateway

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/nyroway/nyro/go/internal/pipeline"
	"github.com/nyroway/nyro/go/internal/protocol/llm/codec"
	"github.com/nyroway/nyro/go/internal/protocol/llm/ir"
	"github.com/nyroway/nyro/go/internal/protocol/llm/spec"
	"github.com/nyroway/nyro/go/internal/provider"
	"github.com/nyroway/nyro/go/internal/router"
	"github.com/nyroway/nyro/go/internal/storage"
	"github.com/nyroway/nyro/go/internal/telemetry"
)

// Dispatch is the single orchestration entry point. The ingress shell
// (handleProxy) has already decoded the wire body into IR; Dispatch builds the
// Exchange, runs it through the Stage chain (telemetry, authn, authz, quota),
// and the chain's terminal resolves the model→backend→provider from the
// in-memory cache, forwards to the upstream, and converts the response back.
//
// Cross-cutting concerns live in Stages, not here: this function is routing,
// failover, and codec transformation, and nothing else. See internal/pipeline
// for the chain contract and internal/telemetry.Stage for the terminal
// telemetry Stage.
func (g *Gateway) Dispatch(w http.ResponseWriter, r *http.Request, req *ir.AiRequest, ingress codec.EndpointHandler) {
	rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
	ex := &pipeline.Exchange{
		Ctx:     r.Context(),
		W:       rec,
		R:       r,
		Req:     req,
		Status:  http.StatusOK,
		Started: time.Now(),
		Stream:  req.Stream.Enabled,
	}
	// The recorder writes the status onto the exchange as it happens, so a
	// Stage emitting from a defer sees what the client actually got.
	rec.ex = ex
	ex.SetExt(telemetry.ExtLogCtx, telemetry.LogCtx{
		Method:         r.Method,
		Path:           r.URL.Path,
		ClientProtocol: string(ingress.Endpoint().Protocol),
		ClientModel:    req.Model,
		IsStream:       req.Stream.Enabled,
	})

	// The chain's Stages own auth, quota, and telemetry. Errors are already
	// written to the client by the Stage that produced them, so the return
	// value is only for Stages that want to inspect it.
	_ = g.chain().Run(ex, func() error {
		g.forward(ex, ingress)
		return nil
	})
}

// forward is the chain's terminal: select a backend, transform, call the
// upstream, and write the response. It runs only if no Stage short-circuited.
func (g *Gateway) forward(ex *pipeline.Exchange, ingress codec.EndpointHandler) {
	rec := ex.W
	req := ex.Req
	route, _ := ex.GetExt(telemetry.ExtRoute).(storage.Route)

	// select + failover: try each backend (ordered by the balance strategy),
	// retrying the same backend up to settings.proxy.max_retries times on a
	// retry_on_status code or network error, then failing over to the next
	// backend; stop at the first usable response.
	targets, strategy := routingTargets(route)
	ordered := g.Router.Select(targets, strategy)
	ps := resolveProxySettings(g.snapshot())
	clientModel := req.Model
	served := false
	for _, target := range ordered {
		p := g.snapshot().UpstreamGet(target.UpstreamID)
		if p == nil || !p.Enabled {
			continue
		}
		actualModel := target.Model
		if actualModel == "" || actualModel == "*" {
			actualModel = clientModel
		}
		req.Model = actualModel

		// resolve egress codec: if the provider speaks a different protocol
		// than the client, encode upstream requests with the egress codec and
		// decode upstream responses with it, then format for the client with
		// the ingress codec (cross-protocol transform).
		egressHandler := ingress
		if proto, parseErr := spec.ParseProtocol(p.Protocol); parseErr == nil {
			if ep, ok := codec.EndpointFor(proto); ok && ep != ingress.Endpoint() {
				if h, found := codec.Get(ep); found {
					egressHandler = h
				}
			}
		}

		// Build the outbound request: codec-encode the IR, then build the
		// upstream URL and resolve the provider's outbound authenticator.
		outbound, err := egressHandler.MakeRequestEncoder().Encode(req)
		if err != nil {
			writeJSONError(rec, http.StatusInternalServerError, "encode request: "+err.Error())
			return
		}
		outbound.Path = provider.BuildURL(p.BaseURL, outbound.Path)

		auth, authErr := provider.AuthenticatorFor(p.Provider, p.Protocol, runtimeFromUpstream(*p))
		if authErr != nil {
			g.Router.Record(router.KeyOf(target), false, 0)
			continue // can't authenticate for this backend → next backend
		}

		client, cErr := g.httpClientFor(p.ProxyURL)
		if cErr != nil {
			g.Router.Record(router.KeyOf(target), false, 0)
			continue // can't build a client for this backend → next backend
		}

		// Retry this backend up to MaxRetries times (1 = no retry, just the
		// initial attempt) on a network error or a retry_on_status code.
		attempts := max(ps.MaxRetries, 1)
		var resp *http.Response
		var latencyMs float64
		for attempt := 1; attempt <= attempts; attempt++ {
			upStart := time.Now()
			resp, err = g.callUpstream(client, ex.R, outbound, auth)
			latencyMs = float64(time.Since(upStart).Microseconds()) / 1000
			if err != nil {
				resp = nil
				if attempt < attempts {
					continue
				}
				break
			}
			if ps.RetryOnStatus[resp.StatusCode] {
				_ = resp.Body.Close()
				resp = nil
				if attempt < attempts {
					continue
				}
				break
			}
			break // usable response (not in retry_on_status) → stop retrying
		}
		if resp == nil {
			g.Router.Record(router.KeyOf(target), false, latencyMs)
			continue // exhausted retries on this backend → next backend
		}

		// usable response (2xx, or a non-retried 4xx/5xx) → serve, no more
		// failover. Record what the telemetry Stage needs now that it's known.
		lc, _ := ex.GetExt(telemetry.ExtLogCtx).(telemetry.LogCtx)
		lc.UpstreamModel = actualModel
		lc.UpstreamProtocol = string(egressHandler.Endpoint().Protocol)
		us := int32(resp.StatusCode)
		lc.UpstreamStatus = &us
		um := int64(latencyMs)
		lc.LatencyUpstreamMs = &um
		ex.SetExt(telemetry.ExtLogCtx, lc)
		ex.SetExt(telemetry.ExtUpstream, *p)
		g.Router.Record(router.KeyOf(target), true, latencyMs)
		switch {
		case resp.StatusCode >= 400:
			forwardError(rec, resp)
		case !ingress.Capabilities().Streaming:
			// Non-streaming endpoints (embeddings) return one JSON body with no
			// IR representation to round-trip, so forward it verbatim.
			copyResponse(rec, resp)
		case req.Stream.Enabled:
			g.serveStream(ex, resp.Body, egressHandler, ingress)
		default:
			g.serveNonStream(ex, resp.Body, egressHandler, ingress)
		}
		_ = resp.Body.Close()
		served = true
		break
	}
	if !served {
		writeJSONError(rec, http.StatusBadGateway, "all upstream backends failed")
	}
}

// callUpstream sends the outbound HTTP request (without writing to the
// client), so the dispatcher can fail over before committing a response. The
// outbound already has the full URL set; auth applies the provider's outbound
// authentication headers last, after the codec-set headers.
func (g *Gateway) callUpstream(client *http.Client, r *http.Request, outbound codec.OutboundRequest, auth provider.Authenticator) (*http.Response, error) {
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
	if err := auth.Apply(r.Context(), upstreamReq); err != nil {
		return nil, err
	}
	return client.Do(upstreamReq)
}

// runtimeFromUpstream projects the storage row's outbound-relevant fields
// into the provider package's runtime view.
func runtimeFromUpstream(u storage.Upstream) provider.UpstreamRuntime {
	return provider.UpstreamRuntime{
		Name:            u.Name,
		Provider:        u.Provider,
		Protocol:        u.Protocol,
		BaseURL:         u.BaseURL,
		CredentialsJSON: u.CredentialsJSON,
		ProxyURL:        u.ProxyURL,
	}
}

// serveStream decodes upstream SSE → IR deltas → re-encodes to client SSE in
// real time, flushing after each frame. Every delta is also handed to the
// chain's StreamStages before it is re-encoded.
func (g *Gateway) serveStream(ex *pipeline.Exchange, upstream io.Reader, decHandler codec.EndpointHandler, encHandler codec.EndpointHandler) {
	w := ex.W
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

	reader := bufio.NewReaderSize(upstream, 64*1024)

	var usage ir.Usage
	for {
		line, readErr := reader.ReadString('\n')
		line = strings.TrimRight(line, "\r\n")
		// Only process "data:" lines (Anthropic pairs event:/data:; data carries the type).
		if !strings.HasPrefix(line, "data:") {
			if readErr != nil {
				break // io.EOF or upstream error: flush what we have and finish
			}
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" {
			if readErr != nil {
				break
			}
			continue
		}
		deltas, err := dec.ParseChunk(payload)
		if err != nil {
			if readErr != nil {
				break
			}
			continue
		}
		for _, d := range deltas {
			if u, ok := d.(*ir.UsageDelta); ok {
				usage = u.Usage
			}
			// Per-delta: every StreamStage sees every frame.
			g.chain().EmitDelta(ex, d)
		}
		frames, _ := enc.FormatDeltas(deltas)
		writeSSE(w, frames, flusher)
		if readErr != nil {
			break // io.EOF or upstream error: flush what we have and finish
		}
	}
	for _, d := range dec.Finish() {
		frames, _ := enc.FormatDeltas([]ir.StreamDelta{d})
		writeSSE(w, frames, flusher)
	}
	done, _ := enc.FormatDone(usage)
	writeSSE(w, done, flusher)
	ex.Usage = usage
}

// serveNonStream decodes the full upstream body → AiResponse → formats it.
func (g *Gateway) serveNonStream(ex *pipeline.Exchange, upstream io.Reader, decHandler codec.EndpointHandler, encHandler codec.EndpointHandler) {
	w := ex.W
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
	ex.Resp = resp
	out, err := encHandler.MakeResponseEncoder().Format(resp)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "format response: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	ex.Usage = resp.Usage
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
		"error": map[string]any{"message": message, "type": "GATEWAY_ERROR"},
	})
	_, _ = w.Write(body)
}
