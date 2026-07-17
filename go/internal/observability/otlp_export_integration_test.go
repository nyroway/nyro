package observability

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	otellog "go.opentelemetry.io/otel/log"
)

// TestOTLPLogsExportHitsV1LogsPath is the regression test for the endpoint-path
// bug: a base OTLP endpoint (scheme://host:port, no path — exactly what the
// admin seeds) must make the logs exporter POST to /v1/logs, not "/". It builds
// the real provider via NewProvider (the same path the gateway uses) pointed at
// a recording server and asserts the path the exporter actually hits.
func TestOTLPLogsExportHitsV1LogsPath(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(http.StatusOK) // empty ExportLogsServiceResponse body is valid
	}))
	defer srv.Close()

	prov, err := NewProvider(context.Background(), ObsConfig{
		Logs: SignalConfig{Kind: ExporterKindOTLP, Params: map[string]string{"endpoint": srv.URL}},
	})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}

	var rec otellog.Record
	rec.SetTimestamp(time.Now())
	rec.SetBody(otellog.StringValue("audit"))
	prov.Logger.Emit(context.Background(), rec)

	// Shutdown flushes the batch processor synchronously.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := prov.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(paths) == 0 {
		t.Fatal("exporter never POSTed to the server")
	}
	for _, p := range paths {
		if p != "/v1/logs" {
			t.Errorf("exporter POSTed to %q; want /v1/logs (base-URL endpoint must append the signal path)", p)
		}
	}
}

// TestOTLPLogsHonorsExportInterval proves the configured "interval" (the WebUI
// Export Interval) is wired into the logs BatchProcessor — previously it was
// dropped and logs always used the SDK's 1s default. With a 120ms interval and
// NO Shutdown call, the export must arrive well before 1s; if the interval were
// ignored, first receipt would be ~1s (the default) and this would fail.
func TestOTLPLogsHonorsExportInterval(t *testing.T) {
	got := make(chan time.Duration, 1)
	start := time.Now()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case got <- time.Since(start):
		default:
		}
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	prov, err := NewProvider(context.Background(), ObsConfig{
		Logs: SignalConfig{Kind: ExporterKindOTLP, Params: map[string]string{"endpoint": srv.URL, "interval": "120ms"}},
	})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	defer func() { _ = prov.Shutdown(context.Background()) }()

	var rec otellog.Record
	rec.SetTimestamp(time.Now())
	rec.SetBody(otellog.StringValue("audit"))
	start = time.Now()
	prov.Logger.Emit(context.Background(), rec)

	select {
	case elapsed := <-got:
		if elapsed > 700*time.Millisecond {
			t.Errorf("first export took %v; expected < 700ms (interval=120ms). The configured interval appears ignored (SDK default is 1s)", elapsed)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no export received within 2s")
	}
}
