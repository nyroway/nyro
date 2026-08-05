package state

import (
	"context"
	"errors"
	"time"
)

var (
	// ErrNotInteger reports that a counter value is not a signed 64-bit integer.
	ErrNotInteger = errors.New("state: value is not an integer")
	// ErrOverflow reports that a counter operation would overflow int64.
	ErrOverflow = errors.New("state: integer overflow")
	// ErrInvalidOptions reports a conflicting or invalid operation option.
	ErrInvalidOptions = errors.New("state: invalid options")
)

// Value distinguishes a missing key from an existing empty value.
type Value struct {
	Bytes []byte
	Found bool
}

// Pair is a binary-safe key/value pair used by MSet.
type Pair struct {
	Key   []byte
	Value []byte
}

// SetCondition controls whether Set may create or replace a key.
type SetCondition uint8

const (
	SetAlways SetCondition = iota
	SetIfMissing
	SetIfPresent
)

// SetOptions controls conditional replacement, previous-value retrieval, and
// expiration. Expiration and ExpireAt are mutually exclusive with KeepTTL.
type SetOptions struct {
	Condition   SetCondition
	GetPrevious bool
	Expiration  time.Duration
	ExpireAt    time.Time
	KeepTTL     bool
}

// SetResult reports whether the write happened and, when requested, the value
// that existed before the operation.
type SetResult struct {
	Applied  bool
	Previous Value
}

// ExpireCondition constrains updates to an existing key's deadline.
type ExpireCondition uint8

const (
	ExpireAlways ExpireCondition = iota
	ExpireIfNoExpiry
	ExpireIfHasExpiry
	ExpireIfGreater
	ExpireIfLess
)

// ExpireOptions controls conditional deadline updates.
type ExpireOptions struct {
	Condition ExpireCondition
}

// TTLState identifies the three Redis-compatible lifetime states.
type TTLState uint8

const (
	TTLMissing TTLState = iota
	TTLPersistent
	TTLExpiring
)

// TTLResult is the remaining lifetime of a key.
type TTLResult struct {
	State     TTLState
	Remaining time.Duration
}

// Operations is the String-and-TTL state surface shared by stores and atomic
// transactions.
type Operations interface {
	Get(context.Context, []byte) (Value, error)
	Set(context.Context, []byte, []byte, SetOptions) (SetResult, error)
	MGet(context.Context, ...[]byte) ([]Value, error)
	MSet(context.Context, []Pair) error
	Delete(context.Context, ...[]byte) (int64, error)
	Exists(context.Context, ...[]byte) (int64, error)
	IncrBy(context.Context, []byte, int64) (int64, error)
	Expire(context.Context, []byte, time.Time, ExpireOptions) (bool, error)
	TTL(context.Context, []byte) (TTLResult, error)
	Persist(context.Context, []byte) (bool, error)
}

// Store supports regular operations and an atomic multi-operation callback.
type Store interface {
	Operations
	Update(context.Context, func(Operations) error) error
}
