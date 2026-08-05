package otlphttp

import (
	"context"
	"errors"

	"github.com/nyroway/nyro/go/infra/observe"
	"github.com/nyroway/nyro/go/infra/observe/internal/queue"
)

func (r *Receiver) run(ctx context.Context) {
	defer close(r.done)
	defer r.cancel()
	for {
		item, err := r.queue.Pop(ctx)
		if err != nil {
			if errors.Is(err, queue.ErrClosed) || errors.Is(err, context.Canceled) {
				return
			}
			continue
		}
		items := []queue.Item{item}
		if r.flushBatch > 1 {
			batchContext, cancel := context.WithTimeout(ctx, r.flushInterval)
			for len(items) < r.flushBatch {
				next, err := r.queue.Pop(batchContext)
				if err != nil {
					break
				}
				items = append(items, next)
			}
			cancel()
		}
		requests := make([]observe.ExportRequest, len(items))
		for index := range items {
			requests[index] = items[index].Value.(observe.ExportRequest)
		}
		if err := r.store.Append(ctx, requests); err != nil {
			r.failed.Add(uint64(len(requests)))
		} else {
			r.persisted.Add(uint64(len(requests)))
		}
		if ctx.Err() != nil {
			return
		}
	}
}
