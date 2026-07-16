package admin

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/nyroway/nyro/go/internal/configsync"
	"github.com/nyroway/nyro/go/internal/observability"
	"github.com/nyroway/nyro/go/internal/provider"
	"github.com/nyroway/nyro/go/internal/storage"
	"github.com/nyroway/nyro/go/internal/version"
	"github.com/nyroway/nyro/go/internal/webutil"
)

// LogSource is the read side for /logs. Backed by parquet
// (observability.Logs) — the only request-log store after the Phase 4 removal
// of the request_logs table. The types are observability.* (JSON tags are
// identical to the old storage.* copies, so the WebUI contract is unchanged).
type LogSource interface {
	Query(q observability.LogQuery) (observability.LogPage, error)
	FindByID(id string) (*observability.LogRecord, error)
	ClearAll() (int64, error)
}

// StatsSource is the read side for /stats/*. Backed by metrics parquet
// (observability.AggregateStats / AggregateHourly).
type StatsSource interface {
	StatsOverview(hours int64) (observability.StatsOverview, error)
	StatsByModel(hours int64) ([]observability.ModelStats, error)
	StatsByProvider(hours int64) ([]observability.ProviderStats, error)
	StatsByApiKey(hours int64) ([]observability.ApiKeyStats, error)
	StatsHourly(hours int64) ([]observability.StatsHourly, error)
}

