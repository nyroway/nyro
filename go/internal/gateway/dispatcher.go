package gateway

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	configsnapshot "github.com/nyroway/nyro/go/internal/config/snapshot"
	"github.com/nyroway/nyro/go/internal/llm"
	llmpipeline "github.com/nyroway/nyro/go/internal/llm/pipeline"
	"github.com/nyroway/nyro/go/internal/llm/protocol"
	"github.com/nyroway/nyro/go/internal/llm/provider"
	"github.com/nyroway/nyro/go/internal/llm/routing"
	"github.com/nyroway/nyro/go/internal/security/authn"
)

// Dispatch is the single orchestration entry point. The ingress shell
// (handleProxy) has already decoded the wire body into IR; Dispatch builds the
// Exchange and runs the explicit LLM phases. The Dispatch phase retains the
// compatibility implementation for routing, failover, codec transformation,
// Provider calls, and HTTP response writing until the trusted Runtime cutover.
func (g *Gateway) Dispatch(w http.ResponseWriter, r *http.Request, req llm.ModelRequest, ingress protocol.IngressCodec) {
	stream := requestStreams(req)
	rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
	ex := &llmpipeline.Exchange{
		Request:     req,
		Source:      ingress.Endpoint(),
		Credentials: authn.Credentials{APIKey: extractKey(r)},
		Status:      http.StatusOK,
		Started:     time.Now(),
		Streamed:    stream,
		RequestInfo: llmpipeline.RequestInfo{
			ClientModel: req.ModelID(),
			Operation:   r.Method,
			Resource:    r.URL.Path,
		},
	}
	// The recorder writes status onto the Exchange as it happens, so the
	// Observe Finalizer sees what the client actually got.
	rec.ex = ex
	state := &gatewayPipelineState{
		gw:       g,
		snapshot: g.snapshot(),
		writer:   rec,
		ingress:  ingress,
	}
	runner, err := g.newPipelineRunner(state)
	if err != nil {
		writeJSONError(rec, http.StatusInternalServerError, err.Error())
		return
	}
	completion, runErr := runner.Run(r.Context(), ex)
	if completion.Error != nil && !rec.wroteHeader {
		status := http.StatusInternalServerError
		if completion.Error.StatusCode != nil {
			status = int(*completion.Error.StatusCode)
		}
		writeJSONError(rec, status, completion.Error.Message)
		return
	}
	if runErr != nil && r.Context().Err() == nil && !rec.wroteHeader {
		writeJSONError(rec, http.StatusInternalServerError, runErr.Error())
		return
	}
	if completion.Response != nil && !rec.wroteHeader {
		if err := writeCompletionResponse(rec, ingress, completion.Response); err != nil {
			writeJSONError(rec, http.StatusInternalServerError, err.Error())
		}
	}
}

