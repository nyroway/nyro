package otlphttp_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	collectlogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	collectmetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	collecttrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	statuspb "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/nyroway/nyro/go/infra/observe"
	"github.com/nyroway/nyro/go/infra/observe/otlphttp"
)

type recordingStore struct {
	mu       sync.Mutex
	requests []observe.ExportRequest
	err      error
}

func (s *recordingStore) Append(_ context.Context, requests []observe.ExportRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, requests...)
	return s.err
}

func (s *recordingStore) QueryLogs(context.Context, observe.LogQuery) (observe.LogPage, error) {
	return observe.LogPage{}, nil
}

func (s *recordingStore) QuerySpans(context.Context, observe.SpanQuery) (observe.SpanPage, error) {
	return observe.SpanPage{}, nil
}

func (s *recordingStore) QueryMetrics(context.Context, observe.MetricQuery) (observe.MetricPage, error) {
	return observe.MetricPage{}, nil
}

func (s *recordingStore) DeleteBefore(context.Context, observe.Signal, time.Time, int) (int64, error) {
	return 0, nil
}

func (s *recordingStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.requests)
}

func TestReceiverAcceptsThreeStandardProtobufPaths(t *testing.T) {
	store := &recordingStore{}
	receiver, err := otlphttp.New(otlphttp.Options{Store: store, FlushInterval: time.Millisecond})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = receiver.Shutdown(context.Background()) })
	tests := []struct {
		path    string
		message proto.Message
	}{
		{"/v1/logs", &collectlogs.ExportLogsServiceRequest{}},
		{"/v1/metrics", &collectmetrics.ExportMetricsServiceRequest{}},
		{"/v1/traces", &collecttrace.ExportTraceServiceRequest{}},
	}
	for _, test := range tests {
		body, _ := proto.Marshal(test.message)
		request := httptest.NewRequest(http.MethodPost, test.path, bytes.NewReader(body))
		request.Header.Set("Content-Type", "application/x-protobuf")
		response := httptest.NewRecorder()
		receiver.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s status = %d, body = %x", test.path, response.Code, response.Body.Bytes())
		}
		if response.Header().Get("Content-Type") != "application/x-protobuf" {
			t.Fatalf("%s content type = %q", test.path, response.Header().Get("Content-Type"))
		}
	}
	waitFor(t, time.Second, func() bool { return store.count() == 3 })
}

func TestReceiverSupportsGzip(t *testing.T) {
	store := &recordingStore{}
	receiver, err := otlphttp.New(otlphttp.Options{Store: store, FlushInterval: time.Millisecond})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = receiver.Shutdown(context.Background()) })
	payload, _ := proto.Marshal(&collectlogs.ExportLogsServiceRequest{})
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	_, _ = writer.Write(payload)
	_ = writer.Close()
	request := httptest.NewRequest(http.MethodPost, "/v1/logs", &compressed)
	request.Header.Set("Content-Type", "application/x-protobuf")
	request.Header.Set("Content-Encoding", "gzip")
	response := httptest.NewRecorder()
	receiver.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %x", response.Code, response.Body.Bytes())
	}
}

func TestReceiverRejectsInvalidHTTPContractsWithProtobufStatus(t *testing.T) {
	receiver, err := otlphttp.New(otlphttp.Options{Store: &recordingStore{}, MaxRequestBytes: 4})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = receiver.Shutdown(context.Background()) })
	tests := []struct {
		name        string
		method      string
		path        string
		contentType string
		body        []byte
		want        int
	}{
		{"method", http.MethodGet, "/v1/logs", "application/x-protobuf", nil, http.StatusMethodNotAllowed},
		{"content type", http.MethodPost, "/v1/logs", "application/json", nil, http.StatusUnsupportedMediaType},
		{"malformed protobuf", http.MethodPost, "/v1/logs", "application/x-protobuf", []byte{0xff}, http.StatusBadRequest},
		{"too large", http.MethodPost, "/v1/logs", "application/x-protobuf", bytes.Repeat([]byte{1}, 5), http.StatusRequestEntityTooLarge},
		{"encoding", http.MethodPost, "/v1/logs", "application/x-protobuf", nil, http.StatusOK},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.path, bytes.NewReader(test.body))
			request.Header.Set("Content-Type", test.contentType)
			if test.name == "encoding" {
				request.Header.Set("Content-Encoding", "br")
				test.want = http.StatusUnsupportedMediaType
			}
			response := httptest.NewRecorder()
			receiver.Handler().ServeHTTP(response, request)
			if response.Code != test.want {
				t.Fatalf("status = %d, want %d, body = %x", response.Code, test.want, response.Body.Bytes())
			}
			if response.Code != http.StatusOK {
				status := &statuspb.Status{}
				if err := proto.Unmarshal(response.Body.Bytes(), status); err != nil || status.GetMessage() == "" {
					t.Fatalf("error response is not google.rpc.Status: %x, %v", response.Body.Bytes(), err)
				}
			}
		})
	}
}

func waitFor(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition was not met before timeout")
}