// Mount registers the admin REST API under /api/v1 on r. If adminToken is
// non-empty, every admin route requires Authorization: Bearer <adminToken>.
//
// logs/stats are the parquet-backed read sources (the observability store) —
// the only request-log/metrics path after the Phase 4 removal of the
// request_logs table. cmd/admin wires the real parquet-backed sources.
func Mount(r chi.Router, s storage.Storage, adminToken string, logs LogSource, stats StatsSource) {
	r.Route("/api/v1", func(g chi.Router) {
		if adminToken != "" {
			g.Use(bearerAuth(adminToken))
		}

		g.Get("/status", func(w http.ResponseWriter, r *http.Request) {
			upstreams, _ := s.Upstreams().List()
			routes, _ := s.Routes().List()
			consumers, _ := s.Consumers().List()
			health, _ := s.Migrator().Health()
			webutil.JSON(w, http.StatusOK, map[string]any{
				"status":         "ok",
				"version":        version.Version,
				"upstream_count": len(upstreams),
				"route_count":    len(routes),
				"consumer_count": len(consumers),
				"backend":        health.Backend,
				"writable":       health.Writable,
			})
		})

		// /nodes lists gateways currently connected over config-sync (best-effort,
		// in-memory — never persisted). Returns an empty array (not an error) when
		// config-sync is disabled (no --config-listen) or nothing is connected yet, so
		// the WebUI can render an empty state instead of handling a special error.
		g.Get("/nodes", func(w http.ResponseWriter, r *http.Request) {
			lister := nodeListerVal
			if lister == nil {
				webutil.JSON(w, http.StatusOK, []configsync.NodeInfo{})
				return
			}
			webutil.JSON(w, http.StatusOK, lister.Nodes())
		})

		// ── upstreams ──
		g.Get("/upstreams", func(w http.ResponseWriter, r *http.Request) { anyList(w, r, s.Upstreams().List) })
		g.Post("/upstreams", func(w http.ResponseWriter, r *http.Request) {
			var in storage.CreateUpstream
			if err := webutil.Decode(r, &in); err != nil {
				badRequest(w, err)
				return
			}
			if err := normalizeCreateUpstreamProtocol(&in); err != nil {
				badRequest(w, err)
				return
			}
			if err := validateNewUpstreamFields(in.Provider, in.BaseURL, in.ModelsJSON, in.ModelsURL); err != nil {
				badRequest(w, err)
				return
			}
			if exists, _ := s.Upstreams().ExistsByName(in.Name, ""); exists {
				conflict(w, "upstream name already exists")
				return
			}
			u, err := s.Upstreams().Create(in)
			if err == nil {
				bumpEpoch(s)
			}
			created(w, u, err)
		})
		g.Post("/upstreams/test-draft/stream", func(w http.ResponseWriter, r *http.Request) {
			var in storage.CreateUpstream
			if err := webutil.Decode(r, &in); err != nil {
				badRequest(w, err)
				return
			}
			streamDraftUpstreamHealth(w, r, s, in)
		})
		g.Post("/upstreams/{id}/test-draft/stream", func(w http.ResponseWriter, r *http.Request) {
			var in storage.CreateUpstream
			if err := webutil.Decode(r, &in); err != nil {
				badRequest(w, err)
				return
			}
			streamEditDraftUpstreamHealth(w, r, s, in, chi.URLParam(r, "id"))
		})
		g.Put("/upstreams/{id}", func(w http.ResponseWriter, r *http.Request) {
			var in storage.UpdateUpstream
			if err := webutil.Decode(r, &in); err != nil {
				badRequest(w, err)
				return
			}
			if err := normalizeUpdateUpstreamProtocol(&in); err != nil {
				badRequest(w, err)
				return
			}
			normalizeEmptyModelsJSON(&in)
			id := chi.URLParam(r, "id")
			existing, err := s.Upstreams().Get(id)
			if err != nil || existing == nil {
				webutil.JSON(w, http.StatusNotFound, map[string]any{"error": "upstream not found"})
				return
			}
			modelsJSON, modelsURL := existing.ModelsJSON, existing.ModelsURL
			if in.ModelsJSON != nil {
				modelsJSON = *in.ModelsJSON
			}
			if in.ModelsURL != nil {
				modelsURL = *in.ModelsURL
			}
			if err := validateModelsMutualExclusion(modelsJSON, modelsURL); err != nil {
				badRequest(w, err)
				return
			}
			u, err := s.Upstreams().Update(id, in)
			if err == nil {
				bumpEpoch(s)
				discoveryCache.invalidate(id)
			}
			ok(w, u, err)
		})
		g.Delete("/upstreams/{id}", func(w http.ResponseWriter, r *http.Request) {
			if err := s.Upstreams().Delete(chi.URLParam(r, "id")); err != nil {
				webutil.JSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
				return
			}
			bumpEpoch(s)
			discoveryCache.invalidate(chi.URLParam(r, "id"))
			w.WriteHeader(http.StatusNoContent)
		})
		g.Post("/upstreams/{id}/test", func(w http.ResponseWriter, r *http.Request) {
			u, err := s.Upstreams().Get(chi.URLParam(r, "id"))
			if err != nil || u == nil {
				webutil.JSON(w, http.StatusNotFound, map[string]any{"error": "upstream not found"})
				return
			}
			streamSavedUpstreamHealth(w, r, s, *u)
		})
		g.Post("/upstreams/{id}/routes/import/stream", func(w http.ResponseWriter, r *http.Request) {
			u, err := s.Upstreams().Get(chi.URLParam(r, "id"))
			if err != nil || u == nil {
				webutil.JSON(w, http.StatusNotFound, map[string]any{"error": "upstream not found"})
				return
			}
			streamImportUpstreamRoutes(w, r, s, *u)
		})
		g.Get("/upstreams/{id}/routes/import/preview", func(w http.ResponseWriter, r *http.Request) {
			u, err := s.Upstreams().Get(chi.URLParam(r, "id"))
			if err != nil || u == nil {
				webutil.JSON(w, http.StatusNotFound, map[string]any{"error": "upstream not found"})
				return
			}
			previewUpstreamRouteImport(w, r, s, *u)
		})
		g.Get("/upstreams/{id}/models", func(w http.ResponseWriter, r *http.Request) {
			u, err := s.Upstreams().Get(chi.URLParam(r, "id"))
			if err != nil || u == nil {
				webutil.JSON(w, http.StatusNotFound, map[string]any{"error": "upstream not found"})
				return
			}
			models, err := modelsForUpstream(r.Context(), *u)
			if err != nil {
				webutil.JSON(w, http.StatusOK, map[string]any{"models": []string{}, "error": err.Error()})
				return
			}
			webutil.JSON(w, http.StatusOK, map[string]any{"models": models})
		})

		// ── routes ──
		g.Get("/routes", func(w http.ResponseWriter, r *http.Request) { anyList(w, r, s.Routes().List) })
		g.Post("/routes", func(w http.ResponseWriter, r *http.Request) {
			var in storage.CreateRoute
			if err := webutil.Decode(r, &in); err != nil {
				badRequest(w, err)
				return
			}
			if exists, _ := s.Routes().ExistsByName(in.Model, ""); exists {
				conflict(w, "route model already exists")
				return
			}
			rt, err := s.Routes().Create(in)
			if err == nil {
				bumpEpoch(s)
			}
			created(w, rt, err)
		})
		g.Put("/routes/{id}", func(w http.ResponseWriter, r *http.Request) {
			var in storage.UpdateRoute
			if err := webutil.Decode(r, &in); err != nil {
				badRequest(w, err)
				return
			}
			rt, err := s.Routes().Update(chi.URLParam(r, "id"), in)
			if err == nil {
				bumpEpoch(s)
			}
			ok(w, rt, err)
		})
		g.Delete("/routes/{id}", func(w http.ResponseWriter, r *http.Request) {
			if err := s.Routes().Delete(chi.URLParam(r, "id")); err != nil {
				webutil.JSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
				return
			}
			bumpEpoch(s)
			w.WriteHeader(http.StatusNoContent)
		})

		// ── consumers ──
		g.Get("/consumers", func(w http.ResponseWriter, r *http.Request) { anyList(w, r, s.Consumers().List) })
		g.Post("/consumers", func(w http.ResponseWriter, r *http.Request) {
			var in storage.CreateConsumer
			if err := webutil.Decode(r, &in); err != nil {
				badRequest(w, err)
				return
			}
			c, err := s.Consumers().Create(in)
			if err == nil {
				bumpEpoch(s)
			}
			// The response's Keys[].Token carries each new key's raw value —
			// the one-time plaintext exposure; only prefix+hash are persisted.
			created(w, c, err)
		})
		g.Put("/consumers/{id}", func(w http.ResponseWriter, r *http.Request) {
			var in storage.UpdateConsumer
			if err := webutil.Decode(r, &in); err != nil {
				badRequest(w, err)
				return
			}
			c, err := s.Consumers().Update(chi.URLParam(r, "id"), in)
			if err == nil {
				bumpEpoch(s)
			}
			ok(w, c, err)
		})
		g.Delete("/consumers/{id}", func(w http.ResponseWriter, r *http.Request) {
			if err := s.Consumers().Delete(chi.URLParam(r, "id")); err != nil {
				webutil.JSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
				return
			}
			bumpEpoch(s)
			w.WriteHeader(http.StatusNoContent)
		})
		g.Post("/consumers/{id}/keys", func(w http.ResponseWriter, r *http.Request) {
			var in storage.CreateConsumerKey
			if err := webutil.Decode(r, &in); err != nil {
				badRequest(w, err)
				return
			}
			key, err := s.Consumers().AddKey(chi.URLParam(r, "id"), in)
			if err == nil {
				bumpEpoch(s)
			}
			// The response's Token carries the new key's raw value — the
			// one-time plaintext exposure; only prefix+hash are persisted.
			created(w, key, err)
		})
		g.Put("/consumers/{id}/keys/{keyId}", func(w http.ResponseWriter, r *http.Request) {
			var in storage.UpdateConsumerKey
			if err := webutil.Decode(r, &in); err != nil {
				badRequest(w, err)
				return
			}
			key, err := s.Consumers().UpdateKey(chi.URLParam(r, "keyId"), in)
			if err == nil {
				bumpEpoch(s)
			}
			ok(w, key, err)
		})
		g.Delete("/consumers/{id}/keys/{keyId}", func(w http.ResponseWriter, r *http.Request) {
			if err := s.Consumers().DeleteKey(chi.URLParam(r, "keyId")); err != nil {
				webutil.JSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
				return
			}
			bumpEpoch(s)
			w.WriteHeader(http.StatusNoContent)
		})

		// ── settings ──
		g.Get("/settings", func(w http.ResponseWriter, r *http.Request) {
			all, err := s.Settings().ListAll()
			if err != nil {
				webutil.JSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
				return
			}
			webutil.JSON(w, http.StatusOK, all)
		})
		g.Get("/settings/{key}", func(w http.ResponseWriter, r *http.Request) {
			key := chi.URLParam(r, "key")
			v, err := s.Settings().Get(key)
			if err != nil {
				webutil.JSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
				return
			}
			webutil.JSON(w, http.StatusOK, map[string]any{"key": key, "value": v})
		})
		g.Put("/settings/{key}", func(w http.ResponseWriter, r *http.Request) {
			key := chi.URLParam(r, "key")
			var body struct {
				Value string `json:"value"`
			}
			_ = webutil.Decode(r, &body)
			value, err := normalizeSettingValue(key, body.Value)
			if err != nil {
				badRequest(w, err)
				return
			}
			if err := s.Settings().Set(key, value); err != nil {
				webutil.JSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
				return
			}
			bumpEpoch(s)
			webutil.JSON(w, http.StatusOK, map[string]any{"key": key, "value": value})
		})

		// ── logs ──
		// /logs reads exclusively from the parquet LogSource (the request_logs
		// table was removed in Phase 4).
		g.Get("/logs", func(w http.ResponseWriter, r *http.Request) {
			q := observability.LogQuery{Provider: r.URL.Query().Get("provider"), Model: r.URL.Query().Get("model")}
			q.Limit, _ = strconv.ParseInt(r.URL.Query().Get("limit"), 10, 64)
			q.Offset, _ = strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
			page, err := logs.Query(q)
			if err != nil {
				webutil.JSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
				return
			}
			webutil.JSON(w, http.StatusOK, page)
		})
		g.Get("/logs/{id}", func(w http.ResponseWriter, r *http.Request) {
			l, err := logs.FindByID(chi.URLParam(r, "id"))
			if err != nil {
				webutil.JSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
				return
			}
			if l == nil {
				webutil.JSON(w, http.StatusNotFound, map[string]any{"error": "not found"})
				return
			}
			webutil.JSON(w, http.StatusOK, l)
		})
		g.Delete("/logs", func(w http.ResponseWriter, r *http.Request) {
			n, err := logs.ClearAll()
			if err != nil {
				webutil.JSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
				return
			}
			webutil.JSON(w, http.StatusOK, map[string]any{"cleared": n})
		})

		// ── stats ──
		parseHours := func(r *http.Request) int64 {
			// Mirrors c.DefaultQuery("hours", "0"): absent → "0" → 0; r.URL.Query
			// returns "" for absent keys, which also parses to 0.
			h, _ := strconv.ParseInt(r.URL.Query().Get("hours"), 10, 64)
			if h < 0 {
				h = 0
			}
			return h
		}
		g.Get("/stats/overview", func(w http.ResponseWriter, r *http.Request) {
			st, err := stats.StatsOverview(parseHours(r))
			if err != nil {
				webutil.JSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
				return
			}
			webutil.JSON(w, http.StatusOK, st)
		})
		g.Get("/stats/models", func(w http.ResponseWriter, r *http.Request) {
			st, err := stats.StatsByModel(parseHours(r))
			if err != nil {
				webutil.JSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
				return
			}
			webutil.JSON(w, http.StatusOK, st)
		})
		g.Get("/stats/providers", func(w http.ResponseWriter, r *http.Request) {
			st, err := stats.StatsByProvider(parseHours(r))
			if err != nil {
				webutil.JSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
				return
			}
			webutil.JSON(w, http.StatusOK, st)
		})
		g.Get("/stats/api-keys", func(w http.ResponseWriter, r *http.Request) {
			st, err := stats.StatsByApiKey(parseHours(r))
			if err != nil {
				webutil.JSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
				return
			}
			webutil.JSON(w, http.StatusOK, st)
		})
		g.Get("/stats/hourly", func(w http.ResponseWriter, r *http.Request) {
			hoursStr := r.URL.Query().Get("hours")
			if hoursStr == "" {
				hoursStr = "24"
			}
			hours, _ := strconv.ParseInt(hoursStr, 10, 64)
			if hours <= 0 {
				hours = 24
			}
			st, err := stats.StatsHourly(hours)
			if err != nil {
				webutil.JSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
				return
			}
			webutil.JSON(w, http.StatusOK, st)
		})
		g.Get("/provider-presets", func(w http.ResponseWriter, r *http.Request) {
			defs := provider.Definitions()
			out := make([]presetView, len(defs))
			for i, d := range defs {
				out[i] = toPresetView(d)
			}
			webutil.JSON(w, http.StatusOK, out)
		})
		g.Get("/protocol-credentials", func(w http.ResponseWriter, r *http.Request) {
			protocols := []string{
				provider.ProtocolOpenAIChatCompletions,
				provider.ProtocolOpenAIResponses,
				provider.ProtocolAnthropicMessages,
				provider.ProtocolGeminiGenerateContent,
			}
			out := make([]protocolCredentialsView, len(protocols))
			for i, p := range protocols {
				out[i] = protocolCredentialsView{
					Protocol: p,
					Fields:   toCredentialFieldViews(provider.CredentialSchemaFor(p).Fields),
				}
			}
			webutil.JSON(w, http.StatusOK, out)
		})
	})
}

