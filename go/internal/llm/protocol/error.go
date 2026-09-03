package protocol

import (
	"encoding/json"
	"strings"

	"github.com/nyroway/nyro/go/internal/llm"
)

// ErrorFromWire builds a canonical Provider error while retaining the wire
// body for trusted same-Endpoint passthrough and diagnostics. Protocol codecs
// supply their decoded vendor discriminators in decreasing specificity.
func ErrorFromWire(response WireResponse, message string, discriminators ...string) *llm.Error {
	if message == "" {
		message = "upstream provider request failed"
	}
	var normalized *llm.Error
	if response.Status > 0 && response.Status <= 65535 {
		normalized = llm.ErrorFromStatus(uint16(response.Status), message)
	} else {
		normalized = llm.NewError(llm.ErrUnknown, message)
	}
	for _, discriminator := range discriminators {
		if kind, found := errorKind(discriminator); found {
			normalized.Kind = kind
			break
		}
	}
	normalized.Raw = append(json.RawMessage(nil), response.Body...)
	return normalized
}

// OpenAIErrorType maps a canonical error kind to the discriminator shared by
// OpenAI-compatible error envelopes.
func OpenAIErrorType(kind llm.ErrorKind) string {
	switch kind {
	case llm.ErrAuthenticationError:
		return "authentication_error"
	case llm.ErrAuthorizationError:
		return "permission_error"
	case llm.ErrRateLimitError:
		return "rate_limit_error"
	case llm.ErrQuotaExceeded:
		return "insufficient_quota"
	case llm.ErrInvalidRequest, llm.ErrNotFoundError, llm.ErrContentFiltered,
		llm.ErrContextLengthExceeded, llm.ErrModelNotAvailable:
		return "invalid_request_error"
	case llm.ErrServerError, llm.ErrServiceUnavailable, llm.ErrTimeout,
		llm.ErrStreamMidError, llm.ErrUnexpectedEOF:
		return "server_error"
	default:
		return "api_error"
	}
}

func errorKind(discriminator string) (llm.ErrorKind, bool) {
	value := strings.ToLower(strings.TrimSpace(discriminator))
	switch {
	case value == "":
		return "", false
	case strings.Contains(value, "context_length") || strings.Contains(value, "max_context"):
		return llm.ErrContextLengthExceeded, true
	case strings.Contains(value, "insufficient_quota") || strings.Contains(value, "quota_exceeded"):
		return llm.ErrQuotaExceeded, true
	case strings.Contains(value, "rate_limit") || strings.Contains(value, "resource_exhausted"):
		return llm.ErrRateLimitError, true
	case strings.Contains(value, "authentication") || strings.Contains(value, "invalid_api_key") || strings.Contains(value, "unauthenticated"):
		return llm.ErrAuthenticationError, true
	case strings.Contains(value, "authorization") || strings.Contains(value, "permission_denied") || strings.Contains(value, "forbidden"):
		return llm.ErrAuthorizationError, true
	case strings.Contains(value, "model_not") || strings.Contains(value, "model_unavailable"):
		return llm.ErrModelNotAvailable, true
	case strings.Contains(value, "not_found"):
		return llm.ErrNotFoundError, true
	case strings.Contains(value, "content_filter") || strings.Contains(value, "safety"):
		return llm.ErrContentFiltered, true
	case strings.Contains(value, "timeout") || strings.Contains(value, "deadline"):
		return llm.ErrTimeout, true
	case strings.Contains(value, "overload") || strings.Contains(value, "unavailable"):
		return llm.ErrServiceUnavailable, true
	case strings.Contains(value, "server") || strings.Contains(value, "internal"):
		return llm.ErrServerError, true
	case strings.Contains(value, "invalid_request") || strings.Contains(value, "invalid_argument") || strings.Contains(value, "bad_request"):
		return llm.ErrInvalidRequest, true
	default:
		return "", false
	}
}
