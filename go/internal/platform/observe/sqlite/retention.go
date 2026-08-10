package sqlite

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/nyroway/nyro/go/internal/platform/observe"
)

const (
	defaultLogsRetention     = 7 * 24 * time.Hour
	defaultMetricsRetention  = 30 * 24 * time.Hour
	defaultTracesRetention   = 3 * 24 * time.Hour
	defaultRetentionInterval = time.Hour
	defaultRetentionBatch    = 1000
	maxRetentionBatch        = 10000
)

type retentionOptions struct {
	logs, metrics, traces time.Duration
	interval              time.Duration
	batch                 int
}

func normalizeRetention(opts Options) retentionOptions {
	logs := opts.LogsRetention
	if logs == 0 {
		logs = defaultLogsRetention
	}
	metrics := opts.MetricsRetention
	if metrics == 0 {
		metrics = defaultMetricsRetention
	}
	traces := opts.TracesRetention
	if traces == 0 {
		traces = defaultTracesRetention
	}
	interval := opts.RetentionInterval
	if interval == 0 {
		interval = defaultRetentionInterval
	}
	batch := opts.RetentionBatch
	if batch <= 0 {
		batch = defaultRetentionBatch
	}
	if batch > maxRetentionBatch {
		batch = maxRetentionBatch
	}
	return retentionOptions{logs: logs, metrics: metrics, traces: traces, interval: interval, batch: batch}
}

// DeleteBefore deletes at most limit received batches for one signal. Associated
// coordinate indexes are deleted by SQLite foreign-key cascades.
func (s *Store) DeleteBefore(ctx context.Context, signal observe.Signal, cutoff time.Time, limit int) (int64, error) {
	switch signal {
	case observe.SignalLogs, observe.SignalMetrics, observe.SignalTraces:
	default:
		return 0, errors.New("observe sqlite: invalid retention signal")
	}
	if limit <= 0 {
		limit = defaultRetentionBatch
	}
	if limit > maxRetentionBatch {
		limit = maxRetentionBatch
	}
	s.write.Lock()
	defer s.write.Unlock()
	result, err := s.db.ExecContext(ctx, `
DELETE FROM otlp_batches
WHERE id IN (
    SELECT id FROM otlp_batches
    WHERE signal = ? AND received_at_ns < ?
    ORDER BY received_at_ns, id
    LIMIT ?
)`, string(signal), cutoff.UnixNano(), limit)
	if err != nil {
		return 0, fmt.Errorf("observe sqlite: delete retained batches: %w", err)
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("observe sqlite: retention rows affected: %w", err)
	}
	return deleted, nil
}

func (s *Store) runRetention(ctx context.Context) {
	defer close(s.done)
	ticker := time.NewTicker(s.retention.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.cleanupRetention(ctx)
		}
	}
}

func (s *Store) cleanupRetention(ctx context.Context) {
	now := s.now()
	retentions := []struct {
		signal   observe.Signal
		duration time.Duration
	}{
		{observe.SignalLogs, s.retention.logs},
		{observe.SignalMetrics, s.retention.metrics},
		{observe.SignalTraces, s.retention.traces},
	}
	for _, item := range retentions {
		if item.duration <= 0 {
			continue
		}
		_, _ = s.DeleteBefore(ctx, item.signal, now.Add(-item.duration), s.retention.batch)
	}
}
