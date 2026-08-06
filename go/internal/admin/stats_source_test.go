package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	collectmetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	metricsv1 "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcev1 "go.opentelemetry.io/proto/otlp/resource/v1"

	infraobserve "github.com/nyroway/nyro/go/infra/observe"
	"github.com/nyroway/nyro/go/internal/observability"
)

func appendMetric(t *testing.T, store infraobserve.Store, at time.Time, metric *metricsv1.Metric) {
	t.Helper()
	request := &collectmetrics.ExportMetricsServiceRequest{ResourceMetrics: []*metricsv1.ResourceMetrics{{
		Resource:     &resourcev1.Resource{},
		ScopeMetrics: []*metricsv1.ScopeMetrics{{Metrics: []*metricsv1.Metric{metric}}},
	}}}
	if err := store.Append(context.Background(), []infraobserve.ExportRequest{{ReceivedAt: at, Metrics: request}}); err != nil {
		t.Fatal(err)
	}
}

func sumMetric(name string, at time.Time, value int64, attributes map[string]any) *metricsv1.Metric {
	return &metricsv1.Metric{Name: name, Data: &metricsv1.Metric_Sum{Sum: &metricsv1.Sum{
		AggregationTemporality: metricsv1.AggregationTemporality_AGGREGATION_TEMPORALITY_DELTA,
		DataPoints: []*metricsv1.NumberDataPoint{{
			TimeUnixNano: uint64(at.UnixNano()), Attributes: observeAttributes(attributes),
			Value: &metricsv1.NumberDataPoint_AsInt{AsInt: value},
		}},
	}}}
}

func TestStatsReadObserveMetrics(t *testing.T) {
	router, store := newObserveEngine(t)
	now := time.Now()
	labels := map[string]any{
		"nyro.route.id": "route-1", "nyro.route.model": "gpt-4o",
		"nyro.upstream.id": "upstream-1", "nyro.upstream.name": "OpenAI",
		"nyro.consumer.id": "consumer-1", "http.response.status_code": int64(200),
	}
	appendMetric(t, store, now, sumMetric("nyro_requests_total", now, 3, labels))
	errorLabels := map[string]any{
		"nyro.route.id": "route-1", "nyro.route.model": "gpt-4o",
		"nyro.upstream.id": "upstream-1", "nyro.upstream.name": "OpenAI",
		"nyro.consumer.id": "consumer-1", "http.response.status_code": int64(503),
	}
	appendMetric(t, store, now, sumMetric("nyro_requests_total", now, 1, errorLabels))
	appendMetric(t, store, now, sumMetric("nyro_tokens_total", now, 100, map[string]any{
		"nyro.route.id": "route-1", "nyro.route.model": "gpt-4o", "nyro.consumer.id": "consumer-1", "direction": "in",
	}))

	rec := do(router, "GET", "/api/v1/stats/overview", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("overview -> %d %s", rec.Code, rec.Body.String())
	}
	var overview observability.StatsOverview
	if err := json.Unmarshal(rec.Body.Bytes(), &overview); err != nil {
		t.Fatal(err)
	}
	if overview.TotalRequests != 4 || overview.ErrorCount != 1 || overview.TotalInputTokens != 100 {
		t.Fatalf("overview = %+v", overview)
	}

	rec = do(router, "GET", "/api/v1/stats/routes", "", nil)
	var routes []observability.RouteStats
	if err := json.Unmarshal(rec.Body.Bytes(), &routes); err != nil {
		t.Fatal(err)
	}
	if len(routes) != 1 || routes[0].RouteID != "route-1" || routes[0].RouteModel != "gpt-4o" || routes[0].RequestCount != 4 {
		t.Fatalf("routes = %+v", routes)
	}

	rec = do(router, "GET", "/api/v1/stats/upstreams", "", nil)
	var upstreams []observability.UpstreamStats
	if err := json.Unmarshal(rec.Body.Bytes(), &upstreams); err != nil {
		t.Fatal(err)
	}
	if len(upstreams) != 1 || upstreams[0].UpstreamID != "upstream-1" || upstreams[0].ErrorCount != 1 {
		t.Fatalf("upstreams = %+v", upstreams)
	}

	rec = do(router, "GET", "/api/v1/stats/consumers", "", nil)
	var consumers []observability.ConsumerStats
	if err := json.Unmarshal(rec.Body.Bytes(), &consumers); err != nil {
		t.Fatal(err)
	}
	if len(consumers) != 1 || consumers[0].ConsumerID != "consumer-1" || consumers[0].RequestCount != 4 || consumers[0].LastUsedAt < now.Add(-time.Second).UnixMilli() || consumers[0].LastUsedAt > now.Add(time.Second).UnixMilli() {
		t.Fatalf("consumers = %+v", consumers)
	}
}

func TestStatsHoursWindowExcludesOldObservePoints(t *testing.T) {
	router, store := newObserveEngine(t)
	now := time.Now()
	appendMetric(t, store, now, sumMetric("nyro_requests_total", now, 1, map[string]any{"nyro.route.id": "recent"}))
	old := now.Add(-48 * time.Hour)
	appendMetric(t, store, old, sumMetric("nyro_requests_total", old, 1, map[string]any{"nyro.route.id": "old"}))

	rec := do(router, "GET", "/api/v1/stats/routes?hours=1", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("routes -> %d %s", rec.Code, rec.Body.String())
	}
	var routes []observability.RouteStats
	if err := json.Unmarshal(rec.Body.Bytes(), &routes); err != nil {
		t.Fatal(err)
	}
	if len(routes) != 1 || routes[0].RouteID != "recent" {
		t.Fatalf("routes = %+v", routes)
	}
}
