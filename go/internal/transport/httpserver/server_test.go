package httpserver

import (
	"context"
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
