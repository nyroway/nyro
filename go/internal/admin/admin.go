// Package admin mounts the management REST API (under /api/v1) consumed by the
// React WebUI and the CLI. Handlers are thin wrappers over storage.Storage
// (config-schema: upstreams/routes/consumers).
package admin

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/nyroway/nyro/go/internal/observability"
	"github.com/nyroway/nyro/go/internal/plugin"
	"github.com/nyroway/nyro/go/internal/provider"
	"github.com/nyroway/nyro/go/internal/storage"
	"github.com/nyroway/nyro/go/internal/web"
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
			health, _ := s.Bootstrap().Health()
			web.JSON(w, http.StatusOK, map[string]any{
				"status":         "ok",
				"upstream_count": len(upstreams),
				"route_count":    len(routes),
				"consumer_count": len(consumers),
				"backend":        health.Backend,
				"writable":       health.Writable,
			})
		})

		// ── upstreams ──
		g.Get("/upstreams", func(w http.ResponseWriter, r *http.Request) { anyList(w, r, s.Upstreams().List) })
		g.Post("/upstreams", func(w http.ResponseWriter, r *http.Request) {
			var in storage.CreateUpstream
			if err := web.Decode(r, &in); err != nil {
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
		g.Put("/upstreams/{id}", func(w http.ResponseWriter, r *http.Request) {
			var in storage.UpdateUpstream
			if err := web.Decode(r, &in); err != nil {
				badRequest(w, err)
				return
			}
			u, err := s.Upstreams().Update(chi.URLParam(r, "id"), in)
			ok(w, u, err)
		})
		g.Delete("/upstreams/{id}", func(w http.ResponseWriter, r *http.Request) {
			if err := s.Upstreams().Delete(chi.URLParam(r, "id")); err != nil {
				web.JSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
				return
			}
			bumpEpoch(s)
			w.WriteHeader(http.StatusNoContent)
		})
		g.Post("/upstreams/{id}/test", func(w http.ResponseWriter, r *http.Request) {
			u, err := s.Upstreams().Get(chi.URLParam(r, "id"))
			if err != nil || u == nil {
				web.JSON(w, http.StatusNotFound, map[string]any{"error": "upstream not found"})
				return
			}
			var cred struct {
				APIKey string `json:"api_key"`
			}
			_ = json.Unmarshal(u.CredentialsJSON, &cred)

			modelsURL := ""
			if def, ok := provider.Lookup(u.Provider); ok && def.Models.Kind == provider.KindDynamic {
				modelsURL = def.Models.URL
			}
			if modelsURL == "" {
				modelsURL = strings.TrimRight(u.BaseURL, "/") + "/models"
			}
			req, _ := http.NewRequest("GET", modelsURL, nil)
			if u.Protocol == provider.ProtocolGoogleGemini {
				req.Header.Set("x-goog-api-key", cred.APIKey)
			} else {
				req.Header.Set("Authorization", "Bearer "+cred.APIKey)
			}
			client := &http.Client{Timeout: 10 * time.Second}
			start := time.Now()
			resp, err := client.Do(req)
			latency := time.Since(start).Milliseconds()
			if err != nil {
				web.JSON(w, http.StatusOK, map[string]any{"success": false, "latency_ms": latency, "error": err.Error()})
				return
			}
			resp.Body.Close()
			success := resp.StatusCode < 400
			web.JSON(w, http.StatusOK, map[string]any{"success": success, "latency_ms": latency, "status_code": resp.StatusCode})
		})

		// ── routes ──
		g.Get("/routes", func(w http.ResponseWriter, r *http.Request) { anyList(w, r, s.Routes().List) })
		g.Post("/routes", func(w http.ResponseWriter, r *http.Request) {
			var in storage.CreateRoute
			if err := web.Decode(r, &in); err != nil {
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
			if err := web.Decode(r, &in); err != nil {
				badRequest(w, err)
				return
			}
			rt, err := s.Routes().Update(chi.URLParam(r, "id"), in)
			ok(w, rt, err)
		})
		g.Delete("/routes/{id}", func(w http.ResponseWriter, r *http.Request) {
			if err := s.Routes().Delete(chi.URLParam(r, "id")); err != nil {
				web.JSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
				return
			}
			bumpEpoch(s)
			w.WriteHeader(http.StatusNoContent)
		})

		// ── consumers ──
		g.Get("/consumers", func(w http.ResponseWriter, r *http.Request) { anyList(w, r, s.Consumers().List) })
		g.Post("/consumers", func(w http.ResponseWriter, r *http.Request) {
			var in storage.CreateConsumer
			if err := web.Decode(r, &in); err != nil {
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
			if err := web.Decode(r, &in); err != nil {
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
				web.JSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
				return
			}
			bumpEpoch(s)
			w.WriteHeader(http.StatusNoContent)
		})

		// ── settings ──
		g.Get("/settings", func(w http.ResponseWriter, r *http.Request) {
			all, err := s.Settings().ListAll()
			if err != nil {
				web.JSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
				return
			}
			web.JSON(w, http.StatusOK, all)
		})
		g.Get("/settings/{key}", func(w http.ResponseWriter, r *http.Request) {
			key := chi.URLParam(r, "key")
			v, err := s.Settings().Get(key)
			if err != nil {
				web.JSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
				return
			}
			web.JSON(w, http.StatusOK, map[string]any{"key": key, "value": v})
		})
		g.Put("/settings/{key}", func(w http.ResponseWriter, r *http.Request) {
			key := chi.URLParam(r, "key")
			var body struct {
				Value string `json:"value"`
			}
			_ = web.Decode(r, &body)
			if err := s.Settings().Set(key, body.Value); err != nil {
				web.JSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
				return
			}
			bumpEpoch(s)
			web.JSON(w, http.StatusOK, map[string]any{"key": key, "value": body.Value})
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
				web.JSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
				return
			}
			web.JSON(w, http.StatusOK, page)
		})
		g.Get("/logs/{id}", func(w http.ResponseWriter, r *http.Request) {
			l, err := logs.FindByID(chi.URLParam(r, "id"))
			if err != nil {
				web.JSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
				return
			}
			if l == nil {
				web.JSON(w, http.StatusNotFound, map[string]any{"error": "not found"})
				return
			}
			web.JSON(w, http.StatusOK, l)
		})
		g.Delete("/logs", func(w http.ResponseWriter, r *http.Request) {
			n, err := logs.ClearAll()
			if err != nil {
				web.JSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
				return
			}
			web.JSON(w, http.StatusOK, map[string]any{"cleared": n})
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
				web.JSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
				return
			}
			web.JSON(w, http.StatusOK, st)
		})
		g.Get("/stats/models", func(w http.ResponseWriter, r *http.Request) {
			st, err := stats.StatsByModel(parseHours(r))
			if err != nil {
				web.JSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
				return
			}
			web.JSON(w, http.StatusOK, st)
		})
		g.Get("/stats/providers", func(w http.ResponseWriter, r *http.Request) {
			st, err := stats.StatsByProvider(parseHours(r))
			if err != nil {
				web.JSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
				return
			}
			web.JSON(w, http.StatusOK, st)
		})
		g.Get("/stats/api-keys", func(w http.ResponseWriter, r *http.Request) {
			st, err := stats.StatsByApiKey(parseHours(r))
			if err != nil {
				web.JSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
				return
			}
			web.JSON(w, http.StatusOK, st)
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
				web.JSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
				return
			}
			web.JSON(w, http.StatusOK, st)
		})
		g.Get("/extensions", func(w http.ResponseWriter, r *http.Request) {
			web.JSON(w, http.StatusOK, map[string]any{"count": plugin.Kernel().Count()})
		})
		g.Get("/provider-presets", func(w http.ResponseWriter, r *http.Request) {
			defs := provider.Definitions()
			out := make([]presetView, len(defs))
			for i, d := range defs {
				out[i] = toPresetView(d)
			}
			web.JSON(w, http.StatusOK, out)
		})
		g.Get("/config/export", func(w http.ResponseWriter, r *http.Request) {
			upstreams, _ := s.Upstreams().List()
			routes, _ := s.Routes().List()
			consumers, _ := s.Consumers().List()
			settings, _ := s.Settings().ListAll()
			web.JSON(w, http.StatusOK, map[string]any{
				"version": 1, "upstreams": upstreams, "routes": routes, "consumers": consumers, "settings": settings,
			})
		})
		g.Post("/config/import", func(w http.ResponseWriter, r *http.Request) {
			var body struct {
				Upstreams []storage.CreateUpstream `json:"upstreams"`
				Routes    []storage.CreateRoute    `json:"routes"`
				Consumers []storage.CreateConsumer `json:"consumers"`
				Settings  []storage.Setting        `json:"settings"`
			}
			if err := web.Decode(r, &body); err != nil {
				badRequest(w, err)
				return
			}
			var upCount, routeCount, consumerCount, setCount int
			for _, u := range body.Upstreams {
				if _, err := s.Upstreams().Create(u); err == nil {
					upCount++
				}
			}
			for _, rt := range body.Routes {
				if _, err := s.Routes().Create(rt); err == nil {
					routeCount++
				}
			}
			for _, c := range body.Consumers {
				if _, err := s.Consumers().Create(c); err == nil {
					consumerCount++
				}
			}
			for _, set := range body.Settings {
				if err := s.Settings().Set(set.Key, set.Value); err == nil {
					setCount++
				}
			}
			web.JSON(w, http.StatusOK, map[string]any{
				"upstreams_imported": upCount, "routes_imported": routeCount,
				"consumers_imported": consumerCount, "settings_imported": setCount,
			})
		})
	})
}

