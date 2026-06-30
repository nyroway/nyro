package admin

import (
	"time"

	"github.com/nyroway/nyro/go/internal/observability"
	"github.com/nyroway/nyro/go/internal/observability/parquet"
	"github.com/nyroway/nyro/go/internal/storage"
)

// parquetLogSource is the LogSource backed by the parquet observability store
// (observability.Logs), with a fallback to the legacy request_logs table
// (storage.Storage.Logs) during the dual-write migration window.
//
// Read rule (the dual-write fallback):
//   - Query:     read parquet; if it has rows OR there is no fallback, return it.
//                 If parquet is empty AND a fallback exists, read the old table.
//   - FindByID:  try parquet; on miss, fall back to the old table.
//   - ClearAll:  clear BOTH stores (the WebUI "clear" button empties every copy
//                 of the data during migration). Returns the sum of removed rows.
//
// nil-safety: parquetLogSource always has a non-nil *observability.Logs (the
// constructor builds one from dir). fallback may be nil.
type parquetLogSource struct {
	logs     *observability.Logs
	fallback storage.Storage
}

// NewParquetLogSource builds a LogSource that reads parquet files under dir and
// falls back to the legacy table in st when parquet is empty. Pass a nil st to
// disable the fallback (used by tests that want to isolate the parquet path).
//
// Exported so cmd/admin can construct the dual-write read source without
// reaching into internal helpers.
func NewParquetLogSource(dir string, fallback storage.Storage) LogSource {
	return &parquetLogSource{logs: observability.NewLogs(dir), fallback: fallback}
}

// Query reads the parquet store. On a non-empty result (or when there is no
// fallback) it returns the parquet page; when parquet is empty and a fallback
// exists, it reads the old table instead.
func (p *parquetLogSource) Query(q storage.LogQuery) (storage.LogPage, error) {
	page, err := p.logs.Query(toObsQuery(q))
	if err != nil {
		return storage.LogPage{}, err
	}
	if len(page.Items) > 0 || p.fallback == nil {
		return toStoragePage(page), nil
	}
	return p.fallback.Logs().Query(q) // dual-write fallback
}

// FindByID tries parquet first; on a miss it falls back to the old table so a
// row written only to request_logs (during dual-write) is still found.
func (p *parquetLogSource) FindByID(id string) (*storage.RequestLog, error) {
	rec, err := p.logs.FindByID(id)
	if err != nil {
		return nil, err
	}
	if rec != nil {
		rl := logRecordToRequestLog(*rec)
		return &rl, nil
	}
	if p.fallback == nil {
		return nil, nil
	}
	return p.fallback.Logs().FindByID(id)
}

// ClearAll empties BOTH stores during dual-write: parquet first (the new path),
// then the old request_logs table. The returned count is the sum of the two so
// the WebUI sees the total number of removed rows.
func (p *parquetLogSource) ClearAll() (int64, error) {
	n, err := p.logs.ClearAll()
	if err != nil {
		return 0, err
	}
	if p.fallback != nil {
		m, err := p.fallback.Logs().ClearAll()
		if err != nil {
			return 0, err
		}
		n += m
	}
	return n, nil
}

// queryLogs reads a page of logs. When src is non-nil it is the parquet-backed
// source (with old-table fallback); otherwise the legacy s.Logs() store is
// used. This is the read dispatch shared by GET /logs.
func queryLogs(src LogSource, s storage.Storage, q storage.LogQuery) (storage.LogPage, error) {
	if src != nil {
		return src.Query(q)
	}
	return s.Logs().Query(q)
}

// findLogByID looks up a single log row. When src is non-nil it is the
// parquet-backed source (which itself falls back to the old table on a miss);
// otherwise the legacy store is used. GET /logs/{id}.
func findLogByID(src LogSource, s storage.Storage, id string) (*storage.RequestLog, error) {
	if src != nil {
		return src.FindByID(id)
	}
	return s.Logs().FindByID(id)
}

