// Package runtime owns transport-neutral LLM execution invariants.
package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"time"

	configsnapshot "github.com/nyroway/nyro/go/internal/config/snapshot"
	"github.com/nyroway/nyro/go/internal/llm"
	"github.com/nyroway/nyro/go/internal/llm/pipeline"
	"github.com/nyroway/nyro/go/internal/llm/protocol"
	"github.com/nyroway/nyro/go/internal/llm/provider"
	"github.com/nyroway/nyro/go/internal/llm/routing"
	"github.com/nyroway/nyro/go/internal/quota"
	"github.com/nyroway/nyro/go/internal/security/authn"
)

const (
	statusOK                  = 200
	statusBadRequest          = 400
	statusUnauthorized        = 401
	statusForbidden           = 403
	statusNotFound            = 404
	statusRequestTimeout      = 408
	statusTooManyRequests     = 429
	statusInternalServerError = 500
	statusBadGateway          = 502
	statusServiceUnavailable  = 503
)

// Config supplies one immutable Snapshot and its explicitly composed LLM
// capabilities. Runtime copies phase slices and never reads a mutable catalog.
type Config struct {
	Snapshot     *configsnapshot.Snapshot
	Protocols    *protocol.Catalog
	Providers    *provider.Catalog
	Router       *routing.Router
	Transport    provider.Transport
	Quota        quota.Store
	Observe      pipeline.Phase
	PreDispatch  []pipeline.Phase
	PostResponse []pipeline.Phase
}

// Runtime executes calls against one immutable configuration Snapshot.
type Runtime struct {
	snapshot     *configsnapshot.Snapshot
	protocols    *protocol.Catalog
	providers    *provider.Catalog
	router       *routing.Router
	transport    provider.Transport
	quota        quota.Store
	settings     Settings
	observe      pipeline.Phase
	preDispatch  []pipeline.Phase
	postResponse []pipeline.Phase
}

// New validates and freezes one Snapshot-bound Runtime.
func New(config Config) (*Runtime, error) {
	switch {
	case config.Snapshot == nil:
		return nil, errors.New("llm runtime: Snapshot is required")
	case config.Protocols == nil:
		return nil, errors.New("llm runtime: Protocol Catalog is required")
	case config.Providers == nil:
		return nil, errors.New("llm runtime: Provider Catalog is required")
	case config.Transport == nil:
		return nil, errors.New("llm runtime: Provider Transport is required")
	}
	observe := config.Observe
	if observe == nil {
		observe = continuePhase{name: "observe"}
	}
	runtime := &Runtime{
		snapshot:     config.Snapshot,
		protocols:    config.Protocols,
		providers:    config.Providers,
		router:       config.Router,
		transport:    config.Transport,
		quota:        config.Quota,
		settings:     SettingsFromSnapshot(config.Snapshot),
		observe:      observe,
		preDispatch:  append([]pipeline.Phase(nil), config.PreDispatch...),
		postResponse: append([]pipeline.Phase(nil), config.PostResponse...),
	}
	if runtime.router == nil {
		runtime.router = routing.New()
	}
	// Validate the complete immutable phase set at construction. Execute builds
	// only a request-bound copy so Dispatch can hold its Sink without placing a
	// function or transport object on pipeline.Exchange.
	if _, err := runtime.runnerFor(&execution{}); err != nil {
		return nil, fmt.Errorf("llm runtime: configure pipeline: %w", err)
	}
	return runtime, nil
}

