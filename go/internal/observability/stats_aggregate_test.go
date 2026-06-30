package observability

import (
	"encoding/json"
	"testing"
)

func sampleReq(model, provider, apikey, status string, n int) []MetricSample {
	var out []MetricSample
	for i := 0; i < n; i++ {
		out = append(out, MetricSample{
			Ts: 1, Name: "nyro_requests_total", Kind: "counter", Value: 1,
			LabelsJSON: labels(model, provider, apikey, status),
		})
	}
	return out
}

func labels(model, provider, apikey, status string) string {
	b, _ := json.Marshal(map[string]string{"model": model, "provider": provider, "apikey": apikey, "status_class": status})
	return string(b)
}

func labelsDir(model, provider, apikey, direction string) string {
	b, _ := json.Marshal(map[string]string{"model": model, "provider": provider, "apikey": apikey, "direction": direction})
	return string(b)
}

// sampleTok returns n token samples of the given direction (in/out/cache_read).
func sampleTok(model, provider, apikey, direction string, val float64) MetricSample {
	return MetricSample{
		Ts: 1, Name: "nyro_tokens_total", Kind: "counter", Value: val,
		LabelsJSON: labelsDir(model, provider, apikey, direction),
	}
}

// sampleLat returns one latency histogram sample: histSum ms over histCount reqs.
func sampleLat(model, provider string, histSum float64, histCount int64) MetricSample {
	return MetricSample{
		Ts: 1, Name: "nyro_request_latency_ms", Kind: "histogram",
		HistSum: histSum, HistCount: histCount,
		LabelsJSON: labels(model, provider, "", ""),
	}
}

func TestAggregateStatsOverview(t *testing.T) {
	samples := append(sampleReq("gpt", "openai", "k1", "2xx", 5),
		sampleReq("gpt", "openai", "k1", "5xx", 2)...) // 7 requests, 2 errors
	ov, _, _, _, err := AggregateStats(samples, 0)
	if err != nil {
		t.Fatal(err)
	}
	if ov.TotalRequests != 7 {
		t.Errorf("TotalRequests=%d want 7", ov.TotalRequests)
	}
	if ov.ErrorCount != 2 {
		t.Errorf("ErrorCount=%d want 2", ov.ErrorCount)
	}
}