// testHTTPClient returns the HTTP client used by admin-side upstream discovery
// and health checks, routed through proxyURL when it's a valid absolute URL.
// This mirrors the same-purpose logic in the data-plane gateway
// (internal/proxy/gateway.go's httpClientFor) so an admin test and a real
// request take the same route, but skips caching because these calls are not
// on the request hot path.
func testHTTPClient(proxyURL string, timeout time.Duration) *http.Client {
	proxyURL = strings.TrimSpace(proxyURL)
	if proxyURL == "" {
		return &http.Client{Timeout: timeout}
	}
	parsed, err := url.Parse(proxyURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return &http.Client{Timeout: timeout}
	}
	return &http.Client{Timeout: timeout, Transport: &http.Transport{Proxy: http.ProxyURL(parsed)}}
}

func bearerAuth(token string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := r.Header.Get("Authorization")
			if !strings.HasPrefix(h, "Bearer ") || strings.TrimPrefix(h, "Bearer ") != token {
				webutil.Error(w, http.StatusUnauthorized, "unauthorized", "AUTH_ERROR")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// anyList renders the result of a list func.
func anyList[T any](w http.ResponseWriter, _ *http.Request, f func() ([]T, error)) {
	items, err := f()
	if err != nil {
		webutil.JSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	webutil.JSON(w, http.StatusOK, items)
}

func ok(w http.ResponseWriter, v any, err error) {
	if err != nil {
		webutil.JSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	webutil.JSON(w, http.StatusOK, v)
}

func created(w http.ResponseWriter, v any, err error) {
	if err != nil {
		webutil.JSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	webutil.JSON(w, http.StatusCreated, v)
}

func badRequest(w http.ResponseWriter, err error) {
	webutil.Error(w, http.StatusBadRequest, err.Error(), "invalid_request")
}

func conflict(w http.ResponseWriter, msg string) {
	webutil.Error(w, http.StatusConflict, msg, "NAME_CONFLICT")
}

// bumpEpoch increments the config_epoch setting so multi-replica deployments can
// detect config changes and reload. Ported from admin/settings.rs bump_config_epoch.
func bumpEpoch(s storage.Storage) {
	v, _ := s.Settings().Get("config_epoch")
	n, _ := strconv.ParseInt(v, 10, 64)
	n++
	_ = s.Settings().Set("config_epoch", strconv.FormatInt(n, 10))
	// Drive notification through the EpochWatcher when one is wired: it is
	// the single decision point for whether/when to push, so a write made on
	// this replica and the same write observed via polling by a peer replica
	// (sharing the same DB) converge on calling Notify exactly once each.
	// Fall back to the raw Broadcaster for standalone/tests where no watcher
	// is registered (config-sync disabled, or single-instance deployments
	// with polling turned off).
	if w := epochWatcher(); w != nil {
		w.Observe(n)
		return
	}
	if b := configBroadcaster(); b != nil {
		b.Notify()
	}
}

// Broadcaster pushes a fresh config snapshot to connected gateways. The
// admin's config-sync ConfigServer implements it; it is optional (nil when
// config-sync is disabled).
type Broadcaster interface {
	Notify()
}

var configBroadcasterVal Broadcaster

// SetBroadcaster wires the config-sync push target. Call once at admin
// startup (after Mount) if the gRPC ConfigServer is enabled. Safe to pass nil
// to disable.
func SetBroadcaster(b Broadcaster) { configBroadcasterVal = b }

func configBroadcaster() Broadcaster { return configBroadcasterVal }

// EpochObserver is implemented by configsync.EpochWatcher. It replaces the
// raw Broadcaster as bumpEpoch's push target whenever multi-replica epoch
// polling is enabled: Observe deduplicates by epoch so the same config write
// isn't pushed twice (once locally here, once when this replica's own poll
// tick later catches up to the epoch it just wrote).
type EpochObserver interface {
	Observe(epoch int64)
}

var epochWatcherVal EpochObserver

// SetEpochWatcher wires the config-sync epoch watcher. Call once at admin
// startup (after Mount) if the gRPC ConfigServer and epoch polling are
// enabled. Safe to pass nil to fall back to the raw Broadcaster.
func SetEpochWatcher(w EpochObserver) { epochWatcherVal = w }

func epochWatcher() EpochObserver { return epochWatcherVal }

// NodeLister exposes the set of gateways currently connected over
// config-sync, for the /api/v1/nodes admin endpoint. The admin's config-sync
// ConfigServer implements it; it is optional (nil when config-sync is
// disabled, in which case /nodes reports an empty list rather than erroring).
type NodeLister interface {
	Nodes() []configsync.NodeInfo
}

var nodeListerVal NodeLister

// SetNodeLister wires the config-sync node registry. Call once at admin
// startup (after Mount) if the gRPC ConfigServer is enabled. Safe to pass nil
// to disable.
func SetNodeLister(l NodeLister) { nodeListerVal = l }
