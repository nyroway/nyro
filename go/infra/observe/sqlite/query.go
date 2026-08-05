package sqlite

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/nyroway/nyro/go/infra/observe"
	collectlogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	collectmetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	collecttrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"
)

const (
	defaultQueryLimit = 100
	maxQueryLimit     = 1000
)

func normalizeLimit(limit int) int {
	if limit <= 0 {
		return defaultQueryLimit
	}
	if limit > maxQueryLimit {
		return maxQueryLimit
	}
	return limit
}

func encodeCursor(timestamp, id int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(timestamp, 10) + ":" + strconv.FormatInt(id, 10)))
}

func decodeCursor(cursor string) (int64, int64, error) {
	if cursor == "" {
		return 0, 0, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return 0, 0, errors.New("observe sqlite: invalid cursor")
	}
	left, right, ok := strings.Cut(string(decoded), ":")
	if !ok {
		return 0, 0, errors.New("observe sqlite: invalid cursor")
	}
	timestamp, err := strconv.ParseInt(left, 10, 64)
	if err != nil {
		return 0, 0, errors.New("observe sqlite: invalid cursor")
	}
	id, err := strconv.ParseInt(right, 10, 64)
	if err != nil {
		return 0, 0, errors.New("observe sqlite: invalid cursor")
	}
	return timestamp, id, nil
}

// QueryLogs returns logs in descending effective-time order.
func (s *Store) QueryLogs(ctx context.Context, query observe.LogQuery) (observe.LogPage, error) {
	limit := normalizeLimit(query.Limit)
	var sqlText strings.Builder
	sqlText.WriteString(`
SELECT i.id, i.batch_id, i.resource_idx, i.scope_idx, i.record_idx,
       i.effective_time_ns, b.received_at_ns, b.payload
FROM otlp_log_index i
JOIN otlp_batches b ON b.id = i.batch_id
WHERE 1 = 1`)
	args := make([]any, 0, 12)
	if !query.Start.IsZero() {
		sqlText.WriteString(` AND i.effective_time_ns >= ?`)
		args = append(args, query.Start.UnixNano())
	}
	if !query.End.IsZero() {
		sqlText.WriteString(` AND i.effective_time_ns < ?`)
		args = append(args, query.End.UnixNano())
	}
	if query.Service != "" {
		sqlText.WriteString(` AND i.service_name = ?`)
		args = append(args, query.Service)
	}
	if query.MinSeverity > 0 {
		sqlText.WriteString(` AND i.severity_number >= ?`)
		args = append(args, query.MinSeverity)
	}
	if len(query.TraceID) > 0 {
		sqlText.WriteString(` AND i.trace_id = ?`)
		args = append(args, query.TraceID)
	}
	if len(query.SpanID) > 0 {
		sqlText.WriteString(` AND i.span_id = ?`)
		args = append(args, query.SpanID)
	}
	if query.Cursor != "" {
		timestamp, id, err := decodeCursor(query.Cursor)
		if err != nil {
			return observe.LogPage{}, err
		}
		sqlText.WriteString(` AND (i.effective_time_ns < ? OR (i.effective_time_ns = ? AND i.id < ?))`)
		args = append(args, timestamp, timestamp, id)
	}
	sqlText.WriteString(` ORDER BY i.effective_time_ns DESC, i.id DESC LIMIT ?`)
	args = append(args, limit+1)

	rows, err := s.db.QueryContext(ctx, sqlText.String(), args...)
	if err != nil {
		return observe.LogPage{}, fmt.Errorf("observe sqlite: query logs: %w", err)
	}
	defer rows.Close()

	type indexedLog struct {
		id, batchID                            int64
		resourceIndex, scopeIndex, recordIndex int
		effective, received                    int64
		payload                                []byte
	}
	indexed := make([]indexedLog, 0, limit+1)
	for rows.Next() {
		var row indexedLog
		if err := rows.Scan(&row.id, &row.batchID, &row.resourceIndex, &row.scopeIndex, &row.recordIndex, &row.effective, &row.received, &row.payload); err != nil {
			return observe.LogPage{}, fmt.Errorf("observe sqlite: scan log index: %w", err)
		}
		indexed = append(indexed, row)
	}
	if err := rows.Err(); err != nil {
		return observe.LogPage{}, fmt.Errorf("observe sqlite: iterate logs: %w", err)
	}

	hasMore := len(indexed) > limit
	if hasMore {
		indexed = indexed[:limit]
	}
	decoded := make(map[int64]*collectlogs.ExportLogsServiceRequest)
	page := observe.LogPage{Records: make([]observe.LogRecord, 0, len(indexed))}
	for _, row := range indexed {
		request := decoded[row.batchID]
		if request == nil {
			request = &collectlogs.ExportLogsServiceRequest{}
			if err := proto.Unmarshal(row.payload, request); err != nil {
				return observe.LogPage{}, fmt.Errorf("observe sqlite: decode log batch %d: %w", row.batchID, err)
			}
			decoded[row.batchID] = request
		}
		resourceLogs := request.GetResourceLogs()
		if row.resourceIndex >= len(resourceLogs) || row.scopeIndex >= len(resourceLogs[row.resourceIndex].GetScopeLogs()) {
			return observe.LogPage{}, errors.New("observe sqlite: corrupt log coordinates")
		}
		scopeLogs := resourceLogs[row.resourceIndex].GetScopeLogs()[row.scopeIndex]
		if row.recordIndex >= len(scopeLogs.GetLogRecords()) {
			return observe.LogPage{}, errors.New("observe sqlite: corrupt log record coordinate")
		}
		page.Records = append(page.Records, observe.LogRecord{
			Cursor:     encodeCursor(row.effective, row.id),
			ReceivedAt: time.Unix(0, row.received),
			Resource:   resourceLogs[row.resourceIndex].GetResource(),
			Scope:      scopeLogs.GetScope(),
			Record:     scopeLogs.GetLogRecords()[row.recordIndex],
		})
	}
	if hasMore && len(page.Records) > 0 {
		page.NextCursor = page.Records[len(page.Records)-1].Cursor
	}
	return page, nil
}

