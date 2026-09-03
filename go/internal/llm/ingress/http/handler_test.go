package httpingress

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	configsnapshot "github.com/nyroway/nyro/go/internal/config/snapshot"
	"github.com/nyroway/nyro/go/internal/llm"
	"github.com/nyroway/nyro/go/internal/llm/protocol"
	"github.com/nyroway/nyro/go/internal/llm/protocol/anthropic/messages"
	"github.com/nyroway/nyro/go/internal/llm/provider"
	llmruntime "github.com/nyroway/nyro/go/internal/llm/runtime"
)

type testIngressCodec struct {
	endpoint protocol.Endpoint
	routes   []protocol.IngressRoute
	decode   func(protocol.IngressRequest) (*llm.ChatRequest, error)
	stream   protocol.StreamEncoder
}

func (codec testIngressCodec) Endpoint() protocol.Endpoint { return codec.endpoint }
func (codec testIngressCodec) Capabilities() protocol.Capabilities {
	return protocol.Capabilities{IngressRoutes: append([]protocol.IngressRoute(nil), codec.routes...), Streaming: codec.stream != nil}
}
func (testIngressCodec) IngressCodec() {}
func (codec testIngressCodec) DecodeRequest(request protocol.IngressRequest) (*llm.ChatRequest, error) {
	if codec.decode != nil {
		return codec.decode(request)
	}
	return llm.NewChatRequest("client-model", nil), nil
}
func (testIngressCodec) EncodeResponse(response *llm.ChatResponse) (protocol.WireResponse, error) {
	return protocol.WireResponse{
		Status:  http.StatusCreated,
		Headers: map[string]string{"Content-Type": "application/test-response"},
		Body:    []byte("response:" + response.Content),
	}, nil
}
func (testIngressCodec) EncodeError(providerError *llm.Error) (protocol.WireResponse, error) {
	status := http.StatusInternalServerError
	message := "LLM request failed"
	if providerError != nil {
		message = providerError.Message
		if providerError.StatusCode != nil {
			status = int(*providerError.StatusCode)
		}
	}
	return protocol.WireResponse{
		Status:  status,
		Headers: map[string]string{"Content-Type": "application/test-error"},
		Body:    []byte("native-error:" + message),
	}, nil
}
func (codec testIngressCodec) NewStreamEncoder() protocol.StreamEncoder { return codec.stream }

type testStreamEncoder struct{}

func (testStreamEncoder) FormatDeltas(deltas []llm.StreamDelta) ([]protocol.Event, error) {
	if len(deltas) != 1 {
		return nil, fmt.Errorf("got %d deltas, want 1", len(deltas))
	}
	switch delta := deltas[0].(type) {
	case *llm.TextDelta:
		return []protocol.Event{{Event: "text", Data: delta.Text}}, nil
	case *llm.StreamErrorDelta:
		return []protocol.Event{{Event: "error", Data: `{"type":"native_error","message":"` + delta.Error.Message + `"}`}}, nil
	case *llm.DoneDelta:
		return []protocol.Event{{Event: "done", Data: delta.StopReason}}, nil
	default:
		return nil, nil
	}
}
func (testStreamEncoder) FormatDone(llm.Usage) ([]protocol.Event, error) {
	return []protocol.Event{{Data: "[DONE]"}}, nil
}

type runtimeSource struct {
	runtime  *llmruntime.Runtime
	acquires atomic.Int64
	releases atomic.Int64
}

func (source *runtimeSource) Acquire() (*llmruntime.Runtime, func(), bool) {
	source.acquires.Add(1)
	if source.runtime == nil {
		return nil, nil, false
	}
	return source.runtime, func() { source.releases.Add(1) }, true
}

type providerTransportFunc func(context.Context, provider.Request) (*provider.Response, error)

func (fn providerTransportFunc) Do(ctx context.Context, request provider.Request) (*provider.Response, error) {
	return fn(ctx, request)
}
func (providerTransportFunc) CloseIdleConnections() {}

func newTestRuntime(t *testing.T, catalog *protocol.Catalog, maxBodyBytes string) *llmruntime.Runtime {
	t.Helper()
	var builder configsnapshot.Builder
	if maxBodyBytes != "" {
		builder.SetSetting("proxy.max_body_bytes", maxBodyBytes)
	}
	providers, err := provider.NewCatalog(provider.Generic())
	if err != nil {
		t.Fatalf("provider catalog: %v", err)
	}
	runtime, err := llmruntime.New(llmruntime.Config{
		Snapshot:  builder.Build(),
		Protocols: catalog,
		Providers: providers,
		Transport: providerTransportFunc(func(context.Context, provider.Request) (*provider.Response, error) {
			return nil, errors.New("unexpected provider call")
		}),
	})
	if err != nil {
		t.Fatalf("runtime: %v", err)
	}
	return runtime
}

