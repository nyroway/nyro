package sqlite_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	collectlogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	collecttrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	logsv1 "go.opentelemetry.io/proto/otlp/logs/v1"
	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"

	dbsqlite "github.com/nyroway/nyro/go/infra/database/sqlite"
	"github.com/nyroway/nyro/go/infra/observe"
	observesqlite "github.com/nyroway/nyro/go/infra/observe/sqlite"
)

func TestDeleteBeforeIsSignalScopedAndCascadesIndexes(t *testing.T) {
	ctx := context.Background()
	db, err := dbsqlite.Open(ctx, dbsqlite.Options{Path: filepath.Join(t.TempDir(), "observe.db")})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := observesqlite.New(ctx, db, observesqlite.Options{RetentionInterval: -1})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { _ = store.Shutdown(context.Background()) })

	old := time.Unix(1_700_000_000, 0)
	newer := old.Add(2 * time.Hour)
	logRequest := func(event time.Time) *collectlogs.ExportLogsServiceRequest {
		return &collectlogs.ExportLogsServiceRequest{ResourceLogs: []*logsv1.ResourceLogs{{ScopeLogs: []*logsv1.ScopeLogs{{LogRecords: []*logsv1.LogRecord{{TimeUnixNano: uint64(event.UnixNano())}}}}}}}
	}
	traceRequest := &collecttrace.ExportTraceServiceRequest{ResourceSpans: []*tracev1.ResourceSpans{{ScopeSpans: []*tracev1.ScopeSpans{{Spans: []*tracev1.Span{{Name: "keep"}}}}}}}
	if err := store.Append(ctx, []observe.ExportRequest{
		{ReceivedAt: old, Logs: logRequest(newer)},
		{ReceivedAt: newer, Logs: logRequest(old)},
		{ReceivedAt: old, Traces: traceRequest},
	}); err != nil {
		t.Fatalf("Append() error = %v", err)
	}

	deleted, err := store.DeleteBefore(ctx, observe.SignalLogs, old.Add(time.Hour), 10)
	if err != nil {
		t.Fatalf("DeleteBefore() error = %v", err)
	}
	if deleted != 1 {
		t.Fatalf("DeleteBefore() = %d, want 1", deleted)
	}
	logs, err := store.QueryLogs(ctx, observe.LogQuery{})
	if err != nil || len(logs.Records) != 1 || logs.Records[0].ReceivedAt != newer {
		t.Fatalf("remaining logs = %#v, err = %v", logs, err)
	}
	spans, err := store.QuerySpans(ctx, observe.SpanQuery{})
	if err != nil || len(spans.Records) != 1 {
		t.Fatalf("remaining spans = %#v, err = %v", spans, err)
	}
	var indexes int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM otlp_log_index`).Scan(&indexes); err != nil || indexes != 1 {
		t.Fatalf("log index count = %d, err = %v", indexes, err)
	}
}

func TestRetentionJanitorDeletesExpiredBatches(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	store := newStore(t, observesqlite.Options{
		Now: nowTime(now), LogsRetention: time.Hour, MetricsRetention: -1, TracesRetention: -1,
		RetentionInterval: 5 * time.Millisecond, RetentionBatch: 10,
	})
	request := &collectlogs.ExportLogsServiceRequest{ResourceLogs: []*logsv1.ResourceLogs{{ScopeLogs: []*logsv1.ScopeLogs{{LogRecords: []*logsv1.LogRecord{{}}}}}}}
	if err := store.Append(context.Background(), []observe.ExportRequest{{ReceivedAt: now.Add(-2 * time.Hour), Logs: request}}); err != nil {
		t.Fatalf("Append() error = %v", err)
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		page, err := store.QueryLogs(context.Background(), observe.LogQuery{})
		if err != nil {
			t.Fatalf("QueryLogs() error = %v", err)
		}
		if len(page.Records) == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("retention janitor did not delete expired batch")
}

func nowTime(value time.Time) func() time.Time {
	return func() time.Time { return value }
}