// QuerySpans returns spans in descending effective-time order.
func (s *Store) QuerySpans(ctx context.Context, query observe.SpanQuery) (observe.SpanPage, error) {
	limit := normalizeLimit(query.Limit)
	var sqlText strings.Builder
	sqlText.WriteString(`
SELECT i.id, i.batch_id, i.resource_idx, i.scope_idx, i.record_idx,
       i.effective_time_ns, b.received_at_ns, b.payload
FROM otlp_span_index i
JOIN otlp_batches b ON b.id = i.batch_id
WHERE 1 = 1`)
	args := make([]any, 0, 12)
	appendTimeRange(&sqlText, &args, "i.effective_time_ns", query.TimeRange)
	if query.Service != "" {
		sqlText.WriteString(` AND i.service_name = ?`)
		args = append(args, query.Service)
	}
	if len(query.TraceID) > 0 {
		sqlText.WriteString(` AND i.trace_id = ?`)
		args = append(args, query.TraceID)
	}
	if len(query.SpanID) > 0 {
		sqlText.WriteString(` AND i.span_id = ?`)
		args = append(args, query.SpanID)
	}
	if query.Name != "" {
		sqlText.WriteString(` AND i.name = ?`)
		args = append(args, query.Name)
	}
	if err := appendCursor(&sqlText, &args, query.Cursor); err != nil {
		return observe.SpanPage{}, err
	}
	sqlText.WriteString(` ORDER BY i.effective_time_ns DESC, i.id DESC LIMIT ?`)
	args = append(args, limit+1)

	rows, err := s.db.QueryContext(ctx, sqlText.String(), args...)
	if err != nil {
		return observe.SpanPage{}, fmt.Errorf("observe sqlite: query spans: %w", err)
	}
	defer rows.Close()
	indexed, err := scanIndexedRecords(rows, limit)
	if err != nil {
		return observe.SpanPage{}, fmt.Errorf("observe sqlite: scan spans: %w", err)
	}
	hasMore := len(indexed) > limit
	if hasMore {
		indexed = indexed[:limit]
	}
	decoded := make(map[int64]*collecttrace.ExportTraceServiceRequest)
	page := observe.SpanPage{Records: make([]observe.SpanRecord, 0, len(indexed))}
	for _, row := range indexed {
		request := decoded[row.batchID]
		if request == nil {
			request = &collecttrace.ExportTraceServiceRequest{}
			if err := proto.Unmarshal(row.payload, request); err != nil {
				return observe.SpanPage{}, fmt.Errorf("observe sqlite: decode trace batch %d: %w", row.batchID, err)
			}
			decoded[row.batchID] = request
		}
		resources := request.GetResourceSpans()
		if row.resourceIndex >= len(resources) || row.scopeIndex >= len(resources[row.resourceIndex].GetScopeSpans()) {
			return observe.SpanPage{}, errors.New("observe sqlite: corrupt span coordinates")
		}
		scope := resources[row.resourceIndex].GetScopeSpans()[row.scopeIndex]
		if row.recordIndex >= len(scope.GetSpans()) {
			return observe.SpanPage{}, errors.New("observe sqlite: corrupt span record coordinate")
		}
		page.Records = append(page.Records, observe.SpanRecord{
			Cursor: encodeCursor(row.effective, row.id), ReceivedAt: time.Unix(0, row.received),
			Resource: resources[row.resourceIndex].GetResource(), Scope: scope.GetScope(), Span: scope.GetSpans()[row.recordIndex],
		})
	}
	if hasMore && len(page.Records) > 0 {
		page.NextCursor = page.Records[len(page.Records)-1].Cursor
	}
	return page, nil
}