func mustCatalog(t *testing.T, codecs ...protocol.IngressCodec) *protocol.Catalog {
	t.Helper()
	catalog, err := protocol.NewCatalog(codecs, nil)
	if err != nil {
		t.Fatalf("protocol catalog: %v", err)
	}
	return catalog
}

func TestNewInstallsOnlyExplicitCatalogRoutesAndUnavailableRuntimeUsesCodec(t *testing.T) {
	codec := testIngressCodec{
		endpoint: protocol.OpenAIChatCompletionsV1,
		routes:   []protocol.IngressRoute{{Method: http.MethodPost, Pattern: "/explicit"}},
	}
	source := &runtimeSource{}
	handler, err := New(mustCatalog(t, codec), source, Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	explicit := httptest.NewRecorder()
	handler.ServeHTTP(explicit, httptest.NewRequest(http.MethodPost, "/explicit", strings.NewReader(`{}`)))
	if explicit.Code != http.StatusServiceUnavailable || explicit.Body.String() != "native-error:LLM runtime is unavailable" {
		t.Fatalf("explicit route = %d %q, want protocol-native 503", explicit.Code, explicit.Body.String())
	}
	if source.acquires.Load() != 1 {
		t.Fatalf("Acquire calls = %d, want 1", source.acquires.Load())
	}

	omitted := httptest.NewRecorder()
	handler.ServeHTTP(omitted, httptest.NewRequest(http.MethodPost, "/not-composed", strings.NewReader(`{}`)))
	if omitted.Code != http.StatusNotFound {
		t.Fatalf("omitted route status = %d, want 404", omitted.Code)
	}
	if source.acquires.Load() != 1 {
		t.Fatalf("omitted route called RuntimeSource; Acquire calls = %d, want 1", source.acquires.Load())
	}
}

func TestNewRejectsDuplicateMethodPatternAcrossCatalogCodecs(t *testing.T) {
	first := testIngressCodec{
		endpoint: protocol.OpenAIChatCompletionsV1,
		routes:   []protocol.IngressRoute{{Method: "post", Pattern: "/duplicate"}},
	}
	second := testIngressCodec{
		endpoint: protocol.Endpoint{Protocol: protocol.ProtocolOpenAIResponses, Workload: llm.WorkloadChat, Version: "v2"},
		routes:   []protocol.IngressRoute{{Method: " POST ", Pattern: "/duplicate"}},
	}
	_, err := New(mustCatalog(t, first, second), &runtimeSource{}, Options{})
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("New duplicate routes error = %v, want duplicate diagnostic", err)
	}
}

