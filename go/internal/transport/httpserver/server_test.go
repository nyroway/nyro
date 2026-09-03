package httpserver

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewMountsHandlersAndOwnsHealthReadiness(t *testing.T) {
	var ready atomic.Bool
	server := New(Options{
		Addr:  "127.0.0.1:0",
		Ready: ready.Load,
	}, Handler{
		Pattern: "/",
		Handler: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			writer.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(writer, request.URL.Path)
		}),
	})

	assertResponse := func(path string, wantStatus int, wantBody string) {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, path, nil)
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, request)
		if response.Code != wantStatus || !strings.Contains(response.Body.String(), wantBody) {
			t.Fatalf("GET %s = %d %q, want %d containing %q", path, response.Code, response.Body.String(), wantStatus, wantBody)
		}
	}

	assertResponse("/healthz", http.StatusOK, `"status":"ok"`)
	assertResponse("/readyz", http.StatusServiceUnavailable, `"status":"unready"`)
	ready.Store(true)
	assertResponse("/readyz", http.StatusOK, `"status":"ready"`)
	assertResponse("/models", http.StatusAccepted, "/models")
}

func TestServerRecoversMountedHandlerPanics(t *testing.T) {
	server := New(Options{}, Handler{
		Pattern: "/",
		Handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			panic("boom")
		}),
	})
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/panic", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("panic response status = %d, want 500", response.Code)
	}
}

func TestServerRepanicsCommittedHandlerPanicWithoutAppendingError(t *testing.T) {
	panicValue := errors.New("committed handler panic")
	server := New(Options{}, Handler{
		Pattern: "/",
		Handler: http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(writer, "committed body")
			panic(panicValue)
		}),
	})
	response := httptest.NewRecorder()

	recovered := capturePanic(func() {
		server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/panic", nil))
	})

	if recovered != panicValue {
		t.Fatalf("recovered panic = %#v, want original committed panic", recovered)
	}
	if response.Code != http.StatusAccepted || response.Body.String() != "committed body" {
		t.Fatalf("committed response = %d %q, want unpolluted 202 body", response.Code, response.Body.String())
	}
}

func TestServerRepanicsErrAbortHandler(t *testing.T) {
	server := New(Options{}, Handler{
		Pattern: "/",
		Handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			panic(http.ErrAbortHandler)
		}),
	})
	response := httptest.NewRecorder()

	recovered := capturePanic(func() {
		server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/abort", nil))
	})

	if recovered != http.ErrAbortHandler {
		t.Fatalf("recovered panic = %#v, want http.ErrAbortHandler", recovered)
	}
	if response.Body.Len() != 0 {
		t.Fatalf("abort response body = %q, want empty", response.Body.String())
	}
}

type capabilityWriter struct {
	header    http.Header
	status    int
	body      strings.Builder
	flushes   int
	hijacks   int
	pushes    int
	hijackErr error
}

func (writer *capabilityWriter) Header() http.Header { return writer.header }
func (writer *capabilityWriter) WriteHeader(status int) {
	if writer.status == 0 {
		writer.status = status
	}
}
func (writer *capabilityWriter) Write(payload []byte) (int, error) {
	if writer.status == 0 {
		writer.status = http.StatusOK
	}
	return writer.body.Write(payload)
}
func (writer *capabilityWriter) Flush() {
	if writer.status == 0 {
		writer.status = http.StatusOK
	}
	writer.flushes++
}
func (writer *capabilityWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	writer.hijacks++
	return nil, nil, writer.hijackErr
}
func (writer *capabilityWriter) Push(string, *http.PushOptions) error {
	writer.pushes++
	return nil
}

func TestRecoveryPreservesResponseWriterCapabilitiesAndFlushCommit(t *testing.T) {
	panicValue := errors.New("panic after flush")
	hijackErr := errors.New("test hijack")
	underlying := &capabilityWriter{header: make(http.Header), hijackErr: hijackErr}
	var hasFlusher, hasHijacker, hasPusher, hasUnwrap bool
	server := New(Options{}, Handler{
		Pattern: "/",
		Handler: http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			_, hasFlusher = writer.(http.Flusher)
			hijacker, ok := writer.(http.Hijacker)
			hasHijacker = ok
			if ok {
				_, _, _ = hijacker.Hijack()
			}
			pusher, ok := writer.(http.Pusher)
			hasPusher = ok
			if ok {
				_ = pusher.Push("/asset", nil)
			}
			unwrapper, ok := writer.(interface{ Unwrap() http.ResponseWriter })
			hasUnwrap = ok && unwrapper.Unwrap() == underlying
			if err := http.NewResponseController(writer).Flush(); err != nil {
				t.Errorf("ResponseController.Flush: %v", err)
			}
			panic(panicValue)
		}),
	})

	recovered := capturePanic(func() {
		server.Handler().ServeHTTP(underlying, httptest.NewRequest(http.MethodGet, "/flush", nil))
	})

	if !hasFlusher || !hasHijacker || !hasPusher || !hasUnwrap {
		t.Fatalf("wrapped capabilities flusher/hijacker/pusher/unwrap = %t/%t/%t/%t, want all true", hasFlusher, hasHijacker, hasPusher, hasUnwrap)
	}
	if underlying.flushes != 1 || underlying.hijacks != 1 || underlying.pushes != 1 {
		t.Fatalf("forwarded flush/hijack/push = %d/%d/%d, want 1/1/1", underlying.flushes, underlying.hijacks, underlying.pushes)
	}
	if recovered != panicValue {
		t.Fatalf("recovered panic after flush = %#v, want original panic", recovered)
	}
	if underlying.body.Len() != 0 {
		t.Fatalf("body after flushed panic = %q, want no appended 500", underlying.body.String())
	}
}

func capturePanic(run func()) (recovered any) {
	defer func() { recovered = recover() }()
	run()
	return nil
}

func TestServerServeAndShutdownOwnsHTTPServerLifecycle(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := New(Options{}, Handler{
		Pattern: "/",
		Handler: http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.WriteHeader(http.StatusNoContent)
		}),
	})
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()

	client := &http.Client{Timeout: time.Second}
	response, err := client.Get("http://" + listener.Addr().String() + "/")
	if err != nil {
		t.Fatalf("GET mounted handler: %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("GET mounted handler status = %d, want 204", response.StatusCode)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("Serve after Shutdown = %v, want nil", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Serve did not return after Shutdown")
	}
}
