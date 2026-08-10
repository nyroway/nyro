package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"time"

	collectlogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	collectmetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	collecttrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	metricsv1 "go.opentelemetry.io/proto/otlp/metrics/v1"

	"github.com/nyroway/nyro/go/internal/platform/observe"
)

func (s *Store) indexLogs(ctx context.Context, tx *sql.Tx, batchID, receivedAt int64, request *collectlogs.ExportLogsServiceRequest) error {
	for resourceIndex, resourceLogs := range request.GetResourceLogs() {
		service := serviceName(resourceLogs.GetResource().GetAttributes())
		for scopeIndex, scopeLogs := range resourceLogs.GetScopeLogs() {
			for recordIndex, record := range scopeLogs.GetLogRecords() {
				effective, err := checkedOTLPUnixNano(record.GetTimeUnixNano())
				if err != nil {
					return fmt.Errorf("observe sqlite: log timestamp: %w", err)
				}
				if effective == 0 {
					effective, err = checkedOTLPUnixNano(record.GetObservedTimeUnixNano())
					if err != nil {
						return fmt.Errorf("observe sqlite: observed log timestamp: %w", err)
					}
				}
				if effective == 0 {
					effective = receivedAt
				}
				result, err := tx.ExecContext(ctx, `
INSERT INTO otlp_log_index(
    batch_id, resource_idx, scope_idx, record_idx, effective_time_ns,
    service_name, severity_number, trace_id, span_id
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
`, batchID, resourceIndex, scopeIndex, recordIndex, effective, service,
					int32(record.GetSeverityNumber()), nullBytes(record.GetTraceId()), nullBytes(record.GetSpanId()))
				if err != nil {
					return err
				}
				logID, err := result.LastInsertId()
				if err != nil {
					return fmt.Errorf("observe sqlite: log index id: %w", err)
				}
				if err := indexLogAttributes(ctx, tx, logID, record.GetAttributes(), s.indexedLogAttributes); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func indexSpans(ctx context.Context, tx *sql.Tx, batchID, receivedAt int64, request *collecttrace.ExportTraceServiceRequest) error {
	for resourceIndex, resourceSpans := range request.GetResourceSpans() {
		service := serviceName(resourceSpans.GetResource().GetAttributes())
		for scopeIndex, scopeSpans := range resourceSpans.GetScopeSpans() {
			for recordIndex, span := range scopeSpans.GetSpans() {
				startTime, err := checkedOTLPUnixNano(span.GetStartTimeUnixNano())
				if err != nil {
					return fmt.Errorf("observe sqlite: span start timestamp: %w", err)
				}
				endTime, err := checkedOTLPUnixNano(span.GetEndTimeUnixNano())
				if err != nil {
					return fmt.Errorf("observe sqlite: span end timestamp: %w", err)
				}
				effective := startTime
				if effective == 0 {
					effective = receivedAt
				}
				_, err = tx.ExecContext(ctx, `
INSERT INTO otlp_span_index(
    batch_id, resource_idx, scope_idx, record_idx, effective_time_ns,
    service_name, trace_id, span_id, parent_span_id, name,
    start_time_ns, end_time_ns, status_code
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, batchID, resourceIndex, scopeIndex, recordIndex, effective, service,
					nullBytes(span.GetTraceId()), nullBytes(span.GetSpanId()), nullBytes(span.GetParentSpanId()), span.GetName(),
					startTime, endTime, int32(span.GetStatus().GetCode()))
				if err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func indexMetrics(ctx context.Context, tx *sql.Tx, batchID, receivedAt int64, request *collectmetrics.ExportMetricsServiceRequest) error {
	for resourceIndex, resourceMetrics := range request.GetResourceMetrics() {
		service := serviceName(resourceMetrics.GetResource().GetAttributes())
		for scopeIndex, scopeMetrics := range resourceMetrics.GetScopeMetrics() {
			for metricIndex, metric := range scopeMetrics.GetMetrics() {
				switch data := metric.GetData().(type) {
				case *metricsv1.Metric_Gauge:
					for pointIndex, point := range data.Gauge.GetDataPoints() {
						if err := insertMetricIndex(ctx, tx, batchID, resourceIndex, scopeIndex, metricIndex, pointIndex, "gauge", point.GetTimeUnixNano(), point.GetStartTimeUnixNano(), receivedAt, service, metric.GetName()); err != nil {
							return err
						}
					}
				case *metricsv1.Metric_Sum:
					for pointIndex, point := range data.Sum.GetDataPoints() {
						if err := insertMetricIndex(ctx, tx, batchID, resourceIndex, scopeIndex, metricIndex, pointIndex, "sum", point.GetTimeUnixNano(), point.GetStartTimeUnixNano(), receivedAt, service, metric.GetName()); err != nil {
							return err
						}
					}
				case *metricsv1.Metric_Histogram:
					for pointIndex, point := range data.Histogram.GetDataPoints() {
						if err := insertMetricIndex(ctx, tx, batchID, resourceIndex, scopeIndex, metricIndex, pointIndex, "histogram", point.GetTimeUnixNano(), point.GetStartTimeUnixNano(), receivedAt, service, metric.GetName()); err != nil {
							return err
						}
					}
				case *metricsv1.Metric_ExponentialHistogram:
					for pointIndex, point := range data.ExponentialHistogram.GetDataPoints() {
						if err := insertMetricIndex(ctx, tx, batchID, resourceIndex, scopeIndex, metricIndex, pointIndex, "exponential_histogram", point.GetTimeUnixNano(), point.GetStartTimeUnixNano(), receivedAt, service, metric.GetName()); err != nil {
							return err
						}
					}
				case *metricsv1.Metric_Summary:
					for pointIndex, point := range data.Summary.GetDataPoints() {
						if err := insertMetricIndex(ctx, tx, batchID, resourceIndex, scopeIndex, metricIndex, pointIndex, "summary", point.GetTimeUnixNano(), point.GetStartTimeUnixNano(), receivedAt, service, metric.GetName()); err != nil {
							return err
						}
					}
				}
			}
		}
	}
	return nil
}

func insertMetricIndex(ctx context.Context, tx *sql.Tx, batchID int64, resourceIndex, scopeIndex, metricIndex, pointIndex int, pointType string, eventTime, startTime uint64, receivedAt int64, service, metricName string) error {
	effective, err := checkedOTLPUnixNano(eventTime)
	if err != nil {
		return fmt.Errorf("observe sqlite: metric point timestamp: %w", err)
	}
	checkedStart, err := checkedOTLPUnixNano(startTime)
	if err != nil {
		return fmt.Errorf("observe sqlite: metric point start timestamp: %w", err)
	}
	if effective == 0 {
		effective = checkedStart
	}
	if effective == 0 {
		effective = receivedAt
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO otlp_metric_index(
    batch_id, resource_idx, scope_idx, metric_idx, point_idx, point_type,
    effective_time_ns, start_time_ns, service_name, metric_name
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, batchID, resourceIndex, scopeIndex, metricIndex, pointIndex, pointType,
		effective, checkedStart, service, metricName)
	return err
}

func checkedOTLPUnixNano(value uint64) (int64, error) {
	if value > math.MaxInt64 {
		return 0, observe.ErrTimestampOutOfRange
	}
	return int64(value), nil
}

func checkedTimeUnixNano(value time.Time) (int64, error) {
	nanoseconds := value.UnixNano()
	if !time.Unix(0, nanoseconds).Equal(value) {
		return 0, observe.ErrTimestampOutOfRange
	}
	return nanoseconds, nil
}

func nullBytes(value []byte) any {
	if len(value) == 0 {
		return nil
	}
	return value
}