func TestModelsUnavailableUsesOpenAIServerErrorType(t *testing.T) {
	codec := testIngressCodec{endpoint: protocol.OpenAIChatCompletionsV1}
	handler, err := New(mustCatalog(t, codec), &runtimeSource{}, Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/models", nil))

	wantBody := "{\"error\":{\"message\":\"LLM runtime is unavailable\",\"type\":\"server_error\"}}\n"
	if response.Code != http.StatusServiceUnavailable || response.Header().Get("Content-Type") != "application/json" || response.Body.String() != wantBody {
		t.Fatalf("models unavailable response = %d %q %q, want 503 application/json %q", response.Code, response.Header().Get("Content-Type"), response.Body.String(), wantBody)
	}
}

func TestHandlerUsesOneRuntimeForBodyLimitAndReleasesOnReadFailure(t *testing.T) {
	var decoded atomic.Bool
	codec := testIngressCodec{
		endpoint: protocol.OpenAIChatCompletionsV1,
		routes:   []protocol.IngressRoute{{Method: http.MethodPost, Pattern: "/limited"}},
		decode: func(protocol.IngressRequest) (*llm.ChatRequest, error) {
			decoded.Store(true)
			return nil, errors.New("decode should not run")
		},
	}
	catalog := mustCatalog(t, codec)
	source := &runtimeSource{runtime: newTestRuntime(t, catalog, "8")}
	handler, err := New(catalog, source, Options{})
	if err != nil {
		t.Fatal(err)
	}

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/limited", strings.NewReader("123456789")))
	if response.Code != http.StatusRequestEntityTooLarge || response.Body.String() != "native-error:request body too large" {
		t.Fatalf("limited response = %d %q, want native 413", response.Code, response.Body.String())
	}
	if decoded.Load() {
		t.Fatal("codec decoded a body rejected by the acquired Runtime's limit")
	}
	if source.acquires.Load() != 1 || source.releases.Load() != 1 {
		t.Fatalf("acquire/release calls = %d/%d, want 1/1", source.acquires.Load(), source.releases.Load())
	}
}

func TestSinkEncodesNonStreamResponseAndCanonicalError(t *testing.T) {
	codec := testIngressCodec{endpoint: protocol.OpenAIChatCompletionsV1}

	responseWriter := httptest.NewRecorder()
	responseSink := newSink(responseWriter, codec)
	if err := responseSink.SendResponse(context.Background(), &llm.ChatResponse{Content: "hello"}); err != nil {
		t.Fatalf("SendResponse: %v", err)
	}
	if responseWriter.Code != http.StatusCreated || responseWriter.Header().Get("Content-Type") != "application/test-response" || responseWriter.Body.String() != "response:hello" {
		t.Fatalf("response wire = %d %q %q", responseWriter.Code, responseWriter.Header().Get("Content-Type"), responseWriter.Body.String())
	}

	errorWriter := httptest.NewRecorder()
	errorSink := newSink(errorWriter, codec)
	canonical := llm.NewError(llm.ErrRateLimitError, "slow down").WithStatus(http.StatusTooManyRequests)
	if err := errorSink.SendError(context.Background(), canonical); err != nil {
		t.Fatalf("SendError: %v", err)
	}
	if errorWriter.Code != http.StatusTooManyRequests || errorWriter.Header().Get("Content-Type") != "application/test-error" || errorWriter.Body.String() != "native-error:slow down" {
		t.Fatalf("error wire = %d %q %q", errorWriter.Code, errorWriter.Header().Get("Content-Type"), errorWriter.Body.String())
	}
}

func TestSinkPassesRuntimeApprovedSameEndpointOpaqueErrorVerbatim(t *testing.T) {
	writer := httptest.NewRecorder()
	sink := newSink(writer, testIngressCodec{endpoint: protocol.OpenAIChatCompletionsV1})
	wire := protocol.WireResponse{
		Status:  http.StatusUnprocessableEntity,
		Headers: map[string]string{"Content-Type": "application/problem+json"},
		Body:    []byte(`{"provider":"verbatim"}`),
	}
	if err := sink.SendOpaque(context.Background(), wire); err != nil {
		t.Fatalf("SendOpaque: %v", err)
	}
	if writer.Code != wire.Status || writer.Header().Get("Content-Type") != wire.Headers["Content-Type"] || !bytes.Equal(writer.Body.Bytes(), wire.Body) {
		t.Fatalf("opaque wire = %d %q %q", writer.Code, writer.Header().Get("Content-Type"), writer.Body.Bytes())
	}
}

func TestSinkReencodesCrossProtocolCanonicalErrorWithoutRawWire(t *testing.T) {
	writer := httptest.NewRecorder()
	sink := newSink(writer, messages.NewIngress())
	providerError := llm.NewError(llm.ErrInvalidRequest, "invalid prompt").
		WithStatus(http.StatusBadRequest).
		WithRaw([]byte(`{"error":{"type":"openai-only"}}`))
	if err := sink.SendError(context.Background(), providerError); err != nil {
		t.Fatalf("SendError: %v", err)
	}
	want := `{"type":"error","error":{"type":"invalid_request_error","message":"invalid prompt"}}`
	if writer.Code != http.StatusBadRequest || writer.Body.String() != want {
		t.Fatalf("cross-protocol error = %d %q, want 400 %q", writer.Code, writer.Body.String(), want)
	}
}

type flushingWriter struct {
	header    http.Header
	status    int
	body      bytes.Buffer
	flushes   int
	failWrite bool
}

func (writer *flushingWriter) Header() http.Header {
	if writer.header == nil {
		writer.header = make(http.Header)
	}
	return writer.header
}
func (writer *flushingWriter) WriteHeader(status int) { writer.status = status }
func (writer *flushingWriter) Write(payload []byte) (int, error) {
	if writer.failWrite {
		return 0, io.ErrClosedPipe
	}
	return writer.body.Write(payload)
}
func (writer *flushingWriter) Flush() { writer.flushes++ }

func TestSinkSendDeltaReturnsSuccessOnlyAfterCompleteFrameAndFlush(t *testing.T) {
	codec := testIngressCodec{endpoint: protocol.OpenAIChatCompletionsV1, stream: testStreamEncoder{}}
	unframedWriter := &flushingWriter{}
	unframedSink := newSink(unframedWriter, codec)
	committed, err := unframedSink.SendDelta(context.Background(), &llm.UsageDelta{Usage: llm.Usage{TotalTokens: 3}})
	if err != nil {
		t.Fatalf("SendDelta unframed Usage: %v", err)
	}
	if committed || unframedWriter.status != 0 || unframedWriter.body.Len() != 0 || unframedWriter.flushes != 0 {
		t.Fatalf("unframed Usage committed/status/body/flush = %t/%d/%d/%d, want false/0/0/0", committed, unframedWriter.status, unframedWriter.body.Len(), unframedWriter.flushes)
	}

	writer := &flushingWriter{}
	sink := newSink(writer, codec)
	committed, err = sink.SendDelta(context.Background(), &llm.TextDelta{Text: "hello"})
	if err != nil {
		t.Fatalf("SendDelta: %v", err)
	}
	if !committed {
		t.Fatal("SendDelta committed = false, want true after event flush")
	}
	if writer.status != http.StatusOK || writer.flushes != 1 || writer.body.String() != "event: text\ndata: hello\n\n" {
		t.Fatalf("stream wire = status %d flushes %d body %q", writer.status, writer.flushes, writer.body.String())
	}

	failingWriter := &flushingWriter{failWrite: true}
	failingSink := newSink(failingWriter, codec)
	committed, err = failingSink.SendDelta(context.Background(), &llm.TextDelta{Text: "never-accepted"})
	if !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("failed first SendDelta error = %v, want closed pipe", err)
	}
	if committed {
		t.Fatal("failed first SendDelta committed = true, want false")
	}
	if failingWriter.flushes != 0 {
		t.Fatalf("failed first event flushes = %d, want 0", failingWriter.flushes)
	}
}

