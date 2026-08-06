package observability

import (
	"encoding/json"
	"fmt"

	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	logsv1 "go.opentelemetry.io/proto/otlp/logs/v1"
	metricsv1 "go.opentelemetry.io/proto/otlp/metrics/v1"
)

// LogRecordFromOTLP maps Nyro's OTLP audit attributes onto the Admin DTO.
func LogRecordFromOTLP(record *logsv1.LogRecord) LogRecord {
	attributes := newAttributeTable(record.GetAttributes())
	projected := LogRecord{
		ID:         attributes.string("nyro.log.id"),
		ConsumerID: attributes.string("nyro.consumer.id"), ConsumerKeyName: attributes.string("nyro.consumer_key.name"), ConsumerKeyPreview: attributes.string("nyro.consumer_key.preview"),
		ClientProtocol: attributes.string("nyro.client_protocol"), UpstreamProtocol: attributes.string("nyro.upstream_protocol"),
		UpstreamID: attributes.string("nyro.upstream.id"), UpstreamName: attributes.string("nyro.upstream.name"),
		RouteID: attributes.string("nyro.route.id"), RouteModel: attributes.string("nyro.route.model"),
		ClientModel: attributes.string("nyro.client_model"), UpstreamModel: attributes.string("nyro.upstream_model"),
		Method: attributes.string("nyro.method"), Path: attributes.string("nyro.path"),
		InputTokens: int32(attributes.integerOrZero("nyro.input_tokens")), OutputTokens: int32(attributes.integerOrZero("nyro.output_tokens")),
		CacheReadTokens: int32(attributes.integerOrZero("nyro.cache_read_tokens")), IsStream: attributes.boolean("nyro.is_stream"),
	}
	if value, ok := attributes.integer("nyro.log.created_ms"); ok {
		projected.CreatedAt = value
	}
	if value, ok := attributes.integer("http.response.status_code"); ok {
		status := int32(value)
		projected.ResponseStatusCode = &status
	}
	if value, ok := attributes.integer("nyro.upstream.status_code"); ok {
		status := int32(value)
		projected.UpstreamStatusCode = &status
	}
	if value, ok := attributes.integer("nyro.latency_total_ms"); ok {
		projected.LatencyTotalMs = &value
	}
	if value, ok := attributes.integer("nyro.latency_upstream_ms"); ok {
		projected.LatencyUpstreamMs = &value
	}
	if projected.CreatedAt == 0 {
		projected.CreatedAt = int64(record.GetTimeUnixNano() / 1_000_000)
	}
	return projected
}

// MetricSampleFromOTLP selects one indexed OTLP data point from a metric.
func MetricSampleFromOTLP(metric *metricsv1.Metric, pointType string, index int) (MetricSample, error) {
	sample := MetricSample{Name: metric.GetName(), Kind: pointType}
	switch data := metric.Data.(type) {
	case *metricsv1.Metric_Gauge:
		if index < 0 || index >= len(data.Gauge.GetDataPoints()) {
			return MetricSample{}, fmt.Errorf("metric %q gauge point index %d is out of range", metric.GetName(), index)
		}
		point := data.Gauge.GetDataPoints()[index]
		sample.Ts = int64(point.GetTimeUnixNano())
		sample.Value = numberDataPointValue(point)
		sample.LabelsJSON = attributesJSON(point.GetAttributes())
	case *metricsv1.Metric_Sum:
		if index < 0 || index >= len(data.Sum.GetDataPoints()) {
			return MetricSample{}, fmt.Errorf("metric %q sum point index %d is out of range", metric.GetName(), index)
		}
		point := data.Sum.GetDataPoints()[index]
		sample.Ts = int64(point.GetTimeUnixNano())
		sample.Value = numberDataPointValue(point)
		sample.LabelsJSON = attributesJSON(point.GetAttributes())
		sample.Kind = "counter"
	case *metricsv1.Metric_Histogram:
		if index < 0 || index >= len(data.Histogram.GetDataPoints()) {
			return MetricSample{}, fmt.Errorf("metric %q histogram point index %d is out of range", metric.GetName(), index)
		}
		point := data.Histogram.GetDataPoints()[index]
		sample.Ts = int64(point.GetTimeUnixNano())
		sample.HistSum = point.GetSum()
		sample.HistCount = int64(point.GetCount())
		sample.LabelsJSON = attributesJSON(point.GetAttributes())
		sample.Kind = "histogram"
	default:
		return MetricSample{}, fmt.Errorf("metric %q has unsupported data type", metric.GetName())
	}
	return sample, nil
}

func numberDataPointValue(point *metricsv1.NumberDataPoint) float64 {
	switch value := point.Value.(type) {
	case *metricsv1.NumberDataPoint_AsDouble:
		return value.AsDouble
	case *metricsv1.NumberDataPoint_AsInt:
		return float64(value.AsInt)
	default:
		return 0
	}
}

func attributesJSON(attributes []*commonv1.KeyValue) string {
	values := make(map[string]any, len(attributes))
	for _, attribute := range attributes {
		values[attribute.GetKey()] = attributeValue(attribute.GetValue())
	}
	encoded, _ := json.Marshal(values)
	return string(encoded)
}

func attributeValue(value *commonv1.AnyValue) any {
	switch typed := value.GetValue().(type) {
	case *commonv1.AnyValue_StringValue:
		return typed.StringValue
	case *commonv1.AnyValue_IntValue:
		return typed.IntValue
	case *commonv1.AnyValue_BoolValue:
		return typed.BoolValue
	case *commonv1.AnyValue_DoubleValue:
		return typed.DoubleValue
	default:
		return value.String()
	}
}

type attributeTable map[string]*commonv1.AnyValue

func newAttributeTable(attributes []*commonv1.KeyValue) attributeTable {
	table := make(attributeTable, len(attributes))
	for _, attribute := range attributes {
		table[attribute.GetKey()] = attribute.GetValue()
	}
	return table
}

func (a attributeTable) string(key string) string {
	return a[key].GetStringValue()
}

func (a attributeTable) integer(key string) (int64, bool) {
	value, ok := a[key]
	if !ok {
		return 0, false
	}
	typed, ok := value.GetValue().(*commonv1.AnyValue_IntValue)
	if !ok {
		return 0, false
	}
	return typed.IntValue, true
}

func (a attributeTable) integerOrZero(key string) int64 {
	value, _ := a.integer(key)
	return value
}

func (a attributeTable) boolean(key string) bool {
	return a[key].GetBoolValue()
}
