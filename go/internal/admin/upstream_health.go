package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/nyroway/nyro/go/internal/protocol/codec"
	_ "github.com/nyroway/nyro/go/internal/protocol/codec/anthropic"
	_ "github.com/nyroway/nyro/go/internal/protocol/codec/gemini"
	_ "github.com/nyroway/nyro/go/internal/protocol/codec/openai"
	_ "github.com/nyroway/nyro/go/internal/protocol/codec/responses"
	"github.com/nyroway/nyro/go/internal/protocol/ids"
	"github.com/nyroway/nyro/go/internal/protocol/ir"
	"github.com/nyroway/nyro/go/internal/provider"
	"github.com/nyroway/nyro/go/internal/storage"
)

type upstreamHealthEvent struct {
	Type       string `json:"type"`
	Check      string `json:"check,omitempty"`
	Status     string `json:"status,omitempty"`
	Message    string `json:"message,omitempty"`
	Model      string `json:"model,omitempty"`
	LatencyMS  int64  `json:"latency_ms,omitempty"`
	StatusCode int    `json:"status_code,omitempty"`
	Error      string `json:"error,omitempty"`
	Success    *bool  `json:"success,omitempty"`
}

type healthEventWriter struct {
	w       http.ResponseWriter
	flusher http.Flusher
}

type upstreamHealthOptions struct {
	checkNameConflict bool
	excludeID         string
}

func newHealthEventWriter(w http.ResponseWriter) *healthEventWriter {
	flusher, _ := w.(http.Flusher)
	return &healthEventWriter{w: w, flusher: flusher}
}

func (e *healthEventWriter) send(ev upstreamHealthEvent) {
	b, _ := json.Marshal(ev)
	_, _ = e.w.Write([]byte("event: health\n"))
	_, _ = e.w.Write([]byte("data: "))
	_, _ = e.w.Write(b)
	_, _ = e.w.Write([]byte("\n\n"))
	if e.flusher != nil {
		e.flusher.Flush()
	}
}

func streamDraftUpstreamHealth(w http.ResponseWriter, r *http.Request, s storage.Storage, in storage.CreateUpstream) {
	streamUpstreamHealth(w, r, s, draftUpstream(in), upstreamHealthOptions{checkNameConflict: true})
}

// streamEditDraftUpstreamHealth runs the same pre-save validation pipeline as
// streamDraftUpstreamHealth, but excludes excludeID from the name-uniqueness
// check — an edit form resubmits the provider's own (unchanged) name, which
// would otherwise always collide with itself.
func streamEditDraftUpstreamHealth(w http.ResponseWriter, r *http.Request, s storage.Storage, in storage.CreateUpstream, excludeID string) {
	streamUpstreamHealth(w, r, s, draftUpstream(in), upstreamHealthOptions{checkNameConflict: true, excludeID: excludeID})
}

func streamSavedUpstreamHealth(w http.ResponseWriter, r *http.Request, s storage.Storage, u storage.Upstream) {
	streamUpstreamHealth(w, r, s, u, upstreamHealthOptions{})
}

func streamUpstreamHealth(w http.ResponseWriter, r *http.Request, s storage.Storage, u storage.Upstream, opts upstreamHealthOptions) {
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	events := newHealthEventWriter(w)
	success := false
	complete := func(ok bool, errMsg string) {
		success = ok
		events.send(upstreamHealthEvent{Type: "complete", Success: &success, Error: errMsg})
	}

	events.send(upstreamHealthEvent{Type: "check", Check: "config", Status: "running", Message: "Validating provider configuration"})
	if strings.TrimSpace(u.Name) == "" {
		msg := "provider name is required"
		events.send(upstreamHealthEvent{Type: "check", Check: "config", Status: "failed", Error: msg})
		complete(false, msg)
		return
	}
	if err := validateNewUpstreamFields(u.Provider, u.BaseURL, u.ModelsJSON, u.ModelsURL); err != nil {
		events.send(upstreamHealthEvent{Type: "check", Check: "config", Status: "failed", Error: err.Error()})
		complete(false, err.Error())
		return
	}
	if opts.checkNameConflict {
		if exists, _ := s.Upstreams().ExistsByName(u.Name, opts.excludeID); exists {
			msg := "upstream name already exists"
			events.send(upstreamHealthEvent{Type: "check", Check: "config", Status: "failed", Error: msg})
			complete(false, msg)
			return
		}
	}
	events.send(upstreamHealthEvent{Type: "check", Check: "config", Status: "passed", Message: "Configuration is valid"})

	events.send(upstreamHealthEvent{Type: "check", Check: "credentials", Status: "running", Message: "Validating upstream credentials"})
	auth, err := provider.AuthenticatorFor(u.Provider, u.Protocol, provider.UpstreamRuntime{
		Name:            u.Name,
		Provider:        u.Provider,
		Protocol:        u.Protocol,
		BaseURL:         u.BaseURL,
		CredentialsJSON: u.CredentialsJSON,
		ProxyURL:        u.ProxyURL,
	})
	if err != nil {
		events.send(upstreamHealthEvent{Type: "check", Check: "credentials", Status: "failed", Error: err.Error()})
		complete(false, err.Error())
		return
	}
	if req, reqErr := http.NewRequestWithContext(r.Context(), http.MethodGet, firstNonEmpty(u.BaseURL, "http://localhost"), nil); reqErr == nil {
		if err := auth.Apply(r.Context(), req); err != nil {
			events.send(upstreamHealthEvent{Type: "check", Check: "credentials", Status: "failed", Error: err.Error()})
			complete(false, err.Error())
			return
		}
	}
	events.send(upstreamHealthEvent{Type: "check", Check: "credentials", Status: "passed", Message: "Credentials can be applied"})

	events.send(upstreamHealthEvent{Type: "check", Check: "models", Status: "running", Message: "Resolving a model to test"})
	model, err := firstModelForDraft(r.Context(), u)
	if err != nil {
		events.send(upstreamHealthEvent{Type: "check", Check: "models", Status: "failed", Error: err.Error()})
		complete(false, err.Error())
		return
	}
	events.send(upstreamHealthEvent{Type: "check", Check: "models", Status: "passed", Model: model, Message: "Model resolved"})

	events.send(upstreamHealthEvent{Type: "check", Check: "model_request", Status: "running", Model: model, Message: "Sending minimal model request"})
	latency, statusCode, err := testDraftModelRequest(r, u, model, auth)
	if err != nil {
		events.send(upstreamHealthEvent{Type: "check", Check: "model_request", Status: "failed", Model: model, LatencyMS: latency, StatusCode: statusCode, Error: err.Error()})
		complete(false, err.Error())
		return
	}
	events.send(upstreamHealthEvent{Type: "check", Check: "model_request", Status: "passed", Model: model, LatencyMS: latency, StatusCode: statusCode, Message: "Model request succeeded"})
	complete(true, "")
}