func TestSinkResetStreamAttemptDiscardsOnlyUncommittedEncoderState(t *testing.T) {
	writer := &flushingWriter{}
	sink := newSink(writer, testIngressCodec{endpoint: protocol.OpenAIChatCompletionsV1, stream: testStreamEncoder{}})
	committed, err := sink.SendDelta(context.Background(), &llm.UsageDelta{Usage: llm.Usage{TotalTokens: 3}})
	if err != nil || committed {
		t.Fatalf("unframed Usage committed/error = %t/%v, want false/nil", committed, err)
	}
	if sink.encoder == nil || sink.usage.TotalTokens != 3 {
		t.Fatalf("uncommitted encoder/usage = %T/%+v, want initialized attempt state", sink.encoder, sink.usage)
	}
	if err := sink.ResetStreamAttempt(); err != nil {
		t.Fatalf("ResetStreamAttempt: %v", err)
	}
	if sink.encoder != nil || sink.usage != (llm.Usage{}) || sink.terminated {
		t.Fatalf("reset encoder/usage/terminal = %T/%+v/%t", sink.encoder, sink.usage, sink.terminated)
	}

	committed, err = sink.SendDelta(context.Background(), &llm.TextDelta{Text: "visible"})
	if err != nil || !committed {
		t.Fatalf("visible Text committed/error = %t/%v, want true/nil", committed, err)
	}
	if err := sink.ResetStreamAttempt(); err == nil {
		t.Fatal("ResetStreamAttempt succeeded after client-visible stream commit")
	}
}

func TestSinkEncodesTerminalStreamErrorAndStops(t *testing.T) {
	writer := &flushingWriter{}
	sink := newSink(writer, testIngressCodec{endpoint: protocol.AnthropicMessagesV1, stream: testStreamEncoder{}})
	terminal := &llm.StreamErrorDelta{Error: llm.NewError(llm.ErrServerError, "provider failed")}
	committed, err := sink.SendDelta(context.Background(), terminal)
	if err != nil {
		t.Fatalf("SendDelta terminal error: %v", err)
	}
	if !committed {
		t.Fatal("terminal SendDelta committed = false, want true")
	}
	if writer.body.String() != "event: error\ndata: {\"type\":\"native_error\",\"message\":\"provider failed\"}\n\n" || writer.flushes != 1 {
		t.Fatalf("terminal stream = %q flushes %d", writer.body.String(), writer.flushes)
	}
	if _, err := sink.SendDelta(context.Background(), &llm.TextDelta{Text: "late"}); err == nil {
		t.Fatal("SendDelta after terminal error succeeded")
	}
}

func TestSinkHonorsClientCancellationBeforeWriting(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	writer := &flushingWriter{}
	sink := newSink(writer, testIngressCodec{endpoint: protocol.OpenAIChatCompletionsV1, stream: testStreamEncoder{}})
	committed, err := sink.SendDelta(ctx, &llm.TextDelta{Text: "late"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("SendDelta canceled error = %v, want context canceled", err)
	}
	if committed {
		t.Fatal("canceled SendDelta committed = true, want false")
	}
	if writer.status != 0 || writer.body.Len() != 0 || writer.flushes != 0 {
		t.Fatalf("canceled Sink wrote status/body/flush = %d/%d/%d", writer.status, writer.body.Len(), writer.flushes)
	}
}

var _ protocol.ChatIngressCodec = testIngressCodec{}
var _ llmruntime.Sink = (*httpSink)(nil)
