// Package observe defines lossless OTLP persistence and indexed query
// contracts independent of any application-specific telemetry model.
//
// Layer: 0 (foundation). It may import the standard library, OTLP/protobuf
// dependencies, and its own subpackages, but no other Nyro domain.
package observe

import (
	"context"
	"errors"
	"time"

	collectlogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	collectmetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	collecttrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	logsv1 "go.opentelemetry.io/proto/otlp/logs/v1"
	metricsv1 "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcev1 "go.opentelemetry.io/proto/otlp/resource/v1"
	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"
)

// Signal identifies an OTLP signal persisted by Observe.
type Signal string

const (
	SignalLogs    Signal = "logs"
	SignalMetrics Signal = "metrics"
	SignalTraces  Signal = "traces"
)

var (
	ErrInvalidExport       = errors.New("observe: export request must contain exactly one signal")
	ErrInvalidQuery        = errors.New("observe: invalid query")
	ErrUnindexedAttribute  = errors.New("observe: attribute is not indexed")
	ErrTimestampOutOfRange = errors.New("observe: timestamp exceeds signed nanosecond range")
)

// ExportRequest is one validated OTLP export and its receiver timestamp.
type ExportRequest struct {
	ReceivedAt time.Time
	Logs       *collectlogs.ExportLogsServiceRequest
	Metrics    *collectmetrics.ExportMetricsServiceRequest
	Traces     *collecttrace.ExportTraceServiceRequest
}

// Signal returns the request signal or ErrInvalidExport.
func (r ExportRequest) Signal() (Signal, error) {
	count := 0
	var signal Signal
	if r.Logs != nil {
		count++
		signal = SignalLogs
	}
	if r.Metrics != nil {
		count++
		signal = SignalMetrics
	}
	if r.Traces != nil {
		count++
		signal = SignalTraces
	}
	if count != 1 {
		return "", ErrInvalidExport
	}
	return signal, nil
}

// TimeRange restricts records by their effective event timestamp.
type TimeRange struct {
	Start time.Time
	End   time.Time
}

// AttributeType is the scalar representation stored in an attribute index.
type AttributeType string

const (
	AttributeString AttributeType = "string"
	AttributeInt64  AttributeType = "int64"
)

// AttributeIndex registers a log-record attribute for indexed queries.
type AttributeIndex struct {
	Key  string
	Type AttributeType
}

// AttributeFilter applies one exact string or integer equality/range match.
type AttributeFilter struct {
	Key          string
	StringEquals *string
	IntEquals    *int64
	IntMin       *int64
	IntMax       *int64
}

// LogQuery filters OTLP log records.
type LogQuery struct {
	TimeRange
	Service      string
	MinSeverity  int32
	TraceID      []byte
	SpanID       []byte
	Attributes   []AttributeFilter
	Limit        int
	Offset       int
	IncludeTotal bool
	Cursor       string
}

// SpanQuery filters OTLP spans.
type SpanQuery struct {
	TimeRange
	Service string
	TraceID []byte
	SpanID  []byte
	Name    string
	Limit   int
	Cursor  string
}

// MetricQuery filters OTLP metric data points.
type MetricQuery struct {
	TimeRange
	Service string
	Name    string
	Type    string
	Limit   int
	Cursor  string
}

type LogRecord struct {
	Cursor     string
	ReceivedAt time.Time
	Resource   *resourcev1.Resource
	Scope      *commonv1.InstrumentationScope
	Record     *logsv1.LogRecord
}

type SpanRecord struct {
	Cursor     string
	ReceivedAt time.Time
	Resource   *resourcev1.Resource
	Scope      *commonv1.InstrumentationScope
	Span       *tracev1.Span
}

type MetricRecord struct {
	Cursor         string
	ReceivedAt     time.Time
	Resource       *resourcev1.Resource
	Scope          *commonv1.InstrumentationScope
	Metric         *metricsv1.Metric
	PointType      string
	DataPointIndex int
}

type LogPage struct {
	Records    []LogRecord
	NextCursor string
	Total      int64
}

type SpanPage struct {
	Records    []SpanRecord
	NextCursor string
}

type MetricPage struct {
	Records    []MetricRecord
	NextCursor string
}

// Store is the persistence and query contract consumed by OTLP receivers.
type Store interface {
	Append(context.Context, []ExportRequest) error
	QueryLogs(context.Context, LogQuery) (LogPage, error)
	QuerySpans(context.Context, SpanQuery) (SpanPage, error)
	QueryMetrics(context.Context, MetricQuery) (MetricPage, error)
	DeleteBefore(context.Context, Signal, time.Time, int) (int64, error)
}
