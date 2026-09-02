package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/nyroway/nyro/go/internal/llm"
	"github.com/nyroway/nyro/go/internal/llm/protocol"
	"github.com/nyroway/nyro/go/internal/llm/provider"
	"github.com/nyroway/nyro/go/internal/storage"
)

type upstreamHealthEvent struct {
	Type       string   `json:"type"`
	Check      string   `json:"check,omitempty"`
	Status     string   `json:"status,omitempty"`
	Message    string   `json:"message,omitempty"`
	Model      string   `json:"model,omitempty"`
	Discovered int      `json:"discovered,omitempty"`
	Models     []string `json:"models,omitempty"`
	LatencyMS  int64    `json:"latency_ms,omitempty"`
	StatusCode int      `json:"status_code,omitempty"`
	Error      string   `json:"error,omitempty"`
	Success    *bool    `json:"success,omitempty"`
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

func streamDraftUpstreamHealth(w http.ResponseWriter, r *http.Request, s storage.Storage, protocols *protocol.Catalog, providers *provider.Catalog, in storage.CreateUpstream) {
	streamUpstreamHealth(w, r, s, protocols, providers, draftUpstream(in), upstreamHealthOptions{checkNameConflict: true})
}

// streamEditDraftUpstreamHealth runs the same pre-save validation pipeline as
// streamDraftUpstreamHealth, but excludes excludeID from the name-uniqueness
// check — an edit form resubmits the provider's own (unchanged) name, which
// would otherwise always collide with itself.
func streamEditDraftUpstreamHealth(w http.ResponseWriter, r *http.Request, s storage.Storage, protocols *protocol.Catalog, providers *provider.Catalog, in storage.CreateUpstream, excludeID string) {
	streamUpstreamHealth(w, r, s, protocols, providers, draftUpstream(in), upstreamHealthOptions{checkNameConflict: true, excludeID: excludeID})
}

func streamSavedUpstreamHealth(w http.ResponseWriter, r *http.Request, s storage.Storage, protocols *protocol.Catalog, providers *provider.Catalog, u storage.Upstream) {
	streamUpstreamHealth(w, r, s, protocols, providers, u, upstreamHealthOptions{})
}

func streamUpstreamHealth(w http.ResponseWriter, r *http.Request, s storage.Storage, protocols *protocol.Catalog, providers *provider.Catalog, u storage.Upstream, opts upstreamHealthOptions) {
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
	if err := validateNewUpstreamFields(providers, u.Provider, u.BaseURL, u.ModelsJSON, u.ModelsURL); err != nil {
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
	if providers == nil {
		msg := "provider catalog is unavailable"
		events.send(upstreamHealthEvent{Type: "check", Check: "credentials", Status: "failed", Error: msg})
		complete(false, msg)
		return
	}
	factory := providers.DriverFor(u.Provider)
	if factory == nil {
		msg := "provider driver is unavailable"
		events.send(upstreamHealthEvent{Type: "check", Check: "credentials", Status: "failed", Error: msg})
		complete(false, msg)
		return
	}
	driver := factory()
	_, err := driver.Prepare(r.Context(), provider.UpstreamRuntime{
		Name:            u.Name,
		Provider:        u.Provider,
		Protocol:        u.Protocol,
		BaseURL:         firstNonEmpty(u.BaseURL, "http://localhost"),
		CredentialsJSON: u.CredentialsJSON,
		ProxyURL:        u.ProxyURL,
	}, protocol.WireRequest{Method: http.MethodGet})
	if err != nil {
		events.send(upstreamHealthEvent{Type: "check", Check: "credentials", Status: "failed", Error: err.Error()})
		complete(false, err.Error())
		return
	}
	events.send(upstreamHealthEvent{Type: "check", Check: "credentials", Status: "passed", Message: "Credentials can be applied"})

	events.send(upstreamHealthEvent{Type: "check", Check: "models", Status: "running", Message: "Resolving a model to test"})
	model, models, err := firstModelForDraft(r.Context(), providers, u)
	if err != nil {
		events.send(upstreamHealthEvent{Type: "check", Check: "models", Status: "failed", Error: err.Error()})
		complete(false, err.Error())
		return
	}
	events.send(upstreamHealthEvent{Type: "check", Check: "models", Status: "passed", Model: model, Discovered: len(models), Models: models, Message: "Model resolved"})

	events.send(upstreamHealthEvent{Type: "check", Check: "model_request", Status: "running", Model: model, Message: "Sending minimal model request"})
	latency, statusCode, err := testDraftModelRequest(r, protocols, u, model, driver)
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

// firstModelForDraft resolves the model list for u (static models_json or a
// live discovery fetch) and returns the first model to run the smoke-test
// request against, along with the full deduplicated list so the caller can
// report every model that was found (the health check only exercises one
// model, but the UI shows the complete discovery result).
func firstModelForDraft(ctx context.Context, providers *provider.Catalog, u storage.Upstream) (string, []string, error) {
	var models []string
	if len(u.ModelsJSON) > 0 {
		if err := json.Unmarshal(u.ModelsJSON, &models); err != nil {
			return "", nil, err
		}
	} else {
		discoveryURL := modelsDiscoveryURL(providers, u)
		if discoveryURL == "" {
			return "", nil, fmt.Errorf("models or models_url is required to verify model availability")
		}
		var err error
		models, err = fetchModels(ctx, providers, u, discoveryURL)
		if err != nil {
			return "", nil, err
		}
	}
	models = normalizeImportModels(models)
	if len(models) == 0 {
		return "", nil, fmt.Errorf("no models returned for verification")
	}
	return models[0], models, nil
}

func testDraftModelRequest(r *http.Request, protocols *protocol.Catalog, u storage.Upstream, model string, driver provider.Driver) (int64, int, error) {
	proto, err := protocol.ParseProtocol(u.Protocol)
	if err != nil {
		return 0, 0, err
	}
	if protocols == nil {
		return 0, 0, fmt.Errorf("LLM protocol catalog is unavailable")
	}
	ep, ok := protocols.EndpointFor(proto)
	if !ok {
		return 0, 0, fmt.Errorf("no egress codec configured for protocol %q", u.Protocol)
	}
	handler, ok := protocols.Egress(ep)
	if !ok {
		return 0, 0, fmt.Errorf("no egress codec configured for protocol %q", u.Protocol)
	}
	chatHandler, ok := handler.(protocol.ChatEgressCodec)
	if !ok {
		return 0, 0, fmt.Errorf("protocol %q does not support chat health checks", u.Protocol)
	}
	maxTokens := uint32(1)
	req := llm.NewChatRequest(model, []llm.Message{{
		Role:    llm.RoleUser,
		Content: &llm.TextContent{Text: "ping"},
	}})
	req.Generation.MaxTokens = &maxTokens
	outbound, err := chatHandler.EncodeRequest(req)
	if err != nil {
		return 0, 0, err
	}
	prepared, err := driver.Prepare(r.Context(), provider.UpstreamRuntime{
		Name: u.Name, Provider: u.Provider, Protocol: u.Protocol, BaseURL: u.BaseURL,
		CredentialsJSON: u.CredentialsJSON, ProxyURL: u.ProxyURL,
	}, outbound)
	if err != nil {
		return 0, 0, err
	}
	transport, err := newAdminProviderTransport(u.ProxyURL, 20*time.Second)
	if err != nil {
		return 0, 0, err
	}
	start := time.Now()
	resp, err := transport.Do(r.Context(), prepared)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		return latency, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		return latency, resp.StatusCode, fmt.Errorf("model request failed: %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return latency, resp.StatusCode, err
	}
	decoded, err := chatHandler.DecodeResponse(protocol.WireResponse{Status: resp.StatusCode, Body: body})
	if err != nil {
		return latency, resp.StatusCode, fmt.Errorf("model response validation failed: %w", err)
	}
	if !isUsableModelResponse(decoded) {
		return latency, resp.StatusCode, fmt.Errorf("model response validation failed: empty or unrecognized response")
	}
	return latency, resp.StatusCode, nil
}

func isUsableModelResponse(resp *llm.ChatResponse) bool {
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