func bearerAuth(token string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := r.Header.Get("Authorization")
			if !strings.HasPrefix(h, "Bearer ") || strings.TrimPrefix(h, "Bearer ") != token {
				web.Error(w, http.StatusUnauthorized, "unauthorized", "auth_error")
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
		web.JSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	web.JSON(w, http.StatusOK, items)
}

func ok(w http.ResponseWriter, v any, err error) {
	if err != nil {
		web.JSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	web.JSON(w, http.StatusOK, v)
}

func created(w http.ResponseWriter, v any, err error) {
	if err != nil {
		web.JSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	web.JSON(w, http.StatusCreated, v)
}

func badRequest(w http.ResponseWriter, err error) {
	web.Error(w, http.StatusBadRequest, err.Error(), "invalid_request")
}

func conflict(w http.ResponseWriter, msg string) {
	web.Error(w, http.StatusConflict, msg, "NAME_CONFLICT")
}

// bumpEpoch increments the config_epoch setting so multi-replica deployments can
// detect config changes and reload. Ported from admin/settings.rs bump_config_epoch.
func bumpEpoch(s storage.Storage) {
	v, _ := s.Settings().Get("config_epoch")
	n, _ := strconv.ParseInt(v, 10, 64)
	_ = s.Settings().Set("config_epoch", strconv.FormatInt(n+1, 10))
	// Push the new config to every connected gateway over xDS, if a broadcaster
	// (the admin's gRPC ConfigServer) has been wired in. No-op in tests/standalone.
	if b := configBroadcaster(); b != nil {
		b.Notify()
	}
}

// Broadcaster pushes a fresh config snapshot to connected gateways. The admin's
// xDS ConfigServer implements it; it is optional (nil when xDS is disabled).
type Broadcaster interface {
	Notify()
}

var (
	configBroadcasterVal Broadcaster
)

// SetBroadcaster wires the xDS push target. Call once at admin startup (after
// Mount) if the gRPC ConfigServer is enabled. Safe to pass nil to disable.
func SetBroadcaster(b Broadcaster) { configBroadcasterVal = b }

func configBroadcaster() Broadcaster { return configBroadcasterVal }