// clearLogs empties the log store(s). When src is non-nil it clears BOTH the
// parquet store and the old table (the parquetLogSource ClearAll does both);
// otherwise it clears only the legacy store. DELETE /logs.
func clearLogs(src LogSource, s storage.Storage) (int64, error) {
	if src != nil {
		return src.ClearAll()
	}
	return s.Logs().ClearAll()
}

// toObsQuery converts the legacy storage.LogQuery into the observability
// facade's LogQuery (identical fields; the two structs mirror each other so the
// WebUI contract is unchanged).
func toObsQuery(q storage.LogQuery) observability.LogQuery {
	return observability.LogQuery{
		Limit:     q.Limit,
		Offset:    q.Offset,
		Provider:  q.Provider,
		Model:     q.Model,
		StatusMin: q.StatusMin,
		StatusMax: q.StatusMax,
	}
}

// toStoragePage converts an observability.LogPage into a storage.LogPage,
// mapping each LogRecord to a storage.RequestLog (field-for-field on the shared
// columns; columns the parquet schema does not capture stay zero-valued, so the
// JSON shape is preserved for the WebUI).
func toStoragePage(p observability.LogPage) storage.LogPage {
	items := make([]storage.RequestLog, len(p.Items))
	for i, r := range p.Items {
		items[i] = logRecordToRequestLog(r)
	}
	return storage.LogPage{Items: items, Total: p.Total}
}

// logRecordToRequestLog copies a parquet-backed LogRecord into the legacy
// storage.RequestLog shape. Every field present on LogRecord maps 1:1 onto the
// identically-named column of RequestLog; the header/body/stream-chunk columns
// that exist only on RequestLog are left zero (the parquet schema does not
// capture them).
func logRecordToRequestLog(r observability.LogRecord) storage.RequestLog {
	return storage.RequestLog{
		ID:                 r.ID,
		CreatedAt:          r.CreatedAt,
		APIKeyID:           r.APIKeyID,
		APIKeyName:         r.APIKeyName,
		ClientProtocol:     r.ClientProtocol,
		UpstreamProtocol:   r.UpstreamProtocol,
		ProviderID:         r.ProviderID,
		ProviderName:       r.ProviderName,
		ModelID:            r.ModelID,
		ModelName:          r.ModelName,
		ClientModel:        r.ClientModel,
		UpstreamModel:      r.UpstreamModel,
		Method:             r.Method,
		Path:               r.Path,
		ClientStatusCode:   r.ClientStatusCode,
		UpstreamStatusCode: r.UpstreamStatusCode,
		LatencyTotalMs:     r.LatencyTotalMs,
		LatencyUpstreamMs:  r.LatencyUpstreamMs,
		InputTokens:        r.InputTokens,
		OutputTokens:       r.OutputTokens,
		CacheReadTokens:    r.CacheReadTokens,
		IsStream:           r.IsStream,
	}
}

// ── /stats/* read source (T2.4) ──────────────────────────────────────────────

// parquetStatsSource is the StatsSource backed by the metrics parquet store,
// with a fallback to the legacy request_logs SQL aggregation during the
// dual-write migration window.
//
// Read rule (the dual-write fallback):
//   - Read metrics parquet within the hours window (ReadSince skips files whose
//     hour bucket is older than now-hours; rows are then filtered by Ts to honor
//     the exact same per-row cutoff the legacy store used).
//   - If samples are non-empty AND there was no read error: aggregate via
//     observability.AggregateStats / AggregateHourly and convert to storage.*.
//   - If samples are empty AND a fallback exists: call fallback.Logs().Stats*.
//   - hours<=0 means "all time" (no cutoff), matching the legacy store.
//
// nil-safety: parquetStatsSource always has a non-empty dir (the constructor
// stores it directly). fallback may be nil (tests / no-legacy deployments).
type parquetStatsSource struct {
	dir      string
	fallback storage.Storage
}

