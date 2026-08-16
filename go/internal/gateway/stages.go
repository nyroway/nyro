package gateway

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/nyroway/nyro/go/internal/pipeline"
	"github.com/nyroway/nyro/go/internal/quota"
	"github.com/nyroway/nyro/go/internal/storage"
	"github.com/nyroway/nyro/go/internal/telemetry"
)

// The Stages below are the cross-cutting concerns the dispatcher used to run
// inline. Each writes its own error response and short-circuits by returning
// without calling next, so a rejected request never reaches an upstream while
// still unwinding through the telemetry Stage.

// routeStage resolves the client's model name to a route and publishes it on
// the Exchange. It runs before auth because the access check is per-route.
type routeStage struct{ gw *Gateway }

func (s routeStage) Name() string { return "route" }

func (s routeStage) Handle(ex *pipeline.Exchange, next func() error) error {
	rt := s.gw.snapshot().RouteByModel(ex.Req.Model)
	if rt == nil {
		writeJSONError(ex.W, http.StatusNotFound, "model not found: "+ex.Req.Model)
		return nil
	}
	if !rt.Enabled {
		writeJSONError(ex.W, http.StatusServiceUnavailable, "model disabled: "+ex.Req.Model)
		return nil
	}
	if len(rt.Upstreams) == 0 {
		writeJSONError(ex.W, http.StatusServiceUnavailable, "no backends for model: "+ex.Req.Model)
		return nil
	}
	ex.SetExt(telemetry.ExtRoute, *rt)
	return next()
}

// accessStage is the inbound access check: it authenticates the API key,
// verifies the route grant, and enforces the consumer's quotas. It is one
// Stage rather than three because the underlying checkAccess resolves the key
// once and the quota check needs that same record; splitting it would mean
// resolving the key twice or threading the record through the Exchange.
//
// A concurrency quota slot acquired here is released on the way out, which is
// why the release runs after next rather than in the dispatcher.
type accessStage struct{ gw *Gateway }

func (s accessStage) Name() string { return "access" }

func (s accessStage) Handle(ex *pipeline.Exchange, next func() error) error {
	route, ok := ex.GetExt(telemetry.ExtRoute).(storage.Route)
	if !ok {
		return next() // no route resolved: routeStage already responded
	}

	lc, _ := ex.GetExt(telemetry.ExtLogCtx).(telemetry.LogCtx)
	status, msg, lease := checkAccess(
		s.gw.snapshot(), s.gw.Quota, route, ex.R,
		&ex.ConsumerID, &lc.ConsumerKeyName, &lc.ConsumerKeyPreview,
	)
	ex.SetExt(telemetry.ExtLogCtx, lc)
	if status != 0 {
		writeJSONError(ex.W, status, msg)
		return nil
	}
	if lease != nil {
		defer func() {
			_ = releaseQuotaLease(lease, ex.ConsumerID)
		}()
	}
	return next()
}

// quotaStage records completed token usage on the way out.
//
// It runs after next so the token count reflects the completed exchange.
// consumerID is empty for unauthenticated/open requests — those are skipped.
// Request admission is counted before dispatch by accessStage, including when
// the admitted request's upstream exchange later fails.
type quotaStage struct{ gw *Gateway }

func (s quotaStage) Name() string { return "quota" }

func (s quotaStage) Handle(ex *pipeline.Exchange, next func() error) error {
	defer func() {
		if ex.ConsumerID == "" {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		tokens := int64(ex.Usage.PromptTokens) + int64(ex.Usage.CompletionTokens)
		if err := s.gw.Quota.RecordTokens(ctx, ex.ConsumerID, tokens); err != nil {
			slog.Error("record quota tokens", "consumer_id", ex.ConsumerID, "error", err)
		}
	}()
	return next()
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
