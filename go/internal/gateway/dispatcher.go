package gateway

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	configsnapshot "github.com/nyroway/nyro/go/internal/config/snapshot"
	"github.com/nyroway/nyro/go/internal/llm"
	"github.com/nyroway/nyro/go/internal/llm/protocol"
	"github.com/nyroway/nyro/go/internal/llm/provider"
	"github.com/nyroway/nyro/go/internal/llm/routing"
	"github.com/nyroway/nyro/go/internal/pipeline"
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
func (g *Gateway) Dispatch(w http.ResponseWriter, r *http.Request, req llm.ModelRequest, ingress protocol.IngressCodec) {
	stream := requestStreams(req)
	rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
	ex := &pipeline.Exchange{
		Ctx:     r.Context(),
		W:       rec,
		R:       r,
		Req:     req,
		Status:  http.StatusOK,
		Started: time.Now(),
		Stream:  stream,
	}
	// The recorder writes the status onto the exchange as it happens, so a
	// Stage emitting from a defer sees what the client actually got.
	rec.ex = ex
	ex.SetExt(telemetry.ExtLogCtx, telemetry.LogCtx{
		Method:         r.Method,
		Path:           r.URL.Path,
		ClientProtocol: string(ingress.Endpoint().Protocol),
		ClientModel:    req.ModelID(),
		IsStream:       stream,
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
func (g *Gateway) forward(ex *pipeline.Exchange, ingress protocol.IngressCodec) {
	rec := ex.W
	req := ex.Req
	route, _ := ex.GetExt(telemetry.ExtRoute).(configsnapshot.Route)

	// select + failover: try each backend (ordered by the balance strategy),
	// retrying the same backend up to settings.proxy.max_retries times on a
	// retry_on_status code or network error, then failing over to the next
	// backend; stop at the first usable response.
	targets, strategy := routingTargets(route)
	ordered := g.Router.Select(targets, strategy)
	ps := resolveProxySettings(g.snapshot())
	clientModel := req.ModelID()
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
		req.SetModelID(actualModel)

		// resolve egress codec: if the provider speaks a different protocol
		// than the client, encode upstream requests with the egress codec and
		// decode upstream responses with it, then format for the client with
		// the ingress codec (cross-protocol transform).
		egressHandler, found := g.Protocols.Egress(ingress.Endpoint())
		if !found {
			writeJSONError(rec, http.StatusInternalServerError, "no egress codec for endpoint "+ingress.Endpoint().String())
			return
		}
		if proto, parseErr := protocol.ParseProtocol(p.Protocol); parseErr == nil {
			if ep, ok := g.Protocols.EndpointFor(proto); ok && ep != ingress.Endpoint() {
				if h, found := g.Protocols.Egress(ep); found {
					egressHandler = h
				}
			}
		}

		// The codec owns protocol conversion; the provider driver owns vendor
		// URL construction, authentication, and provider extensions.
		outbound, err := encodeRequest(egressHandler, req)
		if err != nil {
			writeJSONError(rec, http.StatusInternalServerError, "encode request: "+err.Error())
			return
		}
		if g.Providers == nil {
			writeJSONError(rec, http.StatusInternalServerError, "provider catalog is not configured")
			return
		}
		factory := g.Providers.DriverFor(p.Provider)
		if factory == nil {
			writeJSONError(rec, http.StatusInternalServerError, "provider driver is not configured")
			return
		}
		driver := factory()
		prepared, prepareErr := driver.Prepare(ex.Ctx, runtimeFromUpstream(*p), outbound)
		if prepareErr != nil {
			g.Router.Record(routing.KeyOf(target), false, 0)
			continue
		}

		transport, transportErr := g.providerTransportFor(p.ProxyURL)
		if transportErr != nil {
			g.Router.Record(routing.KeyOf(target), false, 0)
			continue
		}

		// Retry this backend up to MaxRetries times (1 = no retry, just the
		// initial attempt) on a network error or a retry_on_status code.
		attempts := max(ps.MaxRetries, 1)
		var resp *provider.Response
		var latencyMs float64
		for attempt := 1; attempt <= attempts; attempt++ {
			upStart := time.Now()
			resp, err = transport.Do(ex.Ctx, prepared)
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
			g.Router.Record(routing.KeyOf(target), false, latencyMs)
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
		g.Router.Record(routing.KeyOf(target), true, latencyMs)
		if driver.Classify(*resp).Failed {
			forwardError(rec, resp)
		} else {
			switch req := req.(type) {
			case *llm.EmbeddingRequest:
				if ingress.Endpoint() != egressHandler.Endpoint() ||
					!ingress.Capabilities().OpaquePassthrough ||
					!egressHandler.Capabilities().OpaquePassthrough {
					writeJSONError(rec, http.StatusBadGateway, "embedding response requires same-endpoint opaque passthrough")
					break
				}
				copyResponse(rec, resp)
			case *llm.ChatRequest:
				decHandler, decOK := egressHandler.(protocol.ChatEgressCodec)
				encHandler, encOK := ingress.(protocol.ChatIngressCodec)
				if !decOK || !encOK {
					writeJSONError(rec, http.StatusBadGateway, "chat codec does not support selected endpoint")
					break
				}
				if req.Stream.Enabled {
					g.serveStream(ex, resp.Body, decHandler, encHandler)
				} else {
					g.serveNonStream(ex, resp.Body, decHandler, encHandler)
				}
			default:
				writeJSONError(rec, http.StatusInternalServerError, "unsupported llm workload")
			}
		}
		_ = resp.Body.Close()
		served = true
		break
	}
	if !served {
		writeJSONError(rec, http.StatusBadGateway, "all upstream backends failed")
	}
}

func requestStreams(req llm.ModelRequest) bool {
	chat, ok := req.(*llm.ChatRequest)
	return ok && chat.Stream.Enabled
}

func encodeRequest(handler protocol.EgressCodec, req llm.ModelRequest) (protocol.WireRequest, error) {
	endpoint := handler.Endpoint()
	switch req := req.(type) {
	case *llm.ChatRequest:
		chatHandler, ok := handler.(protocol.ChatEgressCodec)
		if !ok {
			return protocol.WireRequest{}, fmt.Errorf("endpoint %s does not support chat", endpoint)
		}
		return chatHandler.EncodeRequest(req)
	case *llm.EmbeddingRequest:
		embeddingHandler, ok := handler.(protocol.EmbeddingEgressCodec)
		if !ok {
			return protocol.WireRequest{}, fmt.Errorf("endpoint %s does not support embedding", endpoint)
		}
		return embeddingHandler.EncodeRequest(req)
	default:
		return protocol.WireRequest{}, fmt.Errorf("unsupported llm request %T", req)
	}
}

// runtimeFromUpstream projects the runtime snapshot's outbound-relevant fields
// into the provider package's runtime view.
func runtimeFromUpstream(u configsnapshot.Upstream) provider.UpstreamRuntime {
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
func (g *Gateway) serveStream(ex *pipeline.Exchange, upstream io.Reader, decHandler protocol.ChatEgressCodec, encHandler protocol.ChatIngressCodec) {
	w := ex.W
	flusher, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	if flusher != nil {
		flusher.Flush()
	}

	dec := decHandler.NewStreamDecoder()
	enc := encHandler.NewStreamEncoder()

	reader := bufio.NewReaderSize(upstream, 64*1024)

	var usage llm.Usage
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
			if u, ok := d.(*llm.UsageDelta); ok {
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
		frames, _ := enc.FormatDeltas([]llm.StreamDelta{d})
		writeSSE(w, frames, flusher)
	}
	done, _ := enc.FormatDone(usage)
	writeSSE(w, done, flusher)
	ex.Usage = usage
}

// serveNonStream decodes the full upstream body → ChatResponse → formats it.
func (g *Gateway) serveNonStream(ex *pipeline.Exchange, upstream io.Reader, decHandler protocol.ChatEgressCodec, encHandler protocol.ChatIngressCodec) {
	w := ex.W
	raw, err := io.ReadAll(upstream)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "read upstream: "+err.Error())
		return
	}
	resp, err := decHandler.DecodeResponse(protocol.WireResponse{Status: http.StatusOK, Body: raw})
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "parse upstream: "+err.Error())
		return
	}
	ex.Resp = resp
	out, err := encHandler.EncodeResponse(resp)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "format response: "+err.Error())
		return
	}
	for key, value := range out.Headers {
		w.Header().Set(key, value)
	}
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "application/json")
	}
	status := out.Status
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	ex.Usage = resp.Usage
	_, _ = w.Write(out.Body)
}

func writeSSE(w io.Writer, frames []protocol.Event, flusher http.Flusher) {
	for _, f := range frames {
		_, _ = w.Write(f.Bytes())
	}
	if flusher != nil {
		flusher.Flush()
	}
}

func forwardError(w http.ResponseWriter, resp *provider.Response) {
	if values := resp.Headers["Content-Type"]; len(values) > 0 && values[0] != "" {
		w.Header().Set("Content-Type", values[0])
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// copyResponse writes an upstream response to the client verbatim (status +
// all headers + body), used for passthrough endpoints like embeddings.
func copyResponse(w http.ResponseWriter, resp *provider.Response) {
	for k, vs := range resp.Headers {
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
