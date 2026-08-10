package observability

import (
	"encoding/json"
	"testing"
	"time"
)

func metricLabelJSON(routeID, routeModel, upstreamID, upstreamName, consumerID, direction string, status int64) string {
	value, _ := json.Marshal(map[string]any{
		"nyro.route.id":             routeID,
		"nyro.route.model":          routeModel,
		"nyro.upstream.id":          upstreamID,
		"nyro.upstream.name":        upstreamName,
		"nyro.consumer.id":          consumerID,
		"http.response.status_code": status,
		"direction":                 direction,
	})
	return string(value)
}

func TestAggregateStatsUsesRouteUpstreamAndConsumerDimensions(t *testing.T) {
	now := time.Now().UnixNano()
	labels := metricLabelJSON("route-1", "gpt-4o", "upstream-1", "OpenAI", "consumer-1", "", 200)
	errorLabels := metricLabelJSON("route-1", "gpt-4o", "upstream-1", "OpenAI", "consumer-1", "", 503)
	samples := []MetricSample{
		{Ts: now, Name: "nyro_requests_total", Kind: "counter", Value: 2, LabelsJSON: labels},
		{Ts: now, Name: "nyro_requests_total", Kind: "counter", Value: 1, LabelsJSON: errorLabels},
		{Ts: now, Name: "nyro_tokens_total", Kind: "counter", Value: 100, LabelsJSON: metricLabelJSON("route-1", "gpt-4o", "", "", "consumer-1", "in", 0)},
		{Ts: now, Name: "nyro_tokens_total", Kind: "counter", Value: 200, LabelsJSON: metricLabelJSON("route-1", "gpt-4o", "", "", "consumer-1", "out", 0)},
		{Ts: now, Name: "nyro_request_latency_ms", Kind: "histogram", HistSum: 750, HistCount: 3, LabelsJSON: labels},
	}

	overview, routes, upstreams, consumers, err := AggregateStats(samples, 0)
	if err != nil {
		t.Fatalf("AggregateStats() error = %v", err)
	}
	if overview.TotalRequests != 3 || overview.ErrorCount != 1 || overview.TotalInputTokens != 100 || overview.TotalOutputTokens != 200 || overview.AvgDurationMs != 250 {
		t.Fatalf("overview = %+v", overview)
	}
	if len(routes) != 1 || routes[0].RouteID != "route-1" || routes[0].RouteModel != "gpt-4o" || routes[0].RequestCount != 3 || routes[0].TotalInputTokens != 100 || routes[0].TotalOutputTokens != 200 || routes[0].AvgDurationMs != 250 {
		t.Fatalf("route stats = %+v", routes)
	}
	if len(upstreams) != 1 || upstreams[0].UpstreamID != "upstream-1" || upstreams[0].UpstreamName != "OpenAI" || upstreams[0].RequestCount != 3 || upstreams[0].ErrorCount != 1 || upstreams[0].AvgDurationMs != 250 {
		t.Fatalf("upstream stats = %+v", upstreams)
	}
	if len(consumers) != 1 || consumers[0].ConsumerID != "consumer-1" || consumers[0].RequestCount != 3 || consumers[0].TotalInputTokens != 100 || consumers[0].TotalOutputTokens != 200 || consumers[0].LastUsedAt != now {
		t.Fatalf("consumer stats = %+v", consumers)
	}
}

func TestAggregateHourlyUsesExactHTTPStatus(t *testing.T) {
	at := time.Date(2026, time.August, 6, 3, 30, 0, 0, time.UTC)
	samples := []MetricSample{
		{Ts: at.UnixNano(), Name: "nyro_requests_total", Value: 1, LabelsJSON: metricLabelJSON("r", "gpt", "u", "OpenAI", "c", "", 429)},
		{Ts: at.UnixNano(), Name: "nyro_requests_total", Value: 1, LabelsJSON: metricLabelJSON("r", "gpt", "u", "OpenAI", "c", "", 204)},
		{Ts: at.UnixNano(), Name: "nyro_request_latency_ms", HistSum: 40, HistCount: 2, LabelsJSON: metricLabelJSON("r", "gpt", "u", "OpenAI", "c", "", 0)},
	}
	hourly, err := AggregateHourly(samples, 0)
	if err != nil {
		t.Fatalf("AggregateHourly() error = %v", err)
	}
	if len(hourly) != 1 || hourly[0].Hour != "2026-08-06T03:00:00Z" || hourly[0].RequestCount != 2 || hourly[0].ErrorCount != 1 || hourly[0].AvgDurationMs != 20 {
		t.Fatalf("hourly = %+v", hourly)
	}
}

func TestAggregateStatsOmitsUnknownDomainDimensions(t *testing.T) {
	now := time.Now().UnixNano()
	samples := []MetricSample{
		{Ts: now, Name: "nyro_requests_total", Value: 1, LabelsJSON: metricLabelJSON("route-1", "gpt-4o", "upstream-1", "OpenAI", "consumer-1", "", 200)},
		{Ts: now, Name: "nyro_requests_total", Value: 1, LabelsJSON: metricLabelJSON("", "", "", "", "", "", 404)},
	}
	overview, routes, upstreams, consumers, err := AggregateStats(samples, 0)
	if err != nil {
		t.Fatal(err)
	}
	if overview.TotalRequests != 2 || overview.ErrorCount != 1 {
		t.Fatalf("overview = %+v", overview)
	}
	if len(routes) != 1 || len(upstreams) != 1 || len(consumers) != 1 {
		t.Fatalf("routes=%+v upstreams=%+v consumers=%+v", routes, upstreams, consumers)
	}
}

func TestAggregateStatsOmitsP95WhenQuantileFallsInOverflowBucket(t *testing.T) {
	samples := []MetricSample{
		{
			Name:        "nyro_request_latency_ms",
			Kind:        "histogram",
			HistSum:     10_000,
			HistCount:   100,
			HistBounds:  []float64{50, 100, 250},
			HistBuckets: []uint64{1, 1, 1, 97},
		},
	}

	overview, _, _, _, err := AggregateStats(samples, 0)
	if err != nil {
		t.Fatalf("AggregateStats() error = %v", err)
	}
	if overview.P95DurationMs != nil {
		t.Fatalf("P95DurationMs = %v, want nil for +Inf overflow bucket", *overview.P95DurationMs)
	}
}
