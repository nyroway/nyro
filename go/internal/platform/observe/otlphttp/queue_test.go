package otlphttp

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nyroway/nyro/go/internal/platform/observe"
)

func TestExportQueueEnforcesLimitsAndPreservesFIFO(t *testing.T) {
	q, err := newExportQueue(2, 5)
	if err != nil {
		t.Fatalf("newExportQueue() error = %v", err)
	}
	first := queuedExport{request: observe.ExportRequest{ReceivedAt: time.Unix(1, 0)}, bytes: 2}
	second := queuedExport{request: observe.ExportRequest{ReceivedAt: time.Unix(2, 0)}, bytes: 3}
	if err := q.push(first); err != nil {
		t.Fatalf("first push() error = %v", err)
	}
	if err := q.push(second); err != nil {
		t.Fatalf("second push() error = %v", err)
	}
	if err := q.push(queuedExport{}); !errors.Is(err, errQueueFull) {
		t.Fatalf("third push() error = %v, want errQueueFull", err)
	}
	got, err := q.pop(context.Background())
	if err != nil || !got.request.ReceivedAt.Equal(first.request.ReceivedAt) {
		t.Fatalf("first pop() = %#v, %v", got, err)
	}
	if err := q.push(queuedExport{bytes: 3}); !errors.Is(err, errQueueFull) {
		t.Fatalf("byte-limited push() error = %v, want errQueueFull", err)
	}
	if batches, bytes := q.size(); batches != 1 || bytes != 3 {
		t.Fatalf("size() = %d, %d, want 1, 3", batches, bytes)
	}
}

func TestExportQueueCancellationAndClose(t *testing.T) {
	q, err := newExportQueue(1, 1)
	if err != nil {
		t.Fatalf("newExportQueue() error = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := q.pop(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("pop() error = %v, want deadline exceeded", err)
	}
	if err := q.push(queuedExport{bytes: 1}); err != nil {
		t.Fatalf("push() error = %v", err)
	}
	q.close()
	if err := q.push(queuedExport{}); !errors.Is(err, errQueueClosed) {
		t.Fatalf("push() after close error = %v, want errQueueClosed", err)
	}
	if _, err := q.pop(context.Background()); err != nil {
		t.Fatalf("pop() buffered item after close error = %v", err)
	}
	if _, err := q.pop(context.Background()); !errors.Is(err, errQueueClosed) {
		t.Fatalf("final pop() error = %v, want errQueueClosed", err)
	}
}
