package llm

import (
	"encoding/json"
	"fmt"
)

// ErrorKind classifies a cross-protocol LLM error. Use IsRetryable to decide
// whether the gateway may automatically retry.
// Ported from ErrorKind (serde rename_all = "snake_case").
type ErrorKind string

const (
	ErrAuthenticationError   ErrorKind = "authentication_error"
	ErrAuthorizationError    ErrorKind = "authorization_error"
	ErrNotFoundError         ErrorKind = "not_found_error"
	ErrRateLimitError        ErrorKind = "rate_limit_error"
	ErrQuotaExceeded         ErrorKind = "quota_exceeded"
	ErrInvalidRequest        ErrorKind = "invalid_request"
	ErrServerError           ErrorKind = "server_error"
	ErrServiceUnavailable    ErrorKind = "service_unavailable"
	ErrTimeout               ErrorKind = "timeout"
	ErrContentFiltered       ErrorKind = "content_filtered"
	ErrContextLengthExceeded ErrorKind = "context_length_exceeded"
	ErrModelNotAvailable     ErrorKind = "model_not_available"
	ErrStreamMidError        ErrorKind = "stream_mid_error"
	ErrUnexpectedEOF         ErrorKind = "unexpected_eof"
	ErrUnknown               ErrorKind = "unknown"
)

// IsRetryable reports whether the gateway may automatically retry after this
// error kind (transient failures only).
func (k ErrorKind) IsRetryable() bool {
	switch k {
	case ErrRateLimitError, ErrServerError, ErrServiceUnavailable, ErrTimeout,
		ErrModelNotAvailable, ErrUnexpectedEOF, ErrStreamMidError:
		return true
	}
	return false
}

// Error is a cross-protocol normalized LLM error, produced by codec parsers
// and the dispatcher when an upstream call fails. It always carries a Kind for
// retry / circuit-breaker decisions; Raw preserves the vendor error body
// verbatim for logging and passthrough.
type Error struct {
	Kind       ErrorKind
	Message    string
	StatusCode *uint16         // optional HTTP status
	Raw        json.RawMessage // optional original vendor error body (verbatim)
}

// NewError constructs an Error with kind and message.
func NewError(kind ErrorKind, message string) *Error {
	return &Error{Kind: kind, Message: message}
}

// WithStatus sets the HTTP status code (builder-style).
func (e *Error) WithStatus(status uint16) *Error { e.StatusCode = &status; return e }

// WithRaw sets the raw vendor error body (builder-style).
func (e *Error) WithRaw(raw json.RawMessage) *Error { e.Raw = raw; return e }

// IsRetryable delegates to the kind.
func (e *Error) IsRetryable() bool { return e.Kind.IsRetryable() }

// Error implements error.
func (e *Error) Error() string { return fmt.Sprintf("[%s] %s", e.Kind, e.Message) }

// ErrorFromStatus constructs an Error from an HTTP status code. The caller
// should override Kind if the response body is more specific (e.g. OpenAI
// error.type = "context_length_exceeded").
func ErrorFromStatus(status uint16, message string) *Error {
	var kind ErrorKind
	switch status {
	case 401:
		kind = ErrAuthenticationError
	case 403:
		kind = ErrAuthorizationError
	case 404:
		kind = ErrNotFoundError
	case 408, 504:
		kind = ErrTimeout
	case 429:
		kind = ErrRateLimitError
	case 500:
		kind = ErrServerError
	case 503, 529:
		kind = ErrServiceUnavailable
	default:
		kind = ErrUnknown
	}
	return NewError(kind, message).WithStatus(status)
}
