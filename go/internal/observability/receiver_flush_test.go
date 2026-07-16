package observability

import (
	"context"
	"testing"
	"time"

	"github.com/nyroway/nyro/go/internal/observability/parquet"
)

// TestStartFlusher_TimeTriggerFlushesBufferedRows proves the time trigger: a row
// written to a sink stays buffered (invisible to a file-based read) until either
// the size trigger (maxRows) or StartFlusher's periodic flush fires. With a large
// maxRows the size trigger never fires, so only the flusher can make the row
// queryable — reproducing and then fixing the "WebUI empty on low traffic" bug.
func TestStartFlusher_TimeTriggerFlushesBufferedRows(t *testing.T) {
	dir := t.TempDir()
	logSink, err := parquet.NewSink[LogRecord](dir, "logs", 50000)
	if err != nil {
		t.Fatal(err)
	}
	mSink, _ := parquet.NewSink[MetricSample](dir, "metrics", 50000)
	tSink, _ := parquet.NewSink[SpanSnapshot](dir, "traces", 50000)
	rcv := NewReceiver(logSink, mSink, tSink)

	if err := logSink.Write([]LogRecord{{ID: "req_x", CreatedAt: time.Now().UnixMilli()}}); err != nil {
		t.Fatal(err)
	}

	logs := NewLogs(dir)
	// Before any flush: the row is buffered in memory, so a file-based read sees
	// nothing (this is exactly why the WebUI was empty).
	if p, _ := logs.Query(LogQuery{}); p.Total != 0 {
		t.Fatalf("expected 0 rows before flush (buffered), got %d", p.Total)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rcv.StartFlusher(ctx, SignalFlush{Logs: 30 * time.Millisecond})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if p, _ := logs.Query(LogQuery{}); p.Total == 1 {
			return // flusher made the buffered row queryable — success
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("row never became queryable: StartFlusher did not flush the buffered log within 2s")
}
