package admin

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/nyroway/nyro/go/internal/observability"
	"github.com/nyroway/nyro/go/internal/observability/parquet"
	"github.com/nyroway/nyro/go/internal/storage"
	"github.com/nyroway/nyro/go/internal/storage/memory"
)

// newStatsEngine mounts the admin API with a parquet-backed StatsSource rooted
// at obsDir (and the old request_logs table as fallback). Mirrors newEngine but
// injects the real parquetStatsSource so /stats/* reads metrics parquet.
func newStatsEngine(t *testing.T, obsDir string) (chi.Router, *memory.Backend) {
	t.Helper()
	st := memory.New()
	r := chi.NewRouter()
	Mount(r, st.Storage(), "", nil, NewParquetStatsSource(obsDir, st.Storage()))
	return r, st
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
// reads the new path (not the old table). Seeds parquet with 3 requests for
// gpt-4o + tokens; expects /stats/overview counts and /stats/models row.
func TestStatsParquetRead(t *testing.T) {
	obsDir := t.TempDir()
	r, _ := newStatsEngine(t, obsDir)

	now := time.Now().UnixMilli()
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
	var ov storage.StatsOverview
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
	var models []storage.ModelStats
	if err := json.Unmarshal(rec.Body.Bytes(), &models); err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].Model != "gpt-4o" || models[0].RequestCount != 3 {
		t.Errorf("/stats/models unexpected: %+v", models)
	}
}

// TestStatsParquetWinsOverOldTable asserts that when BOTH parquet and the old
// table have data, the parquet path is used (the new path takes precedence
// during dual-write). Seeds a different count in the old table and checks the
// parquet value comes through.
func TestStatsParquetWinsOverOldTable(t *testing.T) {
	obsDir := t.TempDir()
	r, st := newStatsEngine(t, obsDir)

	now := time.Now().UnixMilli()
	// Parquet: 2 requests for gpt-4o.
	flushMetrics(t, obsDir,
		observability.MetricSample{Ts: now, Name: "nyro_requests_total", Kind: "counter", Value: 1, LabelsJSON: metricLabelsJSON("gpt-4o", "openai", "k1", "2xx", "")},
		observability.MetricSample{Ts: now, Name: "nyro_requests_total", Kind: "counter", Value: 1, LabelsJSON: metricLabelsJSON("gpt-4o", "openai", "k1", "2xx", "")},
	)
	// Old table: 1 request for a DIFFERENT model (would show up only if fallback used).
	code200 := int32(200)
	if err := st.Logs().AppendBatch([]storage.RequestLog{
		{ID: "old-1", CreatedAt: now, ModelName: "legacy-only-model", ClientStatusCode: &code200},
	}); err != nil {
		t.Fatal(err)
	}

	rec := do(r, "GET", "/api/v1/stats/models", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("/stats/models → %d %s", rec.Code, rec.Body.String())
	}
	var models []storage.ModelStats
	if err := json.Unmarshal(rec.Body.Bytes(), &models); err != nil {
		t.Fatal(err)
	}
	for _, m := range models {
		if m.Model == "legacy-only-model" {
			t.Errorf("fallback was used though parquet had data; models=%+v", models)
		}
	}
	if len(models) != 1 || models[0].Model != "gpt-4o" || models[0].RequestCount != 2 {
		t.Errorf("expected parquet gpt-4o x2, got %+v", models)
	}
}

// TestStatsFallbackWhenParquetEmpty asserts the dual-write fallback: with an
// empty metrics parquet store, /stats/* reads the legacy request_logs table.
func TestStatsFallbackWhenParquetEmpty(t *testing.T) {
	obsDir := t.TempDir()
	r, st := newStatsEngine(t, obsDir)

	// Seed ONLY the old table; parquet is empty.
	now := time.Now().UnixMilli()
	code200 := int32(200)
	if err := st.Logs().AppendBatch([]storage.RequestLog{
		{ID: "old-1", CreatedAt: now, ModelName: "legacy-model", ProviderName: "openai", APIKeyID: "k1", APIKeyName: "key-one", InputTokens: 42, OutputTokens: 7, ClientStatusCode: &code200},
	}); err != nil {
		t.Fatal(err)
	}

	// /stats/models → the legacy model shows up via fallback.
	rec := do(r, "GET", "/api/v1/stats/models", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("/stats/models → %d %s", rec.Code, rec.Body.String())
	}
	var models []storage.ModelStats
	if err := json.Unmarshal(rec.Body.Bytes(), &models); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range models {
		if m.Model == "legacy-model" {
			found = true
		}
	}
	if !found {
		t.Errorf("fallback did not surface legacy-model; models=%+v", models)
	}

	// /stats/overview → fallback total_requests reflects the legacy row.
	rec = do(r, "GET", "/api/v1/stats/overview", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("/stats/overview → %d %s", rec.Code, rec.Body.String())
	}
	var ov storage.StatsOverview
	if err := json.Unmarshal(rec.Body.Bytes(), &ov); err != nil {
		t.Fatal(err)
	}
	if ov.TotalRequests != 1 || ov.TotalInputTokens != 42 {
		t.Errorf("fallback overview unexpected: %+v", ov)
	}
}

// TestStatsHoursWindow asserts the ?hours window filters old samples out.
func TestStatsHoursWindow(t *testing.T) {
	obsDir := t.TempDir()
	r, _ := newStatsEngine(t, obsDir)

	now := time.Now().UnixMilli()
	old := now - 48*60*60*1000 // 48h ago
	// recent: gpt-4o; old: legacy-old (48h old).
	flushMetrics(t, obsDir,
		observability.MetricSample{Ts: now, Name: "nyro_requests_total", Kind: "counter", Value: 1, LabelsJSON: metricLabelsJSON("gpt-4o", "openai", "k1", "2xx", "")},
		observability.MetricSample{Ts: old, Name: "nyro_requests_total", Kind: "counter", Value: 1, LabelsJSON: metricLabelsJSON("legacy-old", "openai", "k1", "2xx", "")},
	)

	rec := do(r, "GET", "/api/v1/stats/models?hours=1", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("/stats/models?hours=1 → %d %s", rec.Code, rec.Body.String())
	}
	var models []storage.ModelStats
	if err := json.Unmarshal(rec.Body.Bytes(), &models); err != nil {
		t.Fatal(err)
	}
	for _, m := range models {
		if m.Model == "legacy-old" {
			t.Errorf("?hours=1 should exclude legacy-old; models=%+v", models)
		}
	}
}
