package sqlite

import (
	"context"
	"log/slog"
	"time"
)

const (
	defaultCleanupInterval = time.Minute
	defaultCleanupBatch    = 1000
)

func (s *Store) startJanitor(parent context.Context, interval time.Duration, batchSize int) {
	if interval < 0 {
		close(s.done)
		return
	}
	if interval == 0 {
		interval = defaultCleanupInterval
	}
	if batchSize <= 0 {
		batchSize = defaultCleanupBatch
	}
	ctx, cancel := context.WithCancel(parent)
	s.cancel = cancel
	go func() {
		defer close(s.done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := s.deleteExpired(ctx, batchSize); err != nil && ctx.Err() == nil {
					slog.Warn("state sqlite expiration cleanup failed", "error", err)
				}
			}
		}
	}()
}

func (s *Store) deleteExpired(ctx context.Context, batchSize int) error {
	s.write.Lock()
	defer s.write.Unlock()
	_, err := s.db.ExecContext(ctx, `
DELETE FROM state_kv
WHERE rowid IN (
    SELECT rowid FROM state_kv
    WHERE expires_at_ms IS NOT NULL AND expires_at_ms <= ?
    ORDER BY expires_at_ms
    LIMIT ?
)
`, s.now().UnixMilli(), batchSize)
	return err
}
