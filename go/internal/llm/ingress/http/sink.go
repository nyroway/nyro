package httpingress

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/nyroway/nyro/go/internal/llm"
	"github.com/nyroway/nyro/go/internal/llm/protocol"
)

type httpSink struct {
	writer     http.ResponseWriter
	ingress    protocol.IngressCodec
	encoder    protocol.StreamEncoder
	usage      llm.Usage
	streamOpen bool
	terminated bool
}

func newSink(writer http.ResponseWriter, ingress protocol.IngressCodec) *httpSink {
	return &httpSink{writer: writer, ingress: ingress}
}

func (sink *httpSink) ResetStreamAttempt() error {
	if sink == nil || sink.writer == nil || sink.ingress == nil {
		return errors.New("HTTP Sink is not configured")
	}
	if sink.streamOpen {
		return errors.New("HTTP stream Sink is already committed")
	}
	sink.encoder = nil
	sink.usage = llm.Usage{}
	sink.terminated = false
	return nil
}

func (sink *httpSink) SendResponse(ctx context.Context, response *llm.ChatResponse) error {
	if err := sink.begin(ctx, "HTTP Sink"); err != nil {
		return err
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
	sink.terminated = true
	return sink.writeWire(ctx, wire, "application/json")
}

func (sink *httpSink) SendError(ctx context.Context, providerError *llm.Error) error {
	if err := sink.begin(ctx, "HTTP Sink"); err != nil {
		return err
	}
	wire, err := sink.ingress.EncodeError(providerError)
	if err != nil {
		return fmt.Errorf("format error: %w", err)
	}
	if wire.Status == 0 {
		wire.Status = errorStatus(providerError)
	}
	sink.terminated = true
	return sink.writeWire(ctx, wire, "application/json")
}

func (sink *httpSink) SendOpaque(ctx context.Context, response protocol.WireResponse) error {
	if err := sink.begin(ctx, "HTTP Sink"); err != nil {
		return err
	}
	if response.Status == 0 {
		response.Status = http.StatusOK
	}
	sink.terminated = true
	return sink.writeWire(ctx, response, "")
}

func (sink *httpSink) begin(ctx context.Context, name string) error {
	if sink == nil || sink.writer == nil || sink.ingress == nil {
		return errors.New("HTTP Sink is not configured")
	}
	if sink.terminated {
		return fmt.Errorf("%s is already terminated", name)
	}
	if ctx != nil {
		return ctx.Err()
	}
	return nil
}

func (sink *httpSink) writeWire(ctx context.Context, response protocol.WireResponse, defaultContentType string) error {
	hasContentType := false
	for key, value := range response.Headers {
		if strings.EqualFold(key, "Content-Type") && value != "" {
			hasContentType = true
		}
		sink.writer.Header().Set(key, value)
	}
	if !hasContentType && defaultContentType != "" {
		sink.writer.Header().Set("Content-Type", defaultContentType)
	}
	sink.writer.WriteHeader(response.Status)
	if len(response.Body) == 0 {
		return contextError(ctx)
	}
	if _, err := sink.writer.Write(response.Body); err != nil {
		return fmt.Errorf("write HTTP response: %w", err)
	}
	return contextError(ctx)
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

func errorStatus(providerError *llm.Error) int {
	if providerError != nil && providerError.StatusCode != nil && *providerError.StatusCode > 0 {
		return int(*providerError.StatusCode)
	}
	return http.StatusInternalServerError
}
