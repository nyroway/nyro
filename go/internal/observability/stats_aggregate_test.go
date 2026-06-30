package observability

import (
	"encoding/json"
	"testing"
)

func sampleReq(model, provider, apikey, status string, n int) []MetricSample {
	var out []MetricSample
	for i := 0; i < n; i++ {
		out = append(out, MetricSample{
			Ts:   1, Name: "nyro_requests_total", Kind: "counter", Value: 1,
			LabelsJSON: labels(model, provider, apikey, status),
		})
	}
	return out
}

func labels(model, provider, apikey, status string) string {
	b, _ := json.Marshal(map[string]string{"model": model, "provider": provider, "apikey": apikey, "status_class": status})
	return string(b)
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
