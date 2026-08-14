package quota

import (
	"context"
	"errors"
	"time"
)

// ErrUnavailable reports that quota State cannot safely serve operations.
var ErrUnavailable = errors.New("quota state unavailable")

const (
	// BucketResolution is the precision of usage windows.
	BucketResolution = time.Minute
	// MaxWindow is the maximum effective usage history.
	MaxWindow = 24 * time.Hour
)

// Usage is the completed usage recorded atomically for one exchange.
type Usage struct {
	Requests int64
	Tokens   int64
}

// Lease reserves one concurrency slot until released.
type Lease interface {
	Release(context.Context) error
}

// Store is the State contract required by quota enforcement.
type Store interface {
	Value(context.Context, string, string, time.Duration) (int64, error)
	Record(context.Context, string, Usage) error
	Acquire(context.Context, string, int64, time.Duration) (Lease, bool, error)
}
