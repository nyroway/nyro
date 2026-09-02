package gateway

import (
	"context"
	"log/slog"
	"net/http"
	"slices"
	"time"

	configsnapshot "github.com/nyroway/nyro/go/internal/config/snapshot"
	"github.com/nyroway/nyro/go/internal/llm"
	llmpipeline "github.com/nyroway/nyro/go/internal/llm/pipeline"
	"github.com/nyroway/nyro/go/internal/llm/protocol"
	"github.com/nyroway/nyro/go/internal/quota"
	"github.com/nyroway/nyro/go/internal/security/authn"
	"github.com/nyroway/nyro/go/internal/security/authz"
	"github.com/nyroway/nyro/go/internal/telemetry"
)

// gatewayPipelineState is a request-local transition adapter. It may retain
// HTTP and full Snapshot values because it never crosses into llm/pipeline.
type gatewayPipelineState struct {
	gw       *Gateway
	snapshot *configsnapshot.Snapshot
	writer   http.ResponseWriter
	ingress  protocol.IngressCodec
	route    *configsnapshot.Route
	access   *configsnapshot.ConsumerAccess
	runner   *llmpipeline.Runner
}

func (g *Gateway) newPipelineRunner(state *gatewayPipelineState) (*llmpipeline.Runner, error) {
	observe := g.observePhase
	if observe == nil {
		observe = telemetry.NewRegisteredPhase()
	}
	runner, err := llmpipeline.NewRunner(llmpipeline.PhaseSet{
		Observe:      observe,
		Resolve:      resolvePhase{state: state},
		Authenticate: authenticatePhase{state: state},
		Authorize:    authorizePhase{state: state},
		Admit:        admitPhase{state: state},
		Dispatch:     dispatchPhase{state: state},
	})
	if err != nil {
		return nil, err
	}
	state.runner = runner
	return runner, nil
}

type resolvePhase struct{ state *gatewayPipelineState }

func (resolvePhase) Name() string { return "resolve" }

func (p resolvePhase) Apply(_ context.Context, ex *llmpipeline.Exchange) (llmpipeline.Outcome, llmpipeline.Finalizer) {
	model := ex.Request.ModelID()
	route := p.state.snapshot.RouteByModel(model)
	switch {
	case route == nil:
		return reject(ex, http.StatusNotFound, "model not found: "+model), nil
	case !route.Enabled:
		return reject(ex, http.StatusServiceUnavailable, "model disabled: "+model), nil
	case len(route.Upstreams) == 0:
		return reject(ex, http.StatusServiceUnavailable, "no backends for model: "+model), nil
	}
	p.state.route = route
	ex.Route = llmpipeline.LogicalRoute{ID: route.ID, Model: route.Model}
	return continueOutcome(), nil
}

type authenticatePhase struct{ state *gatewayPipelineState }

func (authenticatePhase) Name() string { return "authenticate" }

func (p authenticatePhase) Apply(_ context.Context, ex *llmpipeline.Exchange) (llmpipeline.Outcome, llmpipeline.Finalizer) {
	if !p.state.route.EnableAuth {
		ex.Identity = authn.Identity{Anonymous: true}
		return continueOutcome(), nil
	}
	if ex.Credentials.APIKey == "" {
		return reject(ex, http.StatusUnauthorized, "missing API key"), nil
	}
	record := p.state.snapshot.FindKey(ex.Credentials.APIKey)
	if record == nil {
		return reject(ex, http.StatusUnauthorized, "invalid API key"), nil
	}
	p.state.access = record
	ex.Identity = authn.Identity{
		Subject:           record.ConsumerID,
		CredentialID:      record.KeyID,
		CredentialName:    record.Name,
		CredentialPreview: record.KeyPreview,
	}
	if ex.Identity.CredentialName == "" {
		ex.Identity.CredentialName = record.KeyPreview
	}
	if !record.Enabled {
		return reject(ex, http.StatusForbidden, "API key is disabled"), nil
	}
	if record.ExpiresAt != "" && expired(record.ExpiresAt) {
		return reject(ex, http.StatusForbidden, "API key has expired"), nil
	}
	return continueOutcome(), nil
}

