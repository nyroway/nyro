package ir

import (
	"encoding/json"
	"fmt"
)

// AiErrorKind classifies a cross-protocol AI error. Use IsRetryable to decide
// whether the gateway may automatically retry.
// Ported from AiErrorKind (serde rename_all = "snake_case").
type AiErrorKind string

const (
	ErrAuthenticationError   AiErrorKind = "authentication_error"
	ErrAuthorizationError    AiErrorKind = "authorization_error"
	ErrNotFoundError         AiErrorKind = "not_found_error"
	ErrRateLimitError        AiErrorKind = "rate_limit_error"
	ErrQuotaExceeded         AiErrorKind = "quota_exceeded"
	ErrInvalidRequest        AiErrorKind = "invalid_request"
	ErrServerError           AiErrorKind = "server_error"
	ErrServiceUnavailable    AiErrorKind = "service_unavailable"
	ErrTimeout               AiErrorKind = "timeout"
	ErrContentFiltered       AiErrorKind = "content_filtered"
	ErrContextLengthExceeded AiErrorKind = "context_length_exceeded"
	ErrModelNotAvailable     AiErrorKind = "model_not_available"
	ErrStreamMidError        AiErrorKind = "stream_mid_error"
	ErrUnexpectedEOF         AiErrorKind = "unexpected_eof"
	ErrUnknown               AiErrorKind = "unknown"
)

// IsRetryable reports whether the gateway may automatically retry after this
// error kind (transient failures only).
func (k AiErrorKind) IsRetryable() bool {
	switch k {
	case ErrRateLimitError, ErrServerError, ErrServiceUnavailable, ErrTimeout,
		ErrModelNotAvailable, ErrUnexpectedEOF, ErrStreamMidError:
		return true
	}
	return false
}

// AiError is a cross-protocol normalized AI error, produced by codec parsers
// and the dispatcher when an upstream call fails. It always carries a Kind for
// retry / circuit-breaker decisions; Raw preserves the vendor error body
// verbatim for logging and passthrough.
type AiError struct {
	Kind       AiErrorKind
	Message    string
	StatusCode *uint16         // optional HTTP status
	Raw        json.RawMessage // optional original vendor error body (verbatim)
}

// NewAiError constructs an AiError with kind and message.
func NewAiError(kind AiErrorKind, message string) *AiError {
	return &AiError{Kind: kind, Message: message}
}

// WithStatus sets the HTTP status code (builder-style).
func (e *AiError) WithStatus(status uint16) *AiError { e.StatusCode = &status; return e }

// WithRaw sets the raw vendor error body (builder-style).
func (e *AiError) WithRaw(raw json.RawMessage) *AiError { e.Raw = raw; return e }

// IsRetryable delegates to the kind.
func (e *AiError) IsRetryable() bool { return e.Kind.IsRetryable() }

// Error implements error.
func (e *AiError) Error() string { return fmt.Sprintf("[%s] %s", e.Kind, e.Message) }

// AiErrorFromStatus constructs an AiError from an HTTP status code. The caller
// should override Kind if the response body is more specific (e.g. OpenAI
// error.type = "context_length_exceeded").
func AiErrorFromStatus(status uint16, message string) *AiError {
	var kind AiErrorKind
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
	return NewAiError(kind, message).WithStatus(status)
}
