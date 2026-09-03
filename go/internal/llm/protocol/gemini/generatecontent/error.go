package generatecontent

import (
	"encoding/json"

	"github.com/nyroway/nyro/go/internal/llm"
	"github.com/nyroway/nyro/go/internal/llm/protocol"
)

func (egress) DecodeError(response protocol.WireResponse) (*llm.Error, error) {
	var envelope struct {
		Error struct {
			Message string `json:"message"`
			Status  string `json:"status"`
		} `json:"error"`
	}
	_ = json.Unmarshal(response.Body, &envelope)
	return protocol.ErrorFromWire(response, envelope.Error.Message, envelope.Error.Status), nil
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
	status, message, kind := 500, "LLM request failed", llm.ErrUnknown
	if providerError != nil {
		if providerError.StatusCode != nil && *providerError.StatusCode > 0 {
			status = int(*providerError.StatusCode)
		}
		if providerError.Message != "" {
			message = providerError.Message
		}
		if providerError.Kind != "" {
			kind = providerError.Kind
		}
	}
	type errorPayload struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
	}
	return json.Marshal(struct {
		Error errorPayload `json:"error"`
	}{Error: errorPayload{Code: status, Message: message, Status: googleErrorStatus(kind)}})
}

func googleErrorStatus(kind llm.ErrorKind) string {
	switch kind {
	case llm.ErrAuthenticationError:
		return "UNAUTHENTICATED"
	case llm.ErrAuthorizationError:
		return "PERMISSION_DENIED"
	case llm.ErrNotFoundError, llm.ErrModelNotAvailable:
		return "NOT_FOUND"
	case llm.ErrRateLimitError, llm.ErrQuotaExceeded:
		return "RESOURCE_EXHAUSTED"
	case llm.ErrInvalidRequest, llm.ErrContextLengthExceeded:
		return "INVALID_ARGUMENT"
	case llm.ErrServiceUnavailable:
		return "UNAVAILABLE"
	case llm.ErrTimeout:
		return "DEADLINE_EXCEEDED"
	case llm.ErrContentFiltered:
		return "FAILED_PRECONDITION"
	case llm.ErrServerError, llm.ErrStreamMidError, llm.ErrUnexpectedEOF:
		return "INTERNAL"
	default:
		return "UNKNOWN"
	}
}

func ingressErrorStatus(providerError *llm.Error) int {
	if providerError != nil && providerError.StatusCode != nil && *providerError.StatusCode > 0 {
		return int(*providerError.StatusCode)
	}
	return 500
}