// QueryMetrics returns metric data points in descending effective-time order.
func (s *Store) QueryMetrics(ctx context.Context, query observe.MetricQuery) (observe.MetricPage, error) {
	limit := normalizeLimit(query.Limit)
	var sqlText strings.Builder
	sqlText.WriteString(`
SELECT i.id, i.batch_id, i.resource_idx, i.scope_idx, i.metric_idx, i.point_idx,
       i.point_type, i.effective_time_ns, b.received_at_ns, b.payload
FROM otlp_metric_index i
JOIN otlp_batches b ON b.id = i.batch_id
WHERE 1 = 1`)
	args := make([]any, 0, 12)
	appendTimeRange(&sqlText, &args, "i.effective_time_ns", query.TimeRange)
	if query.Service != "" {
		sqlText.WriteString(` AND i.service_name = ?`)
		args = append(args, query.Service)
	}
	if query.Name != "" {
		sqlText.WriteString(` AND i.metric_name = ?`)
		args = append(args, query.Name)
	}
	if query.Type != "" {
		sqlText.WriteString(` AND i.point_type = ?`)
		args = append(args, query.Type)
	}
	if err := appendCursor(&sqlText, &args, query.Cursor); err != nil {
		return observe.MetricPage{}, err
	}
	sqlText.WriteString(` ORDER BY i.effective_time_ns DESC, i.id DESC LIMIT ?`)
	args = append(args, limit+1)

	rows, err := s.db.QueryContext(ctx, sqlText.String(), args...)
	if err != nil {
		return observe.MetricPage{}, fmt.Errorf("observe sqlite: query metrics: %w", err)
	}
	defer rows.Close()
	type indexedMetric struct {
		id, batchID               int64
		resourceIndex, scopeIndex int
		metricIndex, pointIndex   int
		pointType                 string
		effective, received       int64
		payload                   []byte
	}
	indexed := make([]indexedMetric, 0, limit+1)
	for rows.Next() {
		var row indexedMetric
		if err := rows.Scan(&row.id, &row.batchID, &row.resourceIndex, &row.scopeIndex, &row.metricIndex, &row.pointIndex, &row.pointType, &row.effective, &row.received, &row.payload); err != nil {
			return observe.MetricPage{}, fmt.Errorf("observe sqlite: scan metric index: %w", err)
		}
		indexed = append(indexed, row)
	}
	if err := rows.Err(); err != nil {
		return observe.MetricPage{}, fmt.Errorf("observe sqlite: iterate metrics: %w", err)
	}
	hasMore := len(indexed) > limit
	if hasMore {
		indexed = indexed[:limit]
	}
	decoded := make(map[int64]*collectmetrics.ExportMetricsServiceRequest)
	page := observe.MetricPage{Records: make([]observe.MetricRecord, 0, len(indexed))}
	for _, row := range indexed {
		request := decoded[row.batchID]
		if request == nil {
			request = &collectmetrics.ExportMetricsServiceRequest{}
			if err := proto.Unmarshal(row.payload, request); err != nil {
				return observe.MetricPage{}, fmt.Errorf("observe sqlite: decode metric batch %d: %w", row.batchID, err)
			}
			decoded[row.batchID] = request
		}
		resources := request.GetResourceMetrics()
		if row.resourceIndex >= len(resources) || row.scopeIndex >= len(resources[row.resourceIndex].GetScopeMetrics()) {
			return observe.MetricPage{}, errors.New("observe sqlite: corrupt metric coordinates")
		}
		scope := resources[row.resourceIndex].GetScopeMetrics()[row.scopeIndex]
		if row.metricIndex >= len(scope.GetMetrics()) {
			return observe.MetricPage{}, errors.New("observe sqlite: corrupt metric coordinate")
		}
		page.Records = append(page.Records, observe.MetricRecord{
			Cursor: encodeCursor(row.effective, row.id), ReceivedAt: time.Unix(0, row.received),
			Resource: resources[row.resourceIndex].GetResource(), Scope: scope.GetScope(), Metric: scope.GetMetrics()[row.metricIndex],
			PointType: row.pointType, DataPointIndex: row.pointIndex,
		})
	}
	if hasMore && len(page.Records) > 0 {
		page.NextCursor = page.Records[len(page.Records)-1].Cursor
	}
	return page, nil
}

