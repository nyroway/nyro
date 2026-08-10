package admin

import (
	"context"
	"math"
	"time"

	"github.com/nyroway/nyro/go/internal/observability"
	"github.com/nyroway/nyro/go/internal/platform/observe"
)

// ObserveSource projects the generic Observe store into Nyro's Admin DTOs.
type ObserveSource struct {
	store observe.Store
	now   func() time.Time
}

// NewObserveSource returns the shared log and stats source for Admin routes.
func NewObserveSource(store observe.Store) *ObserveSource {
	return &ObserveSource{store: store, now: time.Now}
}

func stringFilter(key, value string) observe.AttributeFilter {
	return observe.AttributeFilter{Key: key, StringEquals: &value}
}

func (s *ObserveSource) Query(ctx context.Context, query observability.LogQuery) (observability.LogPage, error) {
	limit := int(query.Limit)
	if limit <= 0 {
		limit = 50
	}
	filters := make([]observe.AttributeFilter, 0, 6)
	if query.UpstreamID != "" {
		filters = append(filters, stringFilter("nyro.upstream.id", query.UpstreamID))
	}
	if query.RouteID != "" {
		filters = append(filters, stringFilter("nyro.route.id", query.RouteID))
	}
	if query.RouteModel != "" {
		filters = append(filters, stringFilter("nyro.route.model", query.RouteModel))
	}
	if query.ConsumerID != "" {
		filters = append(filters, stringFilter("nyro.consumer.id", query.ConsumerID))
	}
	if query.StatusMin != nil || query.StatusMax != nil {
		filter := observe.AttributeFilter{Key: "http.response.status_code"}
		if query.StatusMin != nil {
			value := int64(*query.StatusMin)
			filter.IntMin = &value
		}
		if query.StatusMax != nil {
			value := int64(*query.StatusMax)
			filter.IntMax = &value
		}
		filters = append(filters, filter)
	}
	page, err := s.store.QueryLogs(ctx, observe.LogQuery{
		Attributes: filters, Limit: limit, Offset: int(query.Offset), IncludeTotal: true,
	})
	if err != nil {
		return observability.LogPage{}, err
	}
	items := make([]observability.LogRecord, 0, len(page.Records))
	for _, record := range page.Records {
		items = append(items, observability.LogRecordFromOTLP(record.Record))
	}
	return observability.LogPage{Items: items, Total: page.Total}, nil
}

func (s *ObserveSource) FindByID(ctx context.Context, id string) (*observability.LogRecord, error) {
	page, err := s.store.QueryLogs(ctx, observe.LogQuery{
		Attributes: []observe.AttributeFilter{stringFilter("nyro.log.id", id)}, Limit: 1,
	})
	if err != nil || len(page.Records) == 0 {
		return nil, err
	}
	record := observability.LogRecordFromOTLP(page.Records[0].Record)
	return &record, nil
}

func (s *ObserveSource) ClearAll(ctx context.Context) (int64, error) {
	var total int64
	cutoff := time.Unix(0, math.MaxInt64)
	for {
		deleted, err := s.store.DeleteBefore(ctx, observe.SignalLogs, cutoff, 10_000)
		if err != nil {
			return total, err
		}
		total += deleted
		if deleted == 0 {
			return total, nil
		}
	}
}

func (s *ObserveSource) readMetricSamples(ctx context.Context, hours int64) ([]observability.MetricSample, error) {
	query := observe.MetricQuery{Limit: 1000}
	if hours > 0 {
		query.Start = s.now().Add(-time.Duration(hours) * time.Hour)
	}
	names := []string{"nyro_requests_total", "nyro_tokens_total", "nyro_request_latency_ms"}
	var samples []observability.MetricSample
	for _, name := range names {
		query.Name = name
		query.Cursor = ""
		for {
			page, err := s.store.QueryMetrics(ctx, query)
			if err != nil {
				return nil, err
			}
			for _, record := range page.Records {
				sample, err := observability.MetricSampleFromOTLP(record.Metric, record.PointType, record.DataPointIndex)
				if err != nil {
					return nil, err
				}
				samples = append(samples, sample)
			}
			if page.NextCursor == "" {
				break
			}
			query.Cursor = page.NextCursor
		}
	}
	return samples, nil
}

func (s *ObserveSource) aggregate(ctx context.Context, hours int64) (observability.StatsOverview, []observability.RouteStats, []observability.UpstreamStats, []observability.ConsumerStats, error) {
	samples, err := s.readMetricSamples(ctx, hours)
	if err != nil {
		return observability.StatsOverview{}, nil, nil, nil, err
	}
	return observability.AggregateStats(samples, hours)
}

func (s *ObserveSource) StatsOverview(ctx context.Context, hours int64) (observability.StatsOverview, error) {
	overview, _, _, _, err := s.aggregate(ctx, hours)
	return overview, err
}

func (s *ObserveSource) StatsByRoute(ctx context.Context, hours int64) ([]observability.RouteStats, error) {
	_, routes, _, _, err := s.aggregate(ctx, hours)
	return routes, err
}

func (s *ObserveSource) StatsByUpstream(ctx context.Context, hours int64) ([]observability.UpstreamStats, error) {
	_, _, upstreams, _, err := s.aggregate(ctx, hours)
	return upstreams, err
}

func (s *ObserveSource) StatsByConsumer(ctx context.Context, hours int64) ([]observability.ConsumerStats, error) {
	_, _, _, consumers, err := s.aggregate(ctx, hours)
	for i := range consumers {
		consumers[i].LastUsedAt /= int64(time.Millisecond)
	}
	return consumers, err
}

func (s *ObserveSource) StatsHourly(ctx context.Context, hours int64) ([]observability.StatsHourly, error) {
	samples, err := s.readMetricSamples(ctx, hours)
	if err != nil {
		return nil, err
	}
	return observability.AggregateHourly(samples, hours)
}
