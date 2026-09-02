package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	collectlogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	logsv1 "go.opentelemetry.io/proto/otlp/logs/v1"
	resourcev1 "go.opentelemetry.io/proto/otlp/resource/v1"

	dbsqlite "github.com/nyroway/nyro/go/internal/platform/database/sqlite"
	infraobserve "github.com/nyroway/nyro/go/internal/platform/observe"
	observesqlite "github.com/nyroway/nyro/go/internal/platform/observe/sqlite"
	"github.com/nyroway/nyro/go/internal/storage/memory"
	"github.com/nyroway/nyro/go/internal/telemetry"
)

func observeAttributes(values map[string]any) []*commonv1.KeyValue {
	attributes := make([]*commonv1.KeyValue, 0, len(values))
	for key, value := range values {
		attribute := &commonv1.KeyValue{Key: key, Value: &commonv1.AnyValue{}}
		switch typed := value.(type) {
		case string:
			attribute.Value.Value = &commonv1.AnyValue_StringValue{StringValue: typed}
		case int64:
			attribute.Value.Value = &commonv1.AnyValue_IntValue{IntValue: typed}
		case bool:
			attribute.Value.Value = &commonv1.AnyValue_BoolValue{BoolValue: typed}
		default:
			panic("unsupported test attribute")
		}
		attributes = append(attributes, attribute)
	}
	return attributes
}

func newObserveEngine(t *testing.T) (chi.Router, *observesqlite.Store) {
	t.Helper()
	ctx := context.Background()
	db, err := dbsqlite.Open(ctx, dbsqlite.Options{Path: filepath.Join(t.TempDir(), "observe.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := observesqlite.New(ctx, db, observesqlite.Options{
		RetentionInterval: -1,
		IndexedLogAttributes: []infraobserve.AttributeIndex{
			{Key: "nyro.log.id", Type: infraobserve.AttributeString},
			{Key: "nyro.upstream.id", Type: infraobserve.AttributeString},
			{Key: "nyro.route.id", Type: infraobserve.AttributeString},
			{Key: "nyro.route.model", Type: infraobserve.AttributeString},
			{Key: "nyro.consumer.id", Type: infraobserve.AttributeString},
			{Key: "http.response.status_code", Type: infraobserve.AttributeInt64},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Shutdown(context.Background()) })
	source := NewObserveSource(store)
	router := chi.NewRouter()
	Mount(router, memory.New().Storage(), source, source, testProtocolCatalog(t), testProviderCatalog(t))
	return router, store
}

func appendAuditLog(t *testing.T, store infraobserve.Store, values map[string]any) {
	t.Helper()
	record := &logsv1.LogRecord{
		TimeUnixNano: uint64(time.Now().UnixNano()),
		Attributes:   observeAttributes(values),
	}
	request := &collectlogs.ExportLogsServiceRequest{ResourceLogs: []*logsv1.ResourceLogs{{
		Resource:  &resourcev1.Resource{},
		ScopeLogs: []*logsv1.ScopeLogs{{LogRecords: []*logsv1.LogRecord{record}}},
	}}}
	if err := store.Append(context.Background(), []infraobserve.ExportRequest{{ReceivedAt: time.Now(), Logs: request}}); err != nil {
		t.Fatal(err)
	}
}

func TestLogsReadAndFilterObserveStore(t *testing.T) {
	router, store := newObserveEngine(t)
	appendAuditLog(t, store, map[string]any{
		"nyro.log.id": "log-1", "nyro.log.created_ms": int64(1_700_000_000_000),
		"nyro.upstream.id": "upstream-1", "nyro.upstream.name": "OpenAI",
		"nyro.route.id": "route-1", "nyro.route.model": "gpt-4o",
		"nyro.consumer.id": "consumer-1", "http.response.status_code": int64(429),
		"nyro.input_tokens": int64(11),
	})
	appendAuditLog(t, store, map[string]any{
		"nyro.log.id": "log-2", "nyro.route.id": "route-2",
		"http.response.status_code": int64(200),
	})

	rec := do(router, "GET", "/api/v1/logs?route_id=route-1&upstream_id=upstream-1&consumer_id=consumer-1&status_min=400&status_max=499", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("/logs -> %d %s", rec.Code, rec.Body.String())
	}
	var page telemetry.LogPage
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || len(page.Items) != 1 {
		t.Fatalf("page = %+v", page)
	}
	got := page.Items[0]
	if got.ID != "log-1" || got.RouteID != "route-1" || got.RouteModel != "gpt-4o" || got.UpstreamID != "upstream-1" || got.ConsumerID != "consumer-1" || got.ResponseStatusCode == nil || *got.ResponseStatusCode != 429 || got.InputTokens != 11 {
		t.Fatalf("log = %+v", got)
	}
}

func TestLogsFindAndClearObserveStore(t *testing.T) {
	router, store := newObserveEngine(t)
	appendAuditLog(t, store, map[string]any{"nyro.log.id": "log-99", "nyro.route.model": "gpt-4o"})

	rec := do(router, "GET", "/api/v1/logs/log-99", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("find -> %d %s", rec.Code, rec.Body.String())
	}
	var got telemetry.LogRecord
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.ID != "log-99" || got.RouteModel != "gpt-4o" {
		t.Fatalf("log = %+v", got)
	}

	rec = do(router, "DELETE", "/api/v1/logs", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete -> %d %s", rec.Code, rec.Body.String())
	}
	rec = do(router, "GET", "/api/v1/logs", "", nil)
	var page telemetry.LogPage
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.Total != 0 || len(page.Items) != 0 {
		t.Fatalf("logs were not cleared: %+v", page)
	}
}

func TestLogsRejectInvalidNumericFilters(t *testing.T) {
	router, _ := newObserveEngine(t)
	for _, query := range []string{
		"status_min=nope",
		"offset=-1",
		"limit=-1",
		"status_min=500&status_max=400",
	} {
		rec := do(router, "GET", "/api/v1/logs?"+query, "", nil)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("query %q status = %d, want 400", query, rec.Code)
		}
	}
}
