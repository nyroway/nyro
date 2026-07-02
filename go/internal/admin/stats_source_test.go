package admin

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/nyroway/nyro/go/internal/observability"
	"github.com/nyroway/nyro/go/internal/observability/parquet"
	"github.com/nyroway/nyro/go/internal/storage/memory"
)

// newStatsEngine mounts the admin API with a parquet-backed StatsSource rooted
// at obsDir. Mirrors newEngine but injects the real parquetStatsSource so
// /stats/* reads metrics parquet.
func newStatsEngine(t *testing.T, obsDir string) chi.Router {
	t.Helper()
	r := chi.NewRouter()
	Mount(r, memory.New().Storage(), "", nil, NewParquetStatsSource(obsDir))
	return r
}

// flushMetrics writes MetricSamples into the parquet metrics dir and flushes so
// the reader can see them (the sink otherwise only flushes at maxRows/hour).
func flushMetrics(t *testing.T, obsDir string, rows ...observability.MetricSample) {
	t.Helper()
	sink, err := parquet.NewSink[observability.MetricSample](obsDir, "metrics", 50000)
	if err != nil {
		t.Fatal(err)
	}
	if err := sink.Write(rows); err != nil {
		t.Fatal(err)
	}
	if err := sink.Flush(); err != nil {
		t.Fatal(err)
	}
}

// metricLabelsJSON builds a labels_json blob matching the receiver's shape
// (the keys AggregateStats parses: model/provider/apikey/status_class/direction).
func metricLabelsJSON(model, provider, apikey, statusClass, direction string) string {
	b, _ := json.Marshal(map[string]string{
		"model":        model,
		"provider":     provider,
		"apikey":       apikey,
		"status_class": statusClass,
		"direction":    direction,
	})
	return string(b)
}

// TestStatsParquetRead asserts that when metrics parquet has samples, /stats/*
// reads them. Seeds parquet with 3 requests for gpt-4o + tokens; expects
// /stats/overview counts and /stats/models row.
func TestStatsParquetRead(t *testing.T) {
	obsDir := t.TempDir()
	r := newStatsEngine(t, obsDir)

	// Production writes MetricSample.Ts as unix-nanoseconds (OTLP
	// p.GetTimeUnixNano), so the tests seed nanos to exercise the real path.
	now := time.Now().UnixNano()
	// 3 request counters (2x 2xx, 1x 5xx) for gpt-4o/openai/k1; tokens in=100/out=200.
	reqLabels := metricLabelsJSON("gpt-4o", "openai", "k1", "2xx", "")
	errLabels := metricLabelsJSON("gpt-4o", "openai", "k1", "5xx", "")
	flushMetrics(t, obsDir,
		observability.MetricSample{Ts: now, Name: "nyro_requests_total", Kind: "counter", Value: 1, LabelsJSON: reqLabels},
		observability.MetricSample{Ts: now, Name: "nyro_requests_total", Kind: "counter", Value: 1, LabelsJSON: reqLabels},
		observability.MetricSample{Ts: now, Name: "nyro_requests_total", Kind: "counter", Value: 1, LabelsJSON: errLabels},
		observability.MetricSample{Ts: now, Name: "nyro_tokens_total", Kind: "counter", Value: 100, LabelsJSON: metricLabelsJSON("gpt-4o", "openai", "k1", "", "in")},
		observability.MetricSample{Ts: now, Name: "nyro_tokens_total", Kind: "counter", Value: 200, LabelsJSON: metricLabelsJSON("gpt-4o", "openai", "k1", "", "out")},
	)

	// /stats/overview → 3 requests, 1 error, in=100, out=200.
	rec := do(r, "GET", "/api/v1/stats/overview", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("/stats/overview → %d %s", rec.Code, rec.Body.String())
	}
	var ov observability.StatsOverview
	if err := json.Unmarshal(rec.Body.Bytes(), &ov); err != nil {
		t.Fatal(err)
	}
	if ov.TotalRequests != 3 {
		t.Errorf("TotalRequests=%d want 3", ov.TotalRequests)
	}
	if ov.ErrorCount != 1 {
		t.Errorf("ErrorCount=%d want 1", ov.ErrorCount)
	}
	if ov.TotalInputTokens != 100 {
		t.Errorf("TotalInputTokens=%d want 100", ov.TotalInputTokens)
	}
	if ov.TotalOutputTokens != 200 {
		t.Errorf("TotalOutputTokens=%d want 200", ov.TotalOutputTokens)
	}

	// /stats/models → one row for gpt-4o with request_count=3.
	rec = do(r, "GET", "/api/v1/stats/models", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("/stats/models → %d %s", rec.Code, rec.Body.String())
	}
	var models []observability.ModelStats
	if err := json.Unmarshal(rec.Body.Bytes(), &models); err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].Model != "gpt-4o" || models[0].RequestCount != 3 {
		t.Errorf("/stats/models unexpected: %+v", models)
	}
}

