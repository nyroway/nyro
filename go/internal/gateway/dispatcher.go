package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/nyroway/nyro/go/internal/llm"
	"github.com/nyroway/nyro/go/internal/llm/protocol"
	llmruntime "github.com/nyroway/nyro/go/internal/llm/runtime"
	"github.com/nyroway/nyro/go/internal/security/authn"
)

// Dispatch is the transitional HTTP ingress adapter. It extracts normalized
// Call metadata and delegates all route, Provider, retry, failover, response,
// and stream-state authority to the Snapshot-bound trusted LLM Runtime.
func (g *Gateway) Dispatch(w http.ResponseWriter, request *http.Request, modelRequest llm.ModelRequest, ingress protocol.IngressCodec) {
	recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
	snapshot := g.snapshot()
	runtime, err := g.runtimeFor(snapshot)
	if err != nil {
		writeJSONError(recorder, http.StatusInternalServerError, err.Error())
		return
	}
	sink := &httpSink{writer: recorder, ingress: ingress}
	runtime.Execute(request.Context(), llmruntime.Call{
		Request:       modelRequest,
		Source:        ingress.Endpoint(),
		Credentials:   authn.Credentials{APIKey: extractKey(request)},
		ClientAddress: request.RemoteAddr,
		RequestID:     request.Header.Get("X-Request-ID"),
		Sink:          sink,
	})
}

// httpSink is intentionally the only remaining wire-writing responsibility in
// Gateway. Task 9 moves this adapter unchanged in purpose to LLM HTTP Ingress.
type httpSink struct {
	writer     http.ResponseWriter
	ingress    protocol.IngressCodec
	encoder    protocol.StreamEncoder
	usage      llm.Usage
	streamOpen bool
	terminated bool
}

func (sink *httpSink) SendResponse(_ context.Context, response *llm.ChatResponse) error {
	if sink.terminated {
		return errors.New("HTTP Sink is already terminated")
	}
	codec, ok := sink.ingress.(protocol.ChatIngressCodec)
	if !ok {
		return fmt.Errorf("ingress endpoint %s cannot encode a chat response", sink.ingress.Endpoint())
	}
	wire, err := codec.EncodeResponse(response)
	if err != nil {
		return fmt.Errorf("format response: %w", err)
	}
	if wire.Status == 0 {
		wire.Status = http.StatusOK
	}
	if wire.Headers == nil {
		wire.Headers = map[string]string{"Content-Type": "application/json"}
	} else if wire.Headers["Content-Type"] == "" {
		wire.Headers["Content-Type"] = "application/json"
	}
	sink.terminated = true
	return sink.writeWire(wire)
}

func (sink *httpSink) SendError(_ context.Context, providerError *llm.Error) error {
	if sink.terminated {
		return errors.New("HTTP Sink is already terminated")
	}
	status := http.StatusInternalServerError
	if providerError != nil && providerError.StatusCode != nil {
		status = int(*providerError.StatusCode)
	}
	message := "LLM request failed"
	if providerError != nil && providerError.Message != "" {
		message = providerError.Message
	}
	sink.terminated = true
	writeJSONError(sink.writer, status, message)
	return nil
}

func (sink *httpSink) SendDelta(_ context.Context, delta llm.StreamDelta) error {
	if sink.terminated {
		return errors.New("HTTP stream Sink is already terminated")
	}
	if sink.encoder == nil {
		codec, ok := sink.ingress.(protocol.ChatIngressCodec)
		if !ok {
			return fmt.Errorf("ingress endpoint %s cannot encode a chat stream", sink.ingress.Endpoint())
		}
		sink.encoder = codec.NewStreamEncoder()
		if sink.encoder == nil {
			return fmt.Errorf("ingress endpoint %s returned no stream encoder", sink.ingress.Endpoint())
		}
	}
	if usage, ok := delta.(*llm.UsageDelta); ok {
		sink.usage = usage.Usage
	}
	frames, err := sink.encoder.FormatDeltas([]llm.StreamDelta{delta})
	if err != nil {
		return fmt.Errorf("format stream delta: %w", err)
	}
	if !sink.streamOpen {
		sink.writer.Header().Set("Content-Type", "text/event-stream")
		sink.writer.Header().Set("Cache-Control", "no-cache")
		sink.writer.Header().Set("Connection", "keep-alive")
		sink.writer.WriteHeader(http.StatusOK)
		sink.streamOpen = true
		flush(sink.writer)
	}
	if err := writeEvents(sink.writer, frames); err != nil {
		return err
	}

	switch delta.(type) {
	case *llm.DoneDelta:
		done, err := sink.encoder.FormatDone(sink.usage)
		if err != nil {
			return fmt.Errorf("format stream completion: %w", err)
		}
		if err := writeEvents(sink.writer, done); err != nil {
			return err
		}
		sink.terminated = true
	case *llm.StreamErrorDelta, *llm.UnexpectedEOFDelta:
		// Existing codecs do not all have a native terminal error event. A
		// safe stream close is the compatibility fallback until Task 9.
		sink.terminated = true
	}
	flush(sink.writer)
	return nil
}

func (sink *httpSink) SendOpaque(_ context.Context, response protocol.WireResponse) error {
	if sink.terminated {
		return errors.New("HTTP Sink is already terminated")
	}
	if response.Status == 0 {
		response.Status = http.StatusOK
	}
	sink.terminated = true
	return sink.writeWire(response)
}

func (sink *httpSink) writeWire(response protocol.WireResponse) error {
	for key, value := range response.Headers {
		sink.writer.Header().Set(key, value)
	}
	sink.writer.WriteHeader(response.Status)
	if len(response.Body) == 0 {
		return nil
	}
	_, err := sink.writer.Write(response.Body)
	return err
}

func writeEvents(writer io.Writer, events []protocol.Event) error {
	for _, event := range events {
		if _, err := writer.Write(event.Bytes()); err != nil {
			return fmt.Errorf("write stream event: %w", err)
		}
	}
	return nil
}

func flush(writer http.ResponseWriter) {
	if flusher, ok := writer.(http.Flusher); ok {
		flusher.Flush()
	}
}

func writeJSONError(writer http.ResponseWriter, status int, message string) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	body, _ := json.Marshal(map[string]any{
		"error": map[string]any{"message": message, "type": "GATEWAY_ERROR"},
	})
	_, _ = writer.Write(body)
}

var _ llmruntime.Sink = (*httpSink)(nil)
