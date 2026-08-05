package queue_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nyroway/nyro/go/infra/observe/internal/queue"
)

func TestQueueEnforcesBatchAndByteLimitsAndPreservesFIFO(t *testing.T) {
	q, err := queue.New(2, 5)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := q.Push(queue.Item{Value: "first", Bytes: 2}); err != nil {
		t.Fatalf("first Push() error = %v", err)
	}
	if err := q.Push(queue.Item{Value: "second", Bytes: 3}); err != nil {
		t.Fatalf("second Push() error = %v", err)
	}
	if err := q.Push(queue.Item{Value: "third", Bytes: 0}); !errors.Is(err, queue.ErrFull) {
		t.Fatalf("third Push() error = %v, want ErrFull", err)
	}
	first, err := q.Pop(context.Background())
	if err != nil || first.Value != "first" {
		t.Fatalf("first Pop() = %#v, %v", first, err)
	}
	if err := q.Push(queue.Item{Value: "too-large", Bytes: 3}); !errors.Is(err, queue.ErrFull) {
		t.Fatalf("byte-limited Push() error = %v, want ErrFull", err)
	}
	if err := q.Push(queue.Item{Value: "third", Bytes: 2}); err != nil {
		t.Fatalf("third Push() error = %v", err)
	}
	drained := q.Drain(10)
	if len(drained) != 2 || drained[0].Value != "second" || drained[1].Value != "third" {
		t.Fatalf("Drain() = %#v", drained)
	}
	if batches, bytes := q.Size(); batches != 0 || bytes != 0 {
		t.Fatalf("Size() = %d, %d, want empty", batches, bytes)
	}
}

func TestQueuePopCancellationAndClose(t *testing.T) {
	q, err := queue.New(1, 1)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := q.Pop(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Pop() error = %v, want deadline exceeded", err)
	}
	q.Close()
	if err := q.Push(queue.Item{Value: "late", Bytes: 1}); !errors.Is(err, queue.ErrClosed) {
		t.Fatalf("Push() after Close error = %v, want ErrClosed", err)
	}
	if _, err := q.Pop(context.Background()); !errors.Is(err, queue.ErrClosed) {
		t.Fatalf("Pop() after Close error = %v, want ErrClosed", err)
	}
}

func TestQueueCloseAllowsBufferedItemsToDrain(t *testing.T) {
	q, _ := queue.New(2, 2)
	_ = q.Push(queue.Item{Value: 1, Bytes: 1})
	q.Close()
	item, err := q.Pop(context.Background())
	if err != nil || item.Value != 1 {
		t.Fatalf("Pop() buffered item = %#v, %v", item, err)
	}
	if _, err := q.Pop(context.Background()); !errors.Is(err, queue.ErrClosed) {
		t.Fatalf("final Pop() error = %v, want ErrClosed", err)
	}
}
