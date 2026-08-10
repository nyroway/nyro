package otlphttp

import (
	"context"
	"errors"

	"github.com/nyroway/nyro/go/internal/platform/observe"
)

func (r *Receiver) run(ctx context.Context) {
	defer close(r.done)
	defer r.cancel()
	for {
		item, err := r.queue.pop(ctx)
		if err != nil {
			if errors.Is(err, errQueueClosed) || errors.Is(err, context.Canceled) {
				return
			}
			continue
		}
		items := []queuedExport{item}
		if r.flushBatch > 1 {
			batchContext, cancel := context.WithTimeout(ctx, r.flushInterval)
			for len(items) < r.flushBatch {
				next, err := r.queue.pop(batchContext)
				if err != nil {
					break
				}
				items = append(items, next)
			}
			cancel()
		}
		requests := make([]observe.ExportRequest, len(items))
		for index := range items {
			requests[index] = items[index].request
		}
		if err := r.store.Append(ctx, requests); err != nil {
			r.failed.Add(uint64(len(requests)))
			if r.onPersistError != nil {
				r.onPersistError(err)
			}
		} else {
			r.persisted.Add(uint64(len(requests)))
		}
		if ctx.Err() != nil {
			return
		}
	}
}