// NewParquetStatsSource builds a StatsSource that reads metrics parquet under
// dir and falls back to the legacy table in fallback when parquet is empty.
// Pass a nil fallback to disable the fallback (used by tests that want to
// isolate the parquet path).
//
// Exported so cmd/admin can construct the dual-write read source without
// reaching into internal helpers.
func NewParquetStatsSource(dir string, fallback storage.Storage) StatsSource {
	return &parquetStatsSource{dir: dir, fallback: fallback}
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

// parquetEmpty reports whether the metrics parquet has no in-window samples.
// The dual-write fallback fires exactly when this is true (and a fallback is
// wired): with no new-path data, /stats/* reads the old request_logs table.
func (p *parquetStatsSource) parquetEmpty(samples []observability.MetricSample) bool {
	return len(samples) == 0
}

// StatsOverview returns the dashboard summary. Reads metrics parquet; falls
// back to the legacy table when parquet is empty within the window.
func (p *parquetStatsSource) StatsOverview(hours int64) (storage.StatsOverview, error) {
	samples, _, err := p.readMetricSamples(hours)
	if err != nil {
		return storage.StatsOverview{}, err
	}
	if p.parquetEmpty(samples) && p.fallback != nil {
		return p.fallback.Logs().StatsOverview(hours)
	}
	ov, _, _, _, err := observability.AggregateStats(samples, hours)
	if err != nil {
		return storage.StatsOverview{}, err
	}
	return toStorageOverview(ov), nil
}

// StatsByModel returns per-model rollups.
func (p *parquetStatsSource) StatsByModel(hours int64) ([]storage.ModelStats, error) {
	samples, _, err := p.readMetricSamples(hours)
	if err != nil {
		return nil, err
	}
	if p.parquetEmpty(samples) && p.fallback != nil {
		return p.fallback.Logs().StatsByModel(hours)
	}
	_, models, _, _, err := observability.AggregateStats(samples, hours)
	if err != nil {
		return nil, err
	}
	out := make([]storage.ModelStats, len(models))
	for i, m := range models {
		out[i] = toStorageModelStats(m)
	}
	return out, nil
}

// StatsByProvider returns per-provider rollups.
func (p *parquetStatsSource) StatsByProvider(hours int64) ([]storage.ProviderStats, error) {
	samples, _, err := p.readMetricSamples(hours)
	if err != nil {
		return nil, err
	}
	if p.parquetEmpty(samples) && p.fallback != nil {
		return p.fallback.Logs().StatsByProvider(hours)
	}
	_, _, provs, _, err := observability.AggregateStats(samples, hours)
	if err != nil {
		return nil, err
	}
	out := make([]storage.ProviderStats, len(provs))
	for i, pr := range provs {
		out[i] = toStorageProviderStats(pr)
	}
	return out, nil
}

// StatsByApiKey returns per-api-key rollups.
func (p *parquetStatsSource) StatsByApiKey(hours int64) ([]storage.ApiKeyStats, error) {
	samples, _, err := p.readMetricSamples(hours)
	if err != nil {
		return nil, err
	}
	if p.parquetEmpty(samples) && p.fallback != nil {
		return p.fallback.Logs().StatsByApiKey(hours)
	}
	_, _, _, keys, err := observability.AggregateStats(samples, hours)
	if err != nil {
		return nil, err
	}
	out := make([]storage.ApiKeyStats, len(keys))
	for i, k := range keys {
		out[i] = toStorageApiKeyStats(k)
	}
	return out, nil
}

// StatsHourly returns per-UTC-hour rollups. Uses AggregateHourly (independent
// from the four AggregateStats shapes). Same fallback rule as the others.
func (p *parquetStatsSource) StatsHourly(hours int64) ([]storage.StatsHourly, error) {
	samples, _, err := p.readMetricSamples(hours)
	if err != nil {
		return nil, err
	}
	if p.parquetEmpty(samples) && p.fallback != nil {
		return p.fallback.Logs().StatsHourly(hours)
	}
	hourly, err := observability.AggregateHourly(samples, hours)
	if err != nil {
		return nil, err
	}
	out := make([]storage.StatsHourly, len(hourly))
	for i, h := range hourly {
		out[i] = toStorageStatsHourly(h)
	}
	return out, nil
}

// ── /stats/* read dispatch (T2.4) ────────────────────────────────────────────
//
// Each dispatch reads from the injected StatsSource when non-nil (the parquet
// path, which itself falls back to the old table when metrics parquet is
// empty); otherwise it reads s.Logs() (the legacy request_logs aggregation).
// Mirrors the queryLogs/findLogByID/clearLogs pattern used by /logs.

func statsOverview(src StatsSource, s storage.Storage, hours int64) (storage.StatsOverview, error) {
	if src != nil {
		return src.StatsOverview(hours)
	}
	return s.Logs().StatsOverview(hours)
}

func statsByModel(src StatsSource, s storage.Storage, hours int64) ([]storage.ModelStats, error) {
	if src != nil {
		return src.StatsByModel(hours)
	}
	return s.Logs().StatsByModel(hours)
}

func statsByProvider(src StatsSource, s storage.Storage, hours int64) ([]storage.ProviderStats, error) {
	if src != nil {
		return src.StatsByProvider(hours)
	}
	return s.Logs().StatsByProvider(hours)
}

func statsByApiKey(src StatsSource, s storage.Storage, hours int64) ([]storage.ApiKeyStats, error) {
	if src != nil {
		return src.StatsByApiKey(hours)
	}
	return s.Logs().StatsByApiKey(hours)
}

func statsHourly(src StatsSource, s storage.Storage, hours int64) ([]storage.StatsHourly, error) {
	if src != nil {
		return src.StatsHourly(hours)
	}
	return s.Logs().StatsHourly(hours)
}

// ── observability.Stats* → storage.Stats* conversions ────────────────────────
//
// The two type families are field-for-field identical (the observability copies
// exist only until Phase 4 removes the storage copies). These helpers keep the
// mapping explicit and in one place.

func toStorageOverview(o observability.StatsOverview) storage.StatsOverview {
	return storage.StatsOverview{
		TotalRequests:     o.TotalRequests,
		TotalInputTokens:  o.TotalInputTokens,
		TotalOutputTokens: o.TotalOutputTokens,
		AvgDurationMs:     o.AvgDurationMs,
		ErrorCount:        o.ErrorCount,
	}
}

func toStorageModelStats(m observability.ModelStats) storage.ModelStats {
	return storage.ModelStats{
		Model:             m.Model,
		RequestCount:      m.RequestCount,
		TotalInputTokens:  m.TotalInputTokens,
		TotalOutputTokens: m.TotalOutputTokens,
		AvgDurationMs:     m.AvgDurationMs,
	}
}

func toStorageProviderStats(p observability.ProviderStats) storage.ProviderStats {
	return storage.ProviderStats{
		Provider:      p.Provider,
		RequestCount:  p.RequestCount,
		ErrorCount:    p.ErrorCount,
		AvgDurationMs: p.AvgDurationMs,
	}
}

func toStorageApiKeyStats(k observability.ApiKeyStats) storage.ApiKeyStats {
	return storage.ApiKeyStats{
		APIKeyID:          k.APIKeyID,
		APIKeyName:        k.APIKeyName,
		RequestCount:      k.RequestCount,
		TotalInputTokens:  k.TotalInputTokens,
		TotalOutputTokens: k.TotalOutputTokens,
		CacheReadTokens:   k.CacheReadTokens,
		// MetricSample.Ts (and thus ApiKeyStats.LastUsedAt from AggregateStats)
		// is unix-nanoseconds; the legacy storage.ApiKeyStats.LastUsedAt contract
		// is unix-milliseconds (it comes from request_logs.created_at =
		// started.UnixMilli()). Normalize at this boundary so the WebUI sees the
		// same unit it always has.
		LastUsedAt: k.LastUsedAt / 1_000_000,
	}
}

func toStorageStatsHourly(h observability.StatsHourly) storage.StatsHourly {
	return storage.StatsHourly{
		Hour:              h.Hour,
		RequestCount:      h.RequestCount,
		ErrorCount:        h.ErrorCount,
		TotalInputTokens:  h.TotalInputTokens,
		TotalOutputTokens: h.TotalOutputTokens,
		AvgDurationMs:     h.AvgDurationMs,
	}
}
