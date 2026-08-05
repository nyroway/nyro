package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	"google.golang.org/protobuf/proto"

	"github.com/nyroway/nyro/go/infra/observe"
)

const schemaVersion = 2

// Options configures retention and time for the Observe SQLite store.
type Options struct {
	LogsRetention        time.Duration
	MetricsRetention     time.Duration
	TracesRetention      time.Duration
	RetentionInterval    time.Duration
	RetentionBatch       int
	IndexedLogAttributes []observe.AttributeIndex
	Now                  func() time.Time
}

// Store persists OTLP batches and signal-specific indexes.
type Store struct {
	db                   *sql.DB
	now                  func() time.Time
	retention            retentionOptions
	indexedLogAttributes map[string]observe.AttributeType
	write                sync.Mutex
	cancel               context.CancelFunc
	done                 chan struct{}
	stop                 sync.Once
}

// New migrates the Observe schema without taking ownership of db.
func New(ctx context.Context, db *sql.DB, opts Options) (*Store, error) {
	if db == nil {
		return nil, errors.New("observe sqlite: database is required")
	}
	if err := migrate(ctx, db); err != nil {
		return nil, err
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	indexed, err := registerLogAttributeIndexes(ctx, db, opts.IndexedLogAttributes)
	if err != nil {
		return nil, err
	}
	s := &Store{
		db: db, now: now, retention: normalizeRetention(opts),
		indexedLogAttributes: indexed, done: make(chan struct{}),
	}
	if opts.RetentionInterval < 0 {
		close(s.done)
	} else {
		janitorContext, cancel := context.WithCancel(context.Background())
		s.cancel = cancel
		go s.runRetention(janitorContext)
	}
	return s, nil
}

func migrate(ctx context.Context, db *sql.DB) error {
	const ddl = `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version INTEGER PRIMARY KEY,
    applied_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS otlp_batches (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    signal TEXT NOT NULL,
    received_at_ns INTEGER NOT NULL,
    payload BLOB NOT NULL
);
CREATE INDEX IF NOT EXISTS otlp_batches_retention ON otlp_batches(signal, received_at_ns, id);
CREATE TABLE IF NOT EXISTS otlp_log_index (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    batch_id INTEGER NOT NULL REFERENCES otlp_batches(id) ON DELETE CASCADE,
    resource_idx INTEGER NOT NULL,
    scope_idx INTEGER NOT NULL,
    record_idx INTEGER NOT NULL,
    effective_time_ns INTEGER NOT NULL,
    service_name TEXT NOT NULL DEFAULT '',
    severity_number INTEGER NOT NULL DEFAULT 0,
    trace_id BLOB,
    span_id BLOB
);
CREATE INDEX IF NOT EXISTS otlp_logs_time ON otlp_log_index(effective_time_ns DESC, id DESC);
CREATE INDEX IF NOT EXISTS otlp_logs_service_time ON otlp_log_index(service_name, effective_time_ns DESC, id DESC);
CREATE INDEX IF NOT EXISTS otlp_logs_trace_time ON otlp_log_index(trace_id, effective_time_ns DESC, id DESC);
CREATE TABLE IF NOT EXISTS otlp_log_attribute_definitions (
    key TEXT PRIMARY KEY,
    value_type TEXT NOT NULL CHECK(value_type IN ('string', 'int64'))
);
CREATE TABLE IF NOT EXISTS otlp_log_attributes (
    log_id INTEGER NOT NULL REFERENCES otlp_log_index(id) ON DELETE CASCADE,
    key TEXT NOT NULL REFERENCES otlp_log_attribute_definitions(key),
    string_value TEXT,
    int_value INTEGER,
    PRIMARY KEY(log_id, key)
);
CREATE INDEX IF NOT EXISTS otlp_log_attributes_string ON otlp_log_attributes(key, string_value, log_id);
CREATE INDEX IF NOT EXISTS otlp_log_attributes_int ON otlp_log_attributes(key, int_value, log_id);
CREATE TABLE IF NOT EXISTS otlp_span_index (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    batch_id INTEGER NOT NULL REFERENCES otlp_batches(id) ON DELETE CASCADE,
    resource_idx INTEGER NOT NULL,
    scope_idx INTEGER NOT NULL,
    record_idx INTEGER NOT NULL,
    effective_time_ns INTEGER NOT NULL,
    service_name TEXT NOT NULL DEFAULT '',
    trace_id BLOB,
    span_id BLOB,
    parent_span_id BLOB,
    name TEXT NOT NULL DEFAULT '',
    start_time_ns INTEGER NOT NULL DEFAULT 0,
    end_time_ns INTEGER NOT NULL DEFAULT 0,
    status_code INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS otlp_spans_time ON otlp_span_index(effective_time_ns DESC, id DESC);
CREATE INDEX IF NOT EXISTS otlp_spans_trace_time ON otlp_span_index(trace_id, effective_time_ns DESC, id DESC);
CREATE INDEX IF NOT EXISTS otlp_spans_service_time ON otlp_span_index(service_name, effective_time_ns DESC, id DESC);
CREATE TABLE IF NOT EXISTS otlp_metric_index (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    batch_id INTEGER NOT NULL REFERENCES otlp_batches(id) ON DELETE CASCADE,
    resource_idx INTEGER NOT NULL,
    scope_idx INTEGER NOT NULL,
    metric_idx INTEGER NOT NULL,
    point_idx INTEGER NOT NULL,
    point_type TEXT NOT NULL,
    effective_time_ns INTEGER NOT NULL,
    start_time_ns INTEGER NOT NULL DEFAULT 0,
    service_name TEXT NOT NULL DEFAULT '',
    metric_name TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS otlp_metrics_time ON otlp_metric_index(effective_time_ns DESC, id DESC);
CREATE INDEX IF NOT EXISTS otlp_metrics_name_time ON otlp_metric_index(metric_name, effective_time_ns DESC, id DESC);
CREATE INDEX IF NOT EXISTS otlp_metrics_service_time ON otlp_metric_index(service_name, effective_time_ns DESC, id DESC);
INSERT OR IGNORE INTO schema_migrations(version, applied_at)
VALUES (1, CAST(strftime('%s','now') AS INTEGER) * 1000);
INSERT OR IGNORE INTO schema_migrations(version, applied_at)
VALUES (2, CAST(strftime('%s','now') AS INTEGER) * 1000);
`
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL)`); err != nil {
		return fmt.Errorf("observe sqlite: create migration table: %w", err)
	}
	var version int
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&version); err != nil {
		return fmt.Errorf("observe sqlite: read schema version: %w", err)
	}
	if version > schemaVersion {
		return fmt.Errorf("observe sqlite: schema version %d is newer than supported version %d", version, schemaVersion)
	}
	if _, err := db.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("observe sqlite: migrate: %w", err)
	}
	return nil
}

// Append atomically stores batches and their query indexes.
func (s *Store) Append(ctx context.Context, requests []observe.ExportRequest) error {
	s.write.Lock()
	defer s.write.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("observe sqlite: begin append: %w", err)
	}
	for _, request := range requests {
		if err := s.appendOne(ctx, tx, request); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("observe sqlite: commit append: %w", err)
	}
	return nil
}

func (s *Store) appendOne(ctx context.Context, tx *sql.Tx, request observe.ExportRequest) error {
	signal, err := request.Signal()
	if err != nil {
		return err
	}
	receivedAt := request.ReceivedAt
	if receivedAt.IsZero() {
		receivedAt = s.now()
	}
	receivedAtNS, err := checkedTimeUnixNano(receivedAt)
	if err != nil {
		return err
	}
	var message proto.Message
	switch signal {
	case observe.SignalLogs:
		message = request.Logs
	case observe.SignalMetrics:
		message = request.Metrics
	case observe.SignalTraces:
		message = request.Traces
	}
	payload, err := proto.Marshal(message)
	if err != nil {
		return fmt.Errorf("observe sqlite: marshal %s export: %w", signal, err)
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO otlp_batches(signal, received_at_ns, payload) VALUES (?, ?, ?)`, string(signal), receivedAtNS, payload)
	if err != nil {
		return fmt.Errorf("observe sqlite: insert batch: %w", err)
	}
	batchID, err := result.LastInsertId()
	if err != nil {
		return fmt.Errorf("observe sqlite: batch id: %w", err)
	}
	switch signal {
	case observe.SignalLogs:
		return s.indexLogs(ctx, tx, batchID, receivedAtNS, request.Logs)
	case observe.SignalMetrics:
		return indexMetrics(ctx, tx, batchID, receivedAtNS, request.Metrics)
	case observe.SignalTraces:
		return indexSpans(ctx, tx, batchID, receivedAtNS, request.Traces)
	}
	return nil
}

func serviceName(attributes []*commonv1.KeyValue) string {
	for _, attribute := range attributes {
		if attribute.GetKey() == "service.name" {
			return attribute.GetValue().GetStringValue()
		}
	}
	return ""
}

// Shutdown stops store-owned background work without closing the database.
func (s *Store) Shutdown(ctx context.Context) error {
	s.stop.Do(func() {
		if s.cancel != nil {
			s.cancel()
		}
	})
	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

var _ observe.Store = (*Store)(nil)
