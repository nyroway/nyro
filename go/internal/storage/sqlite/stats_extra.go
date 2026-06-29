package sqlite

import (
	"time"

	"github.com/nyroway/nyro/go/internal/storage"
)

// statsCutoffMs returns the created_at millisecond cutoff for a hours window,
// or 0 (no filter) when hours <= 0.
func statsCutoffMs(hours int64) int64 {
	if hours <= 0 {
		return 0
	}
	return time.Now().UnixMilli() - hours*3_600_000
}

func (s logStore) StatsByModel(hours int64) ([]storage.ModelStats, error) {
	var out []storage.ModelStats
	q := s.b.db.Model(&storage.RequestLog{}).
		Where("model_name IS NOT NULL AND model_name != ''")
	if cutoff := statsCutoffMs(hours); cutoff > 0 {
		q = q.Where("created_at >= ?", cutoff)
	}
	q.Select("model_name AS model, COUNT(*) AS request_count, COALESCE(SUM(input_tokens),0) AS total_input_tokens, COALESCE(SUM(output_tokens),0) AS total_output_tokens, COALESCE(AVG(latency_total_ms),0) AS avg_duration_ms").
		Group("model_name").
		Order("request_count DESC").
		Scan(&out)
	return out, nil
}

func (s logStore) StatsByProvider(hours int64) ([]storage.ProviderStats, error) {
	var out []storage.ProviderStats
	q := s.b.db.Model(&storage.RequestLog{}).
		Where("provider_name IS NOT NULL AND provider_name != ''")
	if cutoff := statsCutoffMs(hours); cutoff > 0 {
		q = q.Where("created_at >= ?", cutoff)
	}
	q.Select("provider_name AS provider, COUNT(*) AS request_count, COALESCE(SUM(CASE WHEN client_status_code >= 400 THEN 1 ELSE 0 END),0) AS error_count, COALESCE(AVG(latency_total_ms),0) AS avg_duration_ms").
		Group("provider_name").
		Order("request_count DESC").
		Scan(&out)
	return out, nil
}

func (s logStore) StatsByApiKey(hours int64) ([]storage.ApiKeyStats, error) {
	var out []storage.ApiKeyStats
	q := s.b.db.Model(&storage.RequestLog{}).
		Where("api_key_id IS NOT NULL AND api_key_id != ''")
	if cutoff := statsCutoffMs(hours); cutoff > 0 {
		q = q.Where("created_at >= ?", cutoff)
	}
	q.Select("api_key_id AS api_key_id, COALESCE(NULLIF(api_key_name,''), api_key_id) AS api_key_name, COUNT(*) AS request_count, COALESCE(SUM(input_tokens),0) AS total_input_tokens, COALESCE(SUM(output_tokens),0) AS total_output_tokens, COALESCE(SUM(cache_read_tokens),0) AS cache_read_tokens, MAX(created_at) AS last_used_at").
		Group("api_key_id, api_key_name").
		Order("request_count DESC").
		Scan(&out)
	return out, nil
}

func (s logStore) StatsHourly(hours int64) ([]storage.StatsHourly, error) {
	cutoff := time.Now().UnixMilli() - hours*3_600_000
	var out []storage.StatsHourly
	s.b.db.Raw(`SELECT strftime('%Y-%m-%dT%H:00:00Z', created_at/1000, 'unixepoch') AS hour,
		COUNT(*) AS request_count,
		COALESCE(SUM(CASE WHEN client_status_code >= 400 THEN 1 ELSE 0 END),0) AS error_count,
		COALESCE(SUM(input_tokens),0) AS total_input_tokens,
		COALESCE(SUM(output_tokens),0) AS total_output_tokens,
		COALESCE(AVG(latency_total_ms),0) AS avg_duration_ms
		FROM request_logs WHERE created_at >= ?
		GROUP BY hour ORDER BY hour`, cutoff).Scan(&out)
	return out, nil
}

func (s logStore) DeleteBefore(cutoffMs int64) (int64, error) {
	res := s.b.db.Where("created_at < ?", cutoffMs).Delete(&storage.RequestLog{})
	return res.RowsAffected, res.Error
}
