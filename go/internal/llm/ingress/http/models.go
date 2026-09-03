package httpingress

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/nyroway/nyro/go/internal/llm"
	"github.com/nyroway/nyro/go/internal/llm/protocol"
)

func (handler *handler) serveModels(writer http.ResponseWriter, request *http.Request) {
	runtime, release, ok := handler.source.Acquire()
	if release != nil {
		defer release()
	}
	if !ok || runtime == nil {
		writeOpenAIError(writer, llm.NewError(llm.ErrServiceUnavailable, "LLM runtime is unavailable").WithStatus(http.StatusServiceUnavailable))
		return
	}

	names := runtime.ClientModelNames(credentialsFromRequest(request))
	data := make([]map[string]any, 0, len(names))
	for _, name := range names {
		data = append(data, map[string]any{"id": name, "object": "model", "created": 0, "owned_by": "Nyro"})
	}
	writeJSON(writer, http.StatusOK, map[string]any{"object": "list", "data": data})
}

func extractKey(request *http.Request) string {
	if value := request.Header.Get("Authorization"); strings.HasPrefix(value, "Bearer ") {
		return strings.TrimPrefix(value, "Bearer ")
	}
	if value := request.Header.Get("X-Api-Key"); value != "" {
		return value
	}
	return request.Header.Get("X-Goog-Api-Key")
}

func writeOpenAIError(writer http.ResponseWriter, providerError *llm.Error) {
	message := "LLM request failed"
	kind := llm.ErrUnknown
	if providerError != nil {
		if providerError.Message != "" {
			message = providerError.Message
		}
		if providerError.Kind != "" {
			kind = providerError.Kind
		}
	}
	writeJSON(writer, errorStatus(providerError), map[string]any{
		"error": map[string]any{"message": message, "type": protocol.OpenAIErrorType(kind)},
	})
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