func draftUpstream(in storage.CreateUpstream) storage.Upstream {
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	return storage.Upstream{
		Name:            in.Name,
		Provider:        in.Provider,
		Protocol:        in.Protocol,
		BaseURL:         in.BaseURL,
		CredentialsJSON: in.CredentialsJSON,
		ModelsJSON:      in.ModelsJSON,
		ModelsURL:       in.ModelsURL,
		ProxyURL:        in.ProxyURL,
		Enabled:         enabled,
	}
}

func firstModelForDraft(ctx context.Context, u storage.Upstream) (string, error) {
	var models []string
	if len(u.ModelsJSON) > 0 {
		if err := json.Unmarshal(u.ModelsJSON, &models); err != nil {
			return "", err
		}
	} else {
		discoveryURL := modelsDiscoveryURL(u)
		if discoveryURL == "" {
			return "", fmt.Errorf("models or models_url is required to verify model availability")
		}
		var err error
		models, err = fetchModels(ctx, u, discoveryURL)
		if err != nil {
			return "", err
		}
	}
	for _, model := range models {
		if trimmed := strings.TrimSpace(model); trimmed != "" {
			return trimmed, nil
		}
	}
	return "", fmt.Errorf("no models returned for verification")
}

func testDraftModelRequest(r *http.Request, u storage.Upstream, model string, auth provider.Authenticator) (int64, int, error) {
	proto, err := ids.ParseProtocol(u.Protocol)
	if err != nil {
		return 0, 0, err
	}
	ep, ok := ids.ChatEndpointFor(proto)
	if !ok {
		return 0, 0, fmt.Errorf("protocol %q does not support model test requests", u.Protocol)
	}
	handler, ok := codec.Get(ep)
	if !ok {
		return 0, 0, fmt.Errorf("no codec registered for protocol %q", u.Protocol)
	}
	maxTokens := uint32(1)
	req := ir.NewAiRequest(model, []ir.Message{{
		Role:    ir.RoleUser,
		Content: &ir.TextContent{Text: "ping"},
	}})
	req.Generation.MaxTokens = &maxTokens
	outbound, err := handler.MakeRequestEncoder().Encode(req)
	if err != nil {
		return 0, 0, err
	}
	outbound.Path = provider.BuildURL(u.BaseURL, outbound.Path)
	upstreamReq, err := http.NewRequestWithContext(r.Context(), outbound.Method, outbound.Path, bytes.NewReader(outbound.Body))
	if err != nil {
		return 0, 0, err
	}
	for k, v := range outbound.Headers {
		upstreamReq.Header.Set(k, v)
	}
	if upstreamReq.Header.Get("Content-Type") == "" {
		upstreamReq.Header.Set("Content-Type", "application/json")
	}
	if err := auth.Apply(r.Context(), upstreamReq); err != nil {
		return 0, 0, err
	}
	client := testHTTPClient(u.ProxyURL, 20*time.Second)
	start := time.Now()
	resp, err := client.Do(upstreamReq)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		return latency, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		return latency, resp.StatusCode, fmt.Errorf("model request failed: %s", resp.Status)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return latency, resp.StatusCode, err
	}
	decoded, err := handler.MakeResponseDecoder().Parse(body)
	if err != nil {
		return latency, resp.StatusCode, fmt.Errorf("model response validation failed: %w", err)
	}
	if !isUsableModelResponse(decoded) {
		return latency, resp.StatusCode, fmt.Errorf("model response validation failed: empty or unrecognized response")
	}
	return latency, resp.StatusCode, nil
}

func isUsableModelResponse(resp *ir.AiResponse) bool {
	if resp == nil || resp.IsError() {
		return false
	}
	return resp.ID != "" ||
		resp.Model != "" ||
		resp.Content != "" ||
		resp.ReasoningContent != "" ||
		resp.StopReason != "" ||
		len(resp.ToolCalls) > 0 ||
		len(resp.Items) > 0 ||
		resp.Usage.TotalTokens > 0 ||
		resp.Usage.PromptTokens > 0 ||
		resp.Usage.CompletionTokens > 0
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
