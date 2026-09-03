package httptransport

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/nyroway/nyro/go/internal/llm/provider"
)

type Config struct {
	RequestTimeout time.Duration
	ConnectTimeout time.Duration
	ProxyURL       string
	// UseEnvironmentProxy preserves net/http's default proxy behavior for
	// callers that historically used http.DefaultTransport.
	UseEnvironmentProxy bool
	RoundTripper        http.RoundTripper
}

type Transport struct {
	client *http.Client
}

func New(config Config) (*Transport, error) {
	return newWithDialContext(config, (&net.Dialer{}).DialContext)
}

type dialContextFunc func(context.Context, string, string) (net.Conn, error)

func newWithDialContext(config Config, dial dialContextFunc) (*Transport, error) {
	roundTripper := config.RoundTripper
	if roundTripper == nil {
		transport := &http.Transport{
			DialContext:         withConnectTimeout(config.ConnectTimeout, dial),
			MaxIdleConns:        256,
			MaxIdleConnsPerHost: 64,
			IdleConnTimeout:     90 * time.Second,
			ForceAttemptHTTP2:   true,
		}
		if parsed := validProxyURL(config.ProxyURL); parsed != nil {
			transport.Proxy = http.ProxyURL(parsed)
		} else if config.UseEnvironmentProxy {
			transport.Proxy = http.ProxyFromEnvironment
		}
		roundTripper = transport
	}
	return &Transport{client: &http.Client{Timeout: config.RequestTimeout, Transport: roundTripper}}, nil
}

func withConnectTimeout(timeout time.Duration, dial dialContextFunc) dialContextFunc {
	if timeout == 0 {
		return dial
	}
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		return dial(ctx, network, address)
	}
}

func (transport *Transport) Do(ctx context.Context, request provider.Request) (*provider.Response, error) {
	httpRequest, err := http.NewRequestWithContext(ctx, request.Method, request.URL, bytes.NewReader(request.Body))
	if err != nil {
		return nil, err
	}
	for key, value := range request.Headers {
		httpRequest.Header.Set(key, value)
	}
	httpResponse, err := transport.client.Do(httpRequest)
	if err != nil {
		return nil, err
	}
	return &provider.Response{
		StatusCode: httpResponse.StatusCode,
		Headers:    httpResponse.Header.Clone(),
		Body:       httpResponse.Body,
	}, nil
}

func (transport *Transport) CloseIdleConnections() {
	if transport != nil && transport.client != nil {
		transport.client.CloseIdleConnections()
	}
}

func validProxyURL(value string) *url.URL {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil
	}
	return parsed
}

// NormalizeProxyURL returns a trimmed valid absolute proxy URL, or an empty
// string when the value must use direct transport behavior.
func NormalizeProxyURL(value string) string {
	if parsed := validProxyURL(value); parsed != nil {
		return parsed.String()
	}
	return ""
}

var _ provider.Transport = (*Transport)(nil)