type authorizePhase struct{ state *gatewayPipelineState }

func (authorizePhase) Name() string { return "authorize" }

func (p authorizePhase) Apply(_ context.Context, ex *llmpipeline.Exchange) (llmpipeline.Outcome, llmpipeline.Finalizer) {
	request := authz.Request{
		Identity: ex.Identity,
		RouteID:  ex.Route.ID,
		Model:    ex.Route.Model,
		Action:   authz.InvokeModel,
	}
	if request.Identity.Anonymous {
		ex.Authorization = authz.Decision{Allowed: true}
		return continueOutcome(), nil
	}
	if p.state.access == nil || !slices.Contains(p.state.access.Routes, request.Model) {
		ex.Authorization = authz.Decision{Reason: "API key is not granted this route"}
		return reject(ex, http.StatusForbidden, ex.Authorization.Reason), nil
	}
	ex.Authorization = authz.Decision{Allowed: true}
	return continueOutcome(), nil
}

type admitPhase struct{ state *gatewayPipelineState }

func (admitPhase) Name() string { return "admit" }

func (p admitPhase) Apply(ctx context.Context, ex *llmpipeline.Exchange) (llmpipeline.Outcome, llmpipeline.Finalizer) {
	record := p.state.access
	if record == nil {
		return continueOutcome(), nil
	}
	if p.state.gw.Quota == nil {
		return reject(ex, http.StatusServiceUnavailable, "quota state unavailable"), nil
	}
	if status, message := tokenQuotaExceeded(ctx, p.state.gw.Quota, record); status != 0 {
		return reject(ex, status, message), nil
	}

	lease, status, message := acquireConcurrency(ctx, p.state.gw.Quota, record, concurrencyLeaseTTL(p.state.snapshot))
	if status != 0 {
		return reject(ex, status, message), nil
	}
	limits := requestLimits(record)
	if len(limits) > 0 {
		allowed, err := p.state.gw.Quota.AdmitRequest(ctx, record.ConsumerID, limits)
		if err != nil || !allowed {
			if lease != nil {
				if releaseErr := releaseQuotaLease(lease, record.ConsumerID); releaseErr != nil {
					return reject(ex, http.StatusServiceUnavailable, "quota state unavailable"), nil
				}
			}
			if err != nil {
				return reject(ex, http.StatusServiceUnavailable, "quota state unavailable"), nil
			}
			return reject(ex, http.StatusTooManyRequests, "consumer requests quota exceeded"), nil
		}
	}

	consumerID := record.ConsumerID
	return continueOutcome(), func(_ context.Context, _ *llmpipeline.Exchange, completion llmpipeline.Completion) error {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		tokens := int64(completion.Usage.PromptTokens) + int64(completion.Usage.CompletionTokens)
		if err := p.state.gw.Quota.RecordTokens(ctx, consumerID, tokens); err != nil {
			slog.Error("record quota tokens", "consumer_id", consumerID, "error", err)
		}
		if lease != nil {
			return releaseQuotaLease(lease, consumerID)
		}
		return nil
	}
}

type dispatchPhase struct{ state *gatewayPipelineState }

func (dispatchPhase) Name() string { return "dispatch" }

func (p dispatchPhase) Apply(ctx context.Context, ex *llmpipeline.Exchange) (llmpipeline.Outcome, llmpipeline.Finalizer) {
	p.state.gw.forward(ctx, p.state.writer, p.state.runner, ex, *p.state.route, p.state.ingress, p.state.snapshot)
	return continueOutcome(), nil
}

func continueOutcome() llmpipeline.Outcome {
	return llmpipeline.Outcome{Decision: llmpipeline.Continue}
}

func reject(ex *llmpipeline.Exchange, status int, message string) llmpipeline.Outcome {
	ex.Status = status
	return llmpipeline.Outcome{
		Decision: llmpipeline.Reject,
		Error:    llm.ErrorFromStatus(uint16(status), message),
	}
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
