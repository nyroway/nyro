package sqlite

import (
	"time"

	"github.com/nyroway/nyro/go/internal/storage"
)

func (s logStore) StatsByModel() ([]storage.ModelStats, error) {
	var out []storage.ModelStats
	s.b.db.Model(&storage.RequestLog{}).
		Where("model_name IS NOT NULL AND model_name != ''").
		Select("model_name AS model, COUNT(*) AS request_count, COALESCE(SUM(input_tokens),0) AS total_input_tokens, COALESCE(SUM(output_tokens),0) AS total_output_tokens, COALESCE(AVG(latency_total_ms),0) AS avg_duration_ms").
		Group("model_name").
		Order("request_count DESC").
		Scan(&out)
	return out, nil
}

func (s logStore) StatsByProvider() ([]storage.ProviderStats, error) {
	var out []storage.ProviderStats
	s.b.db.Model(&storage.RequestLog{}).
		Where("provider_name IS NOT NULL AND provider_name != ''").
		Select("provider_name AS provider, COUNT(*) AS request_count, COALESCE(SUM(CASE WHEN client_status_code >= 400 THEN 1 ELSE 0 END),0) AS error_count, COALESCE(AVG(latency_total_ms),0) AS avg_duration_ms").
		Group("provider_name").
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