func writeCompletionResponse(w http.ResponseWriter, ingress protocol.IngressCodec, response *llm.ChatResponse) error {
	codec, ok := ingress.(protocol.ChatIngressCodec)
	if !ok {
		return fmt.Errorf("ingress endpoint %s cannot encode a chat response", ingress.Endpoint())
	}
	out, err := codec.EncodeResponse(response)
	if err != nil {
		return fmt.Errorf("format response: %w", err)
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
	_, _ = w.Write(out.Body)
	return nil
}

// forward is the Dispatch phase's gateway-owned HTTP transition adapter.
func (g *Gateway) forward(ctx context.Context, rec http.ResponseWriter, runner *llmpipeline.Runner, ex *llmpipeline.Exchange, route configsnapshot.Route, ingress protocol.IngressCodec, snap *configsnapshot.Snapshot) *llm.Error {
	req := ex.Request

	// select + failover: try each backend (ordered by the balance strategy),
	// retrying the same backend up to settings.proxy.max_retries times on a
	// retry_on_status code or network error, then failing over to the next
	// backend; stop at the first usable response.
	targets, strategy := routingTargets(route)
	ordered := g.Router.Select(targets, strategy)
	ps := resolveProxySettings(snap)
	clientModel := req.ModelID()
	served := false
	for _, target := range ordered {
		p := snap.UpstreamGet(target.UpstreamID)
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
			message := "no egress codec for endpoint " + ingress.Endpoint().String()
			writeJSONError(rec, http.StatusInternalServerError, message)
			return llm.ErrorFromStatus(http.StatusInternalServerError, message)
		}
		if proto, parseErr := protocol.ParseProtocol(p.Protocol); parseErr == nil {
			if ep, ok := g.Protocols.EndpointFor(proto); ok && ep != ingress.Endpoint() {
				if h, found := g.Protocols.Egress(ep); found {
					egressHandler = h
				}
			}
		}
		ex.Target = llmpipeline.Target{
			ID:           routeTargetID(route, target),
			UpstreamID:   p.ID,
			UpstreamName: p.Name,
			Model:        actualModel,
			Endpoint:     egressHandler.Endpoint(),
		}

		// The codec owns protocol conversion; the provider driver owns vendor
		// URL construction, authentication, and provider extensions.
		outbound, err := encodeRequest(egressHandler, req)
		if err != nil {
			message := "encode request: " + err.Error()
			writeJSONError(rec, http.StatusInternalServerError, message)
			return llm.ErrorFromStatus(http.StatusInternalServerError, message)
		}
		if g.Providers == nil {
			message := "provider catalog is not configured"
			writeJSONError(rec, http.StatusInternalServerError, message)
			return llm.ErrorFromStatus(http.StatusInternalServerError, message)
		}
		factory := g.Providers.DriverFor(p.Provider)
		if factory == nil {
			message := "provider driver is not configured"
			writeJSONError(rec, http.StatusInternalServerError, message)
			return llm.ErrorFromStatus(http.StatusInternalServerError, message)
		}
		driver := factory()
		prepared, prepareErr := driver.Prepare(ctx, runtimeFromUpstream(*p), outbound)
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
			resp, err = transport.Do(ctx, prepared)
			latencyMs = float64(time.Since(upStart).Microseconds()) / 1000
			attemptLatencyMs := int64(latencyMs)
			ex.Target.UpstreamLatencyMs = &attemptLatencyMs
			if err != nil {
				resp = nil
				if attempt < attempts {
					continue
				}
				break
			}
			upstreamStatus := int32(resp.StatusCode)
			ex.Target.UpstreamStatus = &upstreamStatus
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
		// failover. Attempt status and latency were published as they arrived.
		g.Router.Record(routing.KeyOf(target), true, latencyMs)
		if driver.Classify(*resp).Failed {
			forwardError(rec, resp)
			_ = resp.Body.Close()
			return llm.ErrorFromStatus(uint16(resp.StatusCode), "upstream provider request failed")
		} else {
			var responseErr *llm.Error
			switch req := req.(type) {
			case *llm.EmbeddingRequest:
				if ingress.Endpoint() != egressHandler.Endpoint() ||
					!ingress.Capabilities().OpaquePassthrough ||
					!egressHandler.Capabilities().OpaquePassthrough {
					message := "embedding response requires same-endpoint opaque passthrough"
					writeJSONError(rec, http.StatusBadGateway, message)
					responseErr = llm.ErrorFromStatus(http.StatusBadGateway, message)
					break
				}
				copyResponse(rec, resp)
			case *llm.ChatRequest:
				decHandler, decOK := egressHandler.(protocol.ChatEgressCodec)
				encHandler, encOK := ingress.(protocol.ChatIngressCodec)
				if !decOK || !encOK {
					message := "chat codec does not support selected endpoint"
					writeJSONError(rec, http.StatusBadGateway, message)
					responseErr = llm.ErrorFromStatus(http.StatusBadGateway, message)
					break
				}
				if req.Stream.Enabled {
					g.serveStream(ctx, rec, runner, ex, resp.Body, decHandler, encHandler)
				} else {
					responseErr = g.serveNonStream(rec, ex, resp.Body, decHandler, encHandler)
				}
			default:
				message := "unsupported llm workload"
				writeJSONError(rec, http.StatusInternalServerError, message)
				responseErr = llm.ErrorFromStatus(http.StatusInternalServerError, message)
			}
			if responseErr != nil {
				_ = resp.Body.Close()
				return responseErr
			}
		}
		_ = resp.Body.Close()
		served = true
		break
	}
	if !served {
		message := "all upstream backends failed"
		writeJSONError(rec, http.StatusBadGateway, message)
		return llm.ErrorFromStatus(http.StatusBadGateway, message)
	}
	return nil
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
// real time, flushing after each frame. Every parsed delta is also handed to
// the Runner's observers before it is re-encoded.
func (g *Gateway) serveStream(ctx context.Context, w http.ResponseWriter, runner *llmpipeline.Runner, ex *llmpipeline.Exchange, upstream io.Reader, decHandler protocol.ChatEgressCodec, encHandler protocol.ChatIngressCodec) {
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
			// Per-delta observers cannot control or rewrite flow.
			if runner != nil {
				runner.ObserveDelta(ctx, ex, d)
			}
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
func (g *Gateway) serveNonStream(w http.ResponseWriter, ex *llmpipeline.Exchange, upstream io.Reader, decHandler protocol.ChatEgressCodec, encHandler protocol.ChatIngressCodec) *llm.Error {
	raw, err := io.ReadAll(upstream)
	if err != nil {
		message := "read upstream: " + err.Error()
		writeJSONError(w, http.StatusBadGateway, message)
		return llm.ErrorFromStatus(http.StatusBadGateway, message)
	}
	resp, err := decHandler.DecodeResponse(protocol.WireResponse{Status: http.StatusOK, Body: raw})
	if err != nil {
		message := "parse upstream: " + err.Error()
		writeJSONError(w, http.StatusBadGateway, message)
		return llm.ErrorFromStatus(http.StatusBadGateway, message)
	}
	ex.Response = resp
	out, err := encHandler.EncodeResponse(resp)
	if err != nil {
		message := "format response: " + err.Error()
		writeJSONError(w, http.StatusBadGateway, message)
		return llm.ErrorFromStatus(http.StatusBadGateway, message)
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
	return nil
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
