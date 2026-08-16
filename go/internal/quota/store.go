package quota

import (
	"context"
	"errors"
	"time"
)

var (
	// ErrUnavailable reports that quota State cannot safely serve operations.
	ErrUnavailable = errors.New("quota state unavailable")
	// ErrAdmissionContended reports that request admission exhausted its
	// conflict retries without proving either admission or denial.
	ErrAdmissionContended = errors.New("quota request admission contended")
)

const (
	// BucketResolution is the precision of usage windows.
	BucketResolution = time.Minute
	// MaxWindow is the maximum effective usage history.
	MaxWindow = 24 * time.Hour
)

// RequestLimit is one request-count boundary evaluated during admission.
type RequestLimit struct {
	Limit  int64
	Window time.Duration
}

// Lease reserves one concurrency slot until released.
type Lease interface {
	Release(context.Context) error
}

// Store is the State contract required by quota enforcement.
type Store interface {
	AdmitRequest(context.Context, string, []RequestLimit) (bool, error)
	TokenValue(context.Context, string, time.Duration) (int64, error)
	RecordTokens(context.Context, string, int64) error
	Acquire(context.Context, string, int64, time.Duration) (Lease, bool, error)
}
