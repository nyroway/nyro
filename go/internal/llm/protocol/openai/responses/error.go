package responses

import (
	"encoding/json"
	"strings"

	"github.com/nyroway/nyro/go/internal/llm"
	"github.com/nyroway/nyro/go/internal/llm/protocol"
)

func (egress) DecodeError(response protocol.WireResponse) (*llm.Error, error) {
	var envelope struct {
		Error struct {
			Message string          `json:"message"`
			Type    string          `json:"type"`
			Code    json.RawMessage `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(response.Body, &envelope)
	code := strings.Trim(string(envelope.Error.Code), `"`)
	return protocol.ErrorFromWire(response, envelope.Error.Message, code, envelope.Error.Type), nil
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
		Message string `json:"message"`
		Type    string `json:"type"`
	}
	return json.Marshal(struct {
		Error errorPayload `json:"error"`
	}{Error: errorPayload{Message: message, Type: openAIErrorType(kind)}})
}

func openAIErrorType(kind llm.ErrorKind) string {
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

func ingressErrorStatus(providerError *llm.Error) int {
	if providerError != nil && providerError.StatusCode != nil && *providerError.StatusCode > 0 {
		return int(*providerError.StatusCode)
	}
	return 500
}