func TestAggregateStatsByModel(t *testing.T) {
	samples := append(sampleReq("gpt", "openai", "k1", "2xx", 3),
		sampleReq("claude", "anthropic", "k1", "2xx", 4)...)
	_, models, _, _, err := AggregateStats(samples, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 {
		t.Fatalf("want 2 model rows, got %d", len(models))
	}
}

// TestAggregateStatsTokens feeds nyro_tokens_total (in/out/cache_read) and
// asserts the sums appear in Overview, ApiKeyStats, and ModelStats.
func TestAggregateStatsTokens(t *testing.T) {
	samples := []MetricSample{
		// 3 requests so model/provider/key rows exist.
		{
			Ts: 1, Name: "nyro_requests_total", Kind: "counter", Value: 1,
			LabelsJSON: labels("gpt", "openai", "k1", "2xx"),
		},
		{
			Ts: 1, Name: "nyro_requests_total", Kind: "counter", Value: 1,
			LabelsJSON: labels("gpt", "openai", "k1", "2xx"),
		},
		{
			Ts: 1, Name: "nyro_requests_total", Kind: "counter", Value: 1,
			LabelsJSON: labels("gpt", "openai", "k1", "2xx"),
		},
		sampleTok("gpt", "openai", "k1", "in", 100),
		sampleTok("gpt", "openai", "k1", "out", 200),
		sampleTok("gpt", "openai", "k1", "cache_read", 50),
	}
	ov, models, _, keys, err := AggregateStats(samples, 0)
	if err != nil {
		t.Fatal(err)
	}
	if ov.TotalInputTokens != 100 {
		t.Errorf("Overview TotalInputTokens=%d want 100", ov.TotalInputTokens)
	}
	if ov.TotalOutputTokens != 200 {
		t.Errorf("Overview TotalOutputTokens=%d want 200", ov.TotalOutputTokens)
	}
	if len(keys) != 1 || keys[0].TotalInputTokens != 100 || keys[0].TotalOutputTokens != 200 || keys[0].CacheReadTokens != 50 {
		t.Errorf("ApiKeyStats unexpected: %+v", keys)
	}
	if len(models) != 1 || models[0].TotalInputTokens != 100 || models[0].TotalOutputTokens != 200 {
		t.Errorf("ModelStats unexpected: %+v", models)
	}
}

// TestAggregateStatsLatency feeds nyro_request_latency_ms and asserts
// AvgDurationMs > 0 on Overview, ModelStats, ProviderStats, and hourly.
func TestAggregateStatsLatency(t *testing.T) {
	samples := []MetricSample{
		{
			Ts: 1, Name: "nyro_requests_total", Kind: "counter", Value: 1,
			LabelsJSON: labels("gpt", "openai", "k1", "2xx"),
		},
		// latency: 500 ms summed over 2 observations → avg 250 ms.
		sampleLat("gpt", "openai", 500, 2),
	}
	ov, models, provs, _, err := AggregateStats(samples, 0)
	if err != nil {
		t.Fatal(err)
	}
	if ov.AvgDurationMs != 250 {
		t.Errorf("Overview AvgDurationMs=%v want 250", ov.AvgDurationMs)
	}
	if ov.AvgDurationMs <= 0 {
		t.Errorf("Overview AvgDurationMs must be > 0, got %v", ov.AvgDurationMs)
	}
	if len(models) != 1 || models[0].AvgDurationMs != 250 {
		t.Errorf("ModelStats AvgDurationMs unexpected: %+v", models)
	}
	if len(models) != 1 || models[0].AvgDurationMs <= 0 {
		t.Errorf("ModelStats AvgDurationMs must be > 0, got %+v", models)
	}
	if len(provs) != 1 || provs[0].AvgDurationMs != 250 {
		t.Errorf("ProviderStats AvgDurationMs unexpected: %+v", provs)
	}
	if len(provs) != 1 || provs[0].AvgDurationMs <= 0 {
		t.Errorf("ProviderStats AvgDurationMs must be > 0, got %+v", provs)
	}

	hourly, err := AggregateHourly(samples, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(hourly) != 1 || hourly[0].AvgDurationMs != 250 {
		t.Errorf("StatsHourly AvgDurationMs unexpected: %+v", hourly)
	}
	if len(hourly) != 1 || hourly[0].AvgDurationMs <= 0 {
		t.Errorf("StatsHourly AvgDurationMs must be > 0, got %+v", hourly)
	}
}

// TestAggregateStatsProviderKeyShape asserts ProviderStats and ApiKeyStats
// carry correct request/error counts.
func TestAggregateStatsProviderKeyShape(t *testing.T) {
	samples := append(sampleReq("gpt", "openai", "k1", "2xx", 4),
		sampleReq("gpt", "openai", "k1", "5xx", 1)...) // openai: 5 req, 1 err
	samples = append(samples, sampleReq("claude", "anthropic", "k2", "4xx", 2)...) // anthropic: 2 req, 2 err
	_, _, provs, keys, err := AggregateStats(samples, 0)
	if err != nil {
		t.Fatal(err)
	}
	pmap := map[string]ProviderStats{}
	for _, p := range provs {
		pmap[p.Provider] = p
	}
	if pmap["openai"].RequestCount != 5 || pmap["openai"].ErrorCount != 1 {
		t.Errorf("openai ProviderStats=%+v want req=5 err=1", pmap["openai"])
	}
	if pmap["anthropic"].RequestCount != 2 || pmap["anthropic"].ErrorCount != 2 {
		t.Errorf("anthropic ProviderStats=%+v want req=2 err=2", pmap["anthropic"])
	}
	kmap := map[string]ApiKeyStats{}
	for _, k := range keys {
		kmap[k.APIKeyID] = k
	}
	if kmap["k1"].RequestCount != 5 {
		t.Errorf("k1 ApiKeyStats RequestCount=%d want 5", kmap["k1"].RequestCount)
	}
	if kmap["k2"].RequestCount != 2 {
		t.Errorf("k2 ApiKeyStats RequestCount=%d want 2", kmap["k2"].RequestCount)
	}
}
