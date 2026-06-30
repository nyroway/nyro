package observability

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

// SignalRetention gives per-signal retention, in days. A signal dir is cleaned
// of files whose ModTime is older than now - Days. Days<=0 disables cleaning
// for that signal.
type SignalRetention struct {
	Logs, Metrics, Traces int // days; <=0 → skip that signal
}

// StartJanitor launches a background goroutine that, every period, sweeps the
// three signal dirs and removes parquet files older than their retention window.
// period<=0 defaults to an hour. The goroutine exits when ctx is cancelled.
// Janitor retention is based on file ModTime, independent of ReadSince's
// filename-hour filtering.
func StartJanitor(ctx context.Context, dir string, rt SignalRetention, period time.Duration) {
	if period <= 0 {
		period = time.Hour
	}
	go func() {
		t := time.NewTicker(period)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				now := time.Now().UTC()
				cleanSignal(dir, "logs", rt.Logs, now)
				cleanSignal(dir, "metrics", rt.Metrics, now)
				cleanSignal(dir, "traces", rt.Traces, now)
			}
		}
	}()
}

// cleanSignal removes every <dir>/<signal>/*.parquet file whose ModTime is
// before now-Days. days<=0 is a no-op (signal disabled). Errors stat'ing or
// removing individual files are logged and skipped so one bad file does not
// abort the sweep.
func cleanSignal(dir, signal string, days int, now time.Time) {
	if days <= 0 {
		return
	}
	cutoff := now.AddDate(0, 0, -days)
	matches, _ := filepath.Glob(filepath.Join(dir, signal, "*.parquet"))
	for _, f := range matches {
		info, err := os.Stat(f)
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			if err := os.Remove(f); err == nil {
				slog.Info("obs janitor removed file", "path", f)
			}
		}
	}
}
