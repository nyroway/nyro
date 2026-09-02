package httptransport

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nyroway/nyro/go/internal/llm/provider"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestTransportConvertsProviderRequestAndResponse(t *testing.T) {
	t.Parallel()
	transport, err := New(Config{
		RequestTimeout: time.Second,
		RoundTripper: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			body, err := io.ReadAll(request.Body)
			if err != nil {
				t.Fatalf("ReadAll(request.Body): %v", err)
			}
			if request.Method != http.MethodPost || request.URL.String() != "https://example.com/v1/chat" {
				t.Fatalf("request = %s %s", request.Method, request.URL)
			}
			if got := request.Header.Get("Authorization"); got != "Bearer key" {
				t.Fatalf("Authorization = %q", got)
			}
			if got := string(body); got != `{"model":"test"}` {
				t.Fatalf("body = %q", got)
			}
			return &http.Response{
				StatusCode: http.StatusAccepted,
				Header:     http.Header{"X-Request-ID": []string{"request-1"}},
				Body:       io.NopCloser(strings.NewReader("accepted")),
			}, nil
		}),
	})
	if err != nil {
		t.Fatalf("New(): %v", err)
	}
	response, err := transport.Do(context.Background(), provider.Request{
		Method: http.MethodPost, URL: "https://example.com/v1/chat",
		Headers: map[string]string{"Authorization": "Bearer key"}, Body: []byte(`{"model":"test"}`),
	})
	if err != nil {
		t.Fatalf("Do(): %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("StatusCode = %d", response.StatusCode)
	}
	if got := response.Headers["X-Request-ID"]; len(got) != 1 || got[0] != "request-1" {
		t.Fatalf("X-Request-ID = %v", got)
	}
}

func TestTransportRejectsInvalidProviderRequest(t *testing.T) {
	t.Parallel()
	transport, err := New(Config{})
	if err != nil {
		t.Fatalf("New(): %v", err)
	}
	if _, err := transport.Do(context.Background(), provider.Request{Method: http.MethodPost, URL: "://invalid"}); err == nil {
		t.Fatal("Do() accepted an invalid URL")
	}
}

func TestTransportPropagatesContextCancellation(t *testing.T) {
	t.Parallel()
	transport, err := New(Config{RoundTripper: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		<-request.Context().Done()
		return nil, request.Context().Err()
	})})
	if err != nil {
		t.Fatalf("New(): %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = transport.Do(ctx, provider.Request{Method: http.MethodGet, URL: "https://example.com"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Do() error = %v, want context.Canceled", err)
	}
}

func TestNewBuildsTunedHTTPTransportAndConfiguresValidProxy(t *testing.T) {
	t.Parallel()
	transport, err := New(Config{ConnectTimeout: 10 * time.Second, ProxyURL: "http://proxy.example:8080"})
	if err != nil {
		t.Fatalf("New(): %v", err)
	}
	httpTransport, ok := transport.client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("client transport = %T", transport.client.Transport)
	}
	if httpTransport.Proxy == nil {
		t.Fatal("valid proxy URL did not configure a proxy")
	}
	if httpTransport.MaxIdleConnsPerHost < 64 {
		t.Fatalf("MaxIdleConnsPerHost = %d, want at least 64", httpTransport.MaxIdleConnsPerHost)
	}
	if httpTransport.IdleConnTimeout == 0 {
		t.Fatal("IdleConnTimeout is not configured")
	}
}

func TestNewIgnoresInvalidProxyURL(t *testing.T) {
	t.Parallel()
	transport, err := New(Config{ProxyURL: "enabled"})
	if err != nil {
		t.Fatalf("New(): %v", err)
	}
	httpTransport := transport.client.Transport.(*http.Transport)
	if httpTransport.Proxy != nil {
		t.Fatal("invalid proxy URL configured a proxy")
	}
}

func TestNewCanUseEnvironmentProxyForAdminCompatibility(t *testing.T) {
	t.Parallel()
	transport, err := New(Config{UseEnvironmentProxy: true})
	if err != nil {
		t.Fatalf("New(): %v", err)
	}
	httpTransport := transport.client.Transport.(*http.Transport)
	if httpTransport.Proxy == nil {
		t.Fatal("UseEnvironmentProxy did not configure ProxyFromEnvironment")
	}
}
