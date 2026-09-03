package messages

import (
	"encoding/json"

	"github.com/nyroway/nyro/go/internal/llm"
	"github.com/nyroway/nyro/go/internal/llm/protocol"
)

func (egress) DecodeError(response protocol.WireResponse) (*llm.Error, error) {
	var envelope struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(response.Body, &envelope)
	return protocol.ErrorFromWire(response, envelope.Error.Message, envelope.Error.Type), nil
}

func (ingress) EncodeError(providerError *llm.Error) (protocol.WireResponse, error) {
	body, err := encodeErrorBody(providerError)
	return protocol.WireResponse{
		Status:  ingressErrorStatus(providerError),
		Headers: map[string]string{"Content-Type": "application/json"},
		Body:    body,
	}, err
}

func encodeErrorBody(providerError *llm.Error) ([]byte, error) {
	message, kind := "LLM request failed", llm.ErrUnknown
	if providerError != nil {
		if providerError.Message != "" {
			message = providerError.Message
		}
		if providerError.Kind != "" {
			kind = providerError.Kind
		}
	}
	type errorPayload struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	}
	return json.Marshal(struct {
		Type  string       `json:"type"`
		Error errorPayload `json:"error"`
	}{Type: "error", Error: errorPayload{Type: anthropicErrorType(kind), Message: message}})
}

func anthropicErrorType(kind llm.ErrorKind) string {
	switch kind {
	case llm.ErrAuthenticationError:
		return "authentication_error"
	case llm.ErrAuthorizationError:
		return "permission_error"
	case llm.ErrNotFoundError, llm.ErrModelNotAvailable:
		return "not_found_error"
	case llm.ErrRateLimitError:
		return "rate_limit_error"
	case llm.ErrQuotaExceeded, llm.ErrServiceUnavailable:
		return "overloaded_error"
	case llm.ErrInvalidRequest, llm.ErrContentFiltered:
		return "invalid_request_error"
	case llm.ErrContextLengthExceeded:
		return "context_length_exceeded"
	default:
		return "api_error"
	}
}

func ingressErrorStatus(providerError *llm.Error) int {
	if providerError != nil && providerError.StatusCode != nil && *providerError.StatusCode > 0 {
		return int(*providerError.StatusCode)
	}
	return 500
}
