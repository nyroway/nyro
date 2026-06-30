package admin

import (
	"time"

	"github.com/nyroway/nyro/go/internal/observability"
	"github.com/nyroway/nyro/go/internal/observability/parquet"
)

// parquetLogSource is the LogSource backed by the parquet observability store
// (observability.Logs). It is the ONLY request-log store after the Phase 4
// removal of the request_logs table — the dual-write fallback that lived here
// through Phase 3 is gone.
type parquetLogSource struct {
	logs *observability.Logs
}

// NewParquetLogSource builds a LogSource that reads parquet files under dir.
// Exported so cmd/admin can construct the read source.
func NewParquetLogSource(dir string) LogSource {
	return &parquetLogSource{logs: observability.NewLogs(dir)}
}

func (p *parquetLogSource) Query(q observability.LogQuery) (observability.LogPage, error) {
	return p.logs.Query(q)
}

func (p *parquetLogSource) FindByID(id string) (*observability.LogRecord, error) {
	return p.logs.FindByID(id)
}

func (p *parquetLogSource) ClearAll() (int64, error) {
	return p.logs.ClearAll()
}

// ── /stats/* read source ────────────────────────────────────────────────────

// parquetStatsSource is the StatsSource backed by the metrics parquet store.
// It is the ONLY stats path after the Phase 4 removal of the request_logs
// table — the dual-write fallback that lived here through Phase 3 is gone.
type parquetStatsSource struct {
	dir string
}

// NewParquetStatsSource builds a StatsSource that reads metrics parquet under
// dir. Exported so cmd/admin can construct the read source.
func NewParquetStatsSource(dir string) StatsSource {
	return &parquetStatsSource{dir: dir}
}

// readMetricSamples reads the metrics parquet within the hours window. hours<=0
// reads everything. The returned cutoff is the per-row nanosecond cutoff (0 when
// unfiltered) so callers can drop rows whose Ts predates the window (ReadSince
// only filters at hour-bucket granularity).
//
// Unit note: the production receiver writes MetricSample.Ts as unix-nanoseconds
// (OTLP p.GetTimeUnixNano), so the per-row cutoff MUST be nanoseconds too. A
// milli cutoff compared against nano Ts is always-true and filters nothing.
func (p *parquetStatsSource) readMetricSamples(hours int64) ([]observability.MetricSample, int64, error) {
	var sinceHour int64
	cutoffNs := int64(0)
	if hours > 0 {
		cutoffNs = time.Now().Add(-time.Duration(hours) * time.Hour).UnixNano()
		// ReadSince compares against the file's hour bucket (the hour's unix-nano,
		// as parsed from the file name). Align the cutoff down to its hour index
		// (in nanos) so files whose whole hour is older than the window are
		// skipped, while the boundary hour (which may contain in-window rows) is
		// still read.
		sinceHour = (cutoffNs / int64(time.Hour)) * int64(time.Hour)
	}
	samples, err := parquet.ReadSince[observability.MetricSample](p.dir, "metrics", sinceHour)
	if err != nil {
		return nil, 0, err
	}
	if cutoffNs > 0 {
		filtered := samples[:0]
		for _, s := range samples {
			if s.Ts >= cutoffNs {
				filtered = append(filtered, s)
			}
		}
		samples = filtered
	}
	return samples, cutoffNs, nil
}

func (p *parquetStatsSource) StatsOverview(hours int64) (observability.StatsOverview, error) {
	samples, _, err := p.readMetricSamples(hours)
	if err != nil {
		return observability.StatsOverview{}, err
	}
	ov, _, _, _, err := observability.AggregateStats(samples, hours)
	return ov, err
}

func (p *parquetStatsSource) StatsByModel(hours int64) ([]observability.ModelStats, error) {
	samples, _, err := p.readMetricSamples(hours)
	if err != nil {
		return nil, err
	}
	_, models, _, _, err := observability.AggregateStats(samples, hours)
	return models, err
}

func (p *parquetStatsSource) StatsByProvider(hours int64) ([]observability.ProviderStats, error) {
	samples, _, err := p.readMetricSamples(hours)
	if err != nil {
		return nil, err
	}
	_, _, provs, _, err := observability.AggregateStats(samples, hours)
	return provs, err
}

// StatsByApiKey returns per-api-key rollups. The MetricSample.Ts that backs
// ApiKeyStats.LastUsedAt is unix-nanoseconds (the OTLP write unit), but the
// WebUI contract for last_used_at is unix-milliseconds (it always was, when the
// value came from request_logs.created_at = started.UnixMilli()). Normalize at
// this boundary so the unit the WebUI has always seen is preserved.
func (p *parquetStatsSource) StatsByApiKey(hours int64) ([]observability.ApiKeyStats, error) {
	samples, _, err := p.readMetricSamples(hours)
	if err != nil {
		return nil, err
	}
	_, _, _, keys, err := observability.AggregateStats(samples, hours)
	if err != nil {
		return nil, err
	}
	for i := range keys {
		keys[i].LastUsedAt /= 1_000_000
	}
	return keys, nil
}

func (p *parquetStatsSource) StatsHourly(hours int64) ([]observability.StatsHourly, error) {
	samples, _, err := p.readMetricSamples(hours)
	if err != nil {
		return nil, err
	}
	return observability.AggregateHourly(samples, hours)
}