// TestStatsHoursWindow asserts the ?hours window filters old samples out. It
// seeds MetricSample.Ts in unix-nanoseconds (the production write unit from OTLP
// p.GetTimeUnixNano); if the per-row cutoff were accidentally in millis, the
// 48h-old sample would survive the ?hours=1 filter and legacy-old would appear.
// Also asserts the /stats/api-keys LastUsedAt is returned in millis (not nanos).
func TestStatsHoursWindow(t *testing.T) {
	obsDir := t.TempDir()
	r := newStatsEngine(t, obsDir)

	// Seed in nanoseconds — matches the receiver's p.GetTimeUnixNano write path.
	now := time.Now().UnixNano()
	old := now - 48*60*60*1_000_000_000 // 48h ago (nanoseconds)
	// recent: gpt-4o/k1; old: legacy-old (48h old), under a different api key.
	flushMetrics(t, obsDir,
		observability.MetricSample{Ts: now, Name: "nyro_requests_total", Kind: "counter", Value: 1, LabelsJSON: metricLabelsJSON("gpt-4o", "openai", "k1", "2xx", "")},
		observability.MetricSample{Ts: old, Name: "nyro_requests_total", Kind: "counter", Value: 1, LabelsJSON: metricLabelsJSON("legacy-old", "openai", "k-old", "2xx", "")},
	)

	rec := do(r, "GET", "/api/v1/stats/models?hours=1", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("/stats/models?hours=1 → %d %s", rec.Code, rec.Body.String())
	}
	var models []observability.ModelStats
	if err := json.Unmarshal(rec.Body.Bytes(), &models); err != nil {
		t.Fatal(err)
	}
	for _, m := range models {
		if m.Model == "legacy-old" {
			t.Errorf("?hours=1 should exclude legacy-old; models=%+v", models)
		}
	}

	// /stats/api-keys?hours=1 → only k1 survives; its LastUsedAt must be in the
	// millisecond range (the WebUI contract), NOT nanoseconds. A raw-nano value
	// (~1.7e18) would land ~1e6x too big.
	rec = do(r, "GET", "/api/v1/stats/api-keys?hours=1", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("/stats/api-keys?hours=1 → %d %s", rec.Code, rec.Body.String())
	}
	var keys []observability.ApiKeyStats
	if err := json.Unmarshal(rec.Body.Bytes(), &keys); err != nil {
		t.Fatal(err)
	}
	var k1 *observability.ApiKeyStats
	for i := range keys {
		if keys[i].APIKeyID == "k1" {
			k1 = &keys[i]
		}
	}
	if k1 == nil {
		t.Fatalf("k1 not in api-keys response (hours=1); keys=%+v", keys)
	}
	if k1.LastUsedAt < 1_700_000_000_000 || k1.LastUsedAt > 1_700_000_000_000_000 {
		t.Errorf("LastUsedAt=%d is not in the unix-millisecond range; want ~%d (millis)", k1.LastUsedAt, time.Now().UnixMilli())
	}
	if delta := k1.LastUsedAt - time.Now().UnixMilli(); delta < -60_000 || delta > 5_000 {
		t.Errorf("LastUsedAt=%d drifts >60s from now(ms)=%d", k1.LastUsedAt, time.Now().UnixMilli())
	}
}