type indexedRecord struct {
	id, batchID                            int64
	resourceIndex, scopeIndex, recordIndex int
	effective, received                    int64
	payload                                []byte
}

type rowScanner interface {
	Next() bool
	Scan(...any) error
	Err() error
}

func scanIndexedRecords(rows rowScanner, limit int) ([]indexedRecord, error) {
	indexed := make([]indexedRecord, 0, limit+1)
	for rows.Next() {
		var row indexedRecord
		if err := rows.Scan(&row.id, &row.batchID, &row.resourceIndex, &row.scopeIndex, &row.recordIndex, &row.effective, &row.received, &row.payload); err != nil {
			return nil, err
		}
		indexed = append(indexed, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return indexed, nil
}

func appendTimeRange(sqlText *strings.Builder, args *[]any, column string, timeRange observe.TimeRange) {
	if !timeRange.Start.IsZero() {
		sqlText.WriteString(` AND ` + column + ` >= ?`)
		*args = append(*args, timeRange.Start.UnixNano())
	}
	if !timeRange.End.IsZero() {
		sqlText.WriteString(` AND ` + column + ` < ?`)
		*args = append(*args, timeRange.End.UnixNano())
	}
}

func appendCursor(sqlText *strings.Builder, args *[]any, cursor string) error {
	if cursor == "" {
		return nil
	}
	timestamp, id, err := decodeCursor(cursor)
	if err != nil {
		return err
	}
	sqlText.WriteString(` AND (i.effective_time_ns < ? OR (i.effective_time_ns = ? AND i.id < ?))`)
	*args = append(*args, timestamp, timestamp, id)
	return nil
}