// Execute runs one call through the fixed pipeline and delivers its terminal
// canonical value through Sink. Pipeline Completion is returned for observers
// and callers that need usage or terminal error state.
func (r *Runtime) Execute(ctx context.Context, call Call) pipeline.Completion {
	if r == nil {
		return pipeline.Completion{Error: llm.ErrorFromStatus(statusInternalServerError, "LLM runtime is not configured")}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if call.Request == nil {
		return r.deliverImmediateError(ctx, call.Sink, llm.ErrorFromStatus(statusBadRequest, "LLM request is required"))
	}
	if call.Sink == nil {
		return pipeline.Completion{Error: llm.ErrorFromStatus(statusInternalServerError, "LLM Sink is required")}
	}
	if call.Source.Protocol == "" {
		return r.deliverImmediateError(ctx, call.Sink, llm.ErrorFromStatus(statusBadRequest, "source Endpoint is required"))
	}

	exchange := &pipeline.Exchange{
		Request:       call.Request,
		Source:        call.Source,
		Credentials:   call.Credentials,
		Started:       time.Now(),
		Streamed:      requestStreams(call.Request),
		Status:        statusOK,
		RequestInfo:   r.requestInfo(call),
		ClientAddress: call.ClientAddress,
		RequestID:     call.RequestID,
	}
	execution := &execution{runtime: r, sink: call.Sink}
	runner, err := r.runnerFor(execution)
	if err != nil {
		return r.deliverImmediateError(ctx, call.Sink, llm.ErrorFromStatus(statusInternalServerError, err.Error()))
	}
	execution.runner = runner
	completion, runErr := runner.Run(ctx, exchange)
	if completion.Error == nil && completion.Response == nil && !execution.delivered && runErr != nil {
		completion.Error = errorFromExecution(runErr)
		exchange.Error = completion.Error
		exchange.Status = statusOf(completion.Error, statusInternalServerError)
	}

	switch {
	case completion.Error != nil && !execution.delivered:
		if sendErr := call.Sink.SendError(ctx, completion.Error); sendErr != nil && !errors.Is(sendErr, context.Canceled) {
			completion.Error = errorFromExecution(fmt.Errorf("send error: %w", sendErr))
		}
	case completion.Response != nil && !execution.delivered:
		if sendErr := call.Sink.SendResponse(ctx, completion.Response); sendErr != nil {
			completion.Error = errorFromExecution(fmt.Errorf("send response: %w", sendErr))
		}
	}
	return completion
}

func (r *Runtime) runnerFor(execution *execution) (*pipeline.Runner, error) {
	return pipeline.NewRunner(pipeline.PhaseSet{
		Observe:      r.observe,
		Resolve:      resolvePhase{runtime: r},
		Authenticate: authenticatePhase{runtime: r},
		Authorize:    authorizePhase{runtime: r},
		Admit:        admitPhase{runtime: r},
		PreDispatch:  r.preDispatch,
		Dispatch:     dispatchPhase{runtime: r, execution: execution},
		PostResponse: r.postResponse,
	})
}

func (r *Runtime) deliverImmediateError(ctx context.Context, sink Sink, providerError *llm.Error) pipeline.Completion {
	if sink != nil {
		_ = sink.SendError(ctx, providerError)
	}
	return pipeline.Completion{Error: providerError}
}

func (r *Runtime) requestInfo(call Call) pipeline.RequestInfo {
	info := pipeline.RequestInfo{ClientModel: call.Request.ModelID()}
	ingress, ok := r.protocols.Ingress(call.Source)
	if !ok {
		return info
	}
	routes := ingress.Capabilities().IngressRoutes
	if len(routes) > 0 {
		info.Operation = routes[0].Method
		info.Resource = routes[0].Pattern
	}
	return info
}

type continuePhase struct{ name string }

func (p continuePhase) Name() string { return p.name }
func (continuePhase) Apply(context.Context, *pipeline.Exchange) (pipeline.Outcome, pipeline.Finalizer) {
	return pipeline.Outcome{Decision: pipeline.Continue}, nil
}

type resolvePhase struct{ runtime *Runtime }

func (resolvePhase) Name() string { return "resolve" }
func (p resolvePhase) Apply(_ context.Context, exchange *pipeline.Exchange) (pipeline.Outcome, pipeline.Finalizer) {
	model := exchange.Request.ModelID()
	route := p.runtime.snapshot.RouteByModel(model)
	switch {
	case route == nil:
		return reject(exchange, statusNotFound, "model not found: "+model), nil
	case !route.Enabled:
		return reject(exchange, statusServiceUnavailable, "model disabled: "+model), nil
	case len(route.Upstreams) == 0:
		return reject(exchange, statusServiceUnavailable, "no backends for model: "+model), nil
	}
	exchange.Route = pipeline.LogicalRoute{ID: route.ID, Model: route.Model}
	return continueOutcome(), nil
}

type authenticatePhase struct{ runtime *Runtime }

func (authenticatePhase) Name() string { return "authenticate" }
func (p authenticatePhase) Apply(_ context.Context, exchange *pipeline.Exchange) (pipeline.Outcome, pipeline.Finalizer) {
	route := p.runtime.snapshot.RouteByModel(exchange.Route.Model)
	if route == nil {
		return reject(exchange, statusServiceUnavailable, "logical route is unavailable"), nil
	}
	if !route.EnableAuth {
		exchange.Identity = authn.Identity{Anonymous: true}
		return continueOutcome(), nil
	}
	if exchange.Credentials.APIKey == "" {
		return reject(exchange, statusUnauthorized, "missing API key"), nil
	}
	record := p.runtime.snapshot.FindKey(exchange.Credentials.APIKey)
	if record == nil {
		return reject(exchange, statusUnauthorized, "invalid API key"), nil
	}
	exchange.Identity = authn.Identity{
		Subject:           record.ConsumerID,
		CredentialID:      record.KeyID,
		CredentialName:    record.Name,
		CredentialPreview: record.KeyPreview,
	}
	if exchange.Identity.CredentialName == "" {
		exchange.Identity.CredentialName = record.KeyPreview
	}
	if !record.Enabled {
		return reject(exchange, statusForbidden, "API key is disabled"), nil
	}
	if record.ExpiresAt != "" && expired(record.ExpiresAt) {
		return reject(exchange, statusForbidden, "API key has expired"), nil
	}
	return continueOutcome(), nil
}

type authorizePhase struct{ runtime *Runtime }

func (authorizePhase) Name() string { return "authorize" }
func (p authorizePhase) Apply(_ context.Context, exchange *pipeline.Exchange) (pipeline.Outcome, pipeline.Finalizer) {
	if exchange.Identity.Anonymous {
		exchange.Authorization.Allowed = true
		return continueOutcome(), nil
	}
	record := p.runtime.snapshot.FindKey(exchange.Credentials.APIKey)
	if record == nil || !slices.Contains(record.Routes, exchange.Route.Model) {
		exchange.Authorization.Reason = "API key is not granted this route"
		return reject(exchange, statusForbidden, exchange.Authorization.Reason), nil
	}
	exchange.Authorization.Allowed = true
	return continueOutcome(), nil
}

type admitPhase struct{ runtime *Runtime }

func (admitPhase) Name() string { return "admit" }
func (p admitPhase) Apply(ctx context.Context, exchange *pipeline.Exchange) (pipeline.Outcome, pipeline.Finalizer) {
	if exchange.Identity.Anonymous {
		return continueOutcome(), nil
	}
	record := p.runtime.snapshot.FindKey(exchange.Credentials.APIKey)
	if record == nil {
		return reject(exchange, statusUnauthorized, "invalid API key"), nil
	}
	if p.runtime.quota == nil {
		return reject(exchange, statusServiceUnavailable, "quota state unavailable"), nil
	}
	if status, message := tokenQuotaExceeded(ctx, p.runtime.quota, record); status != 0 {
		return reject(exchange, status, message), nil
	}
	lease, status, message := acquireConcurrency(ctx, p.runtime.quota, record, concurrencyLeaseTTL(p.runtime.settings))
	if status != 0 {
		return reject(exchange, status, message), nil
	}
	limits := requestLimits(record)
	if len(limits) > 0 {
		allowed, err := p.runtime.quota.AdmitRequest(ctx, record.ConsumerID, limits)
		if err != nil || !allowed {
			if lease != nil {
				if releaseErr := releaseQuotaLease(lease, record.ConsumerID); releaseErr != nil {
					return reject(exchange, statusServiceUnavailable, "quota state unavailable"), nil
				}
			}
			if err != nil {
				return reject(exchange, statusServiceUnavailable, "quota state unavailable"), nil
			}
			return reject(exchange, statusTooManyRequests, "consumer requests quota exceeded"), nil
		}
	}

	consumerID := record.ConsumerID
	return continueOutcome(), func(_ context.Context, _ *pipeline.Exchange, completion pipeline.Completion) error {
		finalizeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		tokens := int64(completion.Usage.PromptTokens) + int64(completion.Usage.CompletionTokens)
		if err := p.runtime.quota.RecordTokens(finalizeCtx, consumerID, tokens); err != nil {
			slog.Error("record quota tokens", "consumer_id", consumerID, "error", err)
		}
		if lease != nil {
			return releaseQuotaLease(lease, consumerID)
		}
		return nil
	}
}

func continueOutcome() pipeline.Outcome {
	return pipeline.Outcome{Decision: pipeline.Continue}
}

func reject(exchange *pipeline.Exchange, status int, message string) pipeline.Outcome {
	exchange.Status = status
	return pipeline.Outcome{
		Decision: pipeline.Reject,
		Error:    llm.ErrorFromStatus(uint16(status), message),
	}
}

// Settings is the immutable subset of proxy settings used by Runtime and its
// transitional Provider transport owner.
type Settings struct {
	RequestTimeout time.Duration
	ConnectTimeout time.Duration
	MaxRetries     int
	RetryOnStatus  map[int]bool
	MaxBodyBytes   int64
}

// SettingsFromSnapshot resolves proxy settings while preserving the existing
// defaults and total-attempt max_retries semantics.
func SettingsFromSnapshot(snapshot *configsnapshot.Snapshot) Settings {
	settings := Settings{
		RequestTimeout: 120 * time.Second,
		ConnectTimeout: 30 * time.Second,
		MaxRetries:     2,
		RetryOnStatus:  map[int]bool{429: true, 500: true, 502: true, 503: true, 504: true},
		MaxBodyBytes:   32 << 20,
	}
	if snapshot == nil {
		return settings
	}
	if value, ok := snapshot.SettingGet("proxy.request_timeout"); ok {
		if duration, err := time.ParseDuration(value); err == nil {
			settings.RequestTimeout = duration
		}
	}
	if value, ok := snapshot.SettingGet("proxy.connect_timeout"); ok {
		if duration, err := time.ParseDuration(value); err == nil {
			settings.ConnectTimeout = duration
		}
	}
	if value, ok := snapshot.SettingGet("proxy.max_retries"); ok {
		if attempts, err := strconv.Atoi(value); err == nil {
			settings.MaxRetries = attempts
		}
	}
	if value, ok := snapshot.SettingGet("proxy.retry_on_status"); ok {
		var codes []int
		if err := json.Unmarshal([]byte(value), &codes); err == nil && len(codes) > 0 {
			settings.RetryOnStatus = make(map[int]bool, len(codes))
			for _, code := range codes {
				settings.RetryOnStatus[code] = true
			}
		}
	}
	if value, ok := snapshot.SettingGet("proxy.max_body_bytes"); ok {
		if size, err := strconv.ParseInt(value, 10, 64); err == nil && size > 0 {
			settings.MaxBodyBytes = size
		}
	}
	return settings
}

func expired(iso string) bool {
	expiresAt, err := time.Parse(time.RFC3339, iso)
	return err == nil && time.Now().After(expiresAt)
}

func tokenQuotaExceeded(ctx context.Context, store quota.Store, record *configsnapshot.ConsumerAccess) (int, string) {
	for _, configured := range record.Quotas {
		if configured.QuotaType != "tokens" {
			continue
		}
		window, err := quota.ParseWindow(configured.Window)
		if err != nil {
			continue
		}
		value, err := store.TokenValue(ctx, record.ConsumerID, window)
		if err != nil {
			return statusServiceUnavailable, "quota state unavailable"
		}
		if value >= configured.QuotaLimit {
			return statusTooManyRequests, "consumer tokens quota exceeded"
		}
	}
	return 0, ""
}

func requestLimits(record *configsnapshot.ConsumerAccess) []quota.RequestLimit {
	limits := make([]quota.RequestLimit, 0, len(record.Quotas))
	for _, configured := range record.Quotas {
		if configured.QuotaType != "requests" {
			continue
		}
		window, err := quota.ParseWindow(configured.Window)
		if err == nil {
			limits = append(limits, quota.RequestLimit{Limit: configured.QuotaLimit, Window: window})
		}
	}
	return limits
}

func acquireConcurrency(ctx context.Context, store quota.Store, record *configsnapshot.ConsumerAccess, leaseTTL time.Duration) (quota.Lease, int, string) {
	for _, configured := range record.Quotas {
		if configured.QuotaType != "concurrency" {
			continue
		}
		lease, allowed, err := store.Acquire(ctx, record.ConsumerID, configured.QuotaLimit, leaseTTL)
		if err != nil {
			return nil, statusServiceUnavailable, "quota state unavailable"
		}
		if !allowed {
			return nil, statusTooManyRequests, "consumer concurrency quota exceeded"
		}
		return lease, 0, ""
	}
	return nil, 0, ""
}

func concurrencyLeaseTTL(settings Settings) time.Duration {
	ttl := settings.RequestTimeout + time.Minute
	if ttl < 5*time.Minute {
		return 5 * time.Minute
	}
	return ttl
}

func releaseQuotaLease(lease quota.Lease, consumerID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := lease.Release(ctx); err != nil {
		slog.Error("release quota lease", "consumer_id", consumerID, "error", err)
		return err
	}
	return nil
}

func requestStreams(request llm.ModelRequest) bool {
	chat, ok := request.(*llm.ChatRequest)
	return ok && chat.Stream.Enabled
}

func statusOf(providerError *llm.Error, fallback int) int {
	if providerError != nil && providerError.StatusCode != nil {
		return int(*providerError.StatusCode)
	}
	return fallback
}

func errorFromExecution(err error) *llm.Error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return llm.ErrorFromStatus(statusRequestTimeout, "request timed out")
	case errors.Is(err, context.Canceled):
		return llm.NewError(llm.ErrUnknown, "request canceled")
	default:
		return llm.ErrorFromStatus(statusBadGateway, err.Error())
	}
}
