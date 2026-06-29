// Package admin mounts the management REST API (under /api/v1) consumed by the
// React WebUI and the CLI. Handlers are thin wrappers over storage.Storage.
// Ported (scoped) from crates/nyro-core/src/admin/.
package admin

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nyroway/nyro/go/internal/plugin"
	"github.com/nyroway/nyro/go/internal/provider"
	"github.com/nyroway/nyro/go/internal/storage"
)

// Mount registers the admin REST API under /api/v1 on r. If adminToken is
// non-empty, every admin route requires Authorization: Bearer <adminToken>.
func Mount(r gin.IRouter, s storage.Storage, adminToken string) {
	g := r.Group("/api/v1")
	if adminToken != "" {
		g.Use(bearerAuth(adminToken))
	}

	g.GET("/status", func(c *gin.Context) {
		providers, _ := s.Providers().List()
		models, _ := s.Models().List()
		keys, _ := s.APIKeys().List()
		health, _ := s.Bootstrap().Health()
		c.JSON(http.StatusOK, gin.H{
			"status":         "ok",
			"provider_count": len(providers),
			"model_count":    len(models),
			"api_key_count":  len(keys),
			"backend":        health.Backend,
			"writable":       health.Writable,
		})
	})

	// ── providers ──
	g.GET("/providers", func(c *gin.Context) { anyList(c, s.Providers().List) })
	g.POST("/providers", func(c *gin.Context) {
		var in storage.CreateProvider
		if err := c.ShouldBindJSON(&in); err != nil {
			badRequest(c, err)
			return
		}
		if exists, _ := s.Providers().ExistsByName(in.Name, ""); exists {
			conflict(c, "provider name already exists")
			return
		}
		p, err := s.Providers().Create(in)
		if err == nil {
			bumpEpoch(s)
		}
		created(c, p, err)
	})
	g.PUT("/providers/:id", func(c *gin.Context) {
		var in storage.UpdateProvider
		if err := c.ShouldBindJSON(&in); err != nil {
			badRequest(c, err)
			return
		}
		p, err := s.Providers().Update(c.Param("id"), in)
		ok(c, p, err)
	})
	g.DELETE("/providers/:id", func(c *gin.Context) {
		if err := s.Providers().Delete(c.Param("id")); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		bumpEpoch(s)
		c.Status(http.StatusNoContent)
	})
	g.POST("/providers/:id/test", func(c *gin.Context) {
		p, err := s.Providers().Get(c.Param("id"))
		if err != nil || p == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "provider not found"})
			return
		}
		modelsURL := p.ModelsSource
		if modelsURL == "" {
			modelsURL = strings.TrimRight(p.BaseURL, "/") + "/models"
		}
		req, _ := http.NewRequest("GET", modelsURL, nil)
		if p.Protocol == "google-gemini" {
			req.Header.Set("x-goog-api-key", p.APIKey)
		} else {
			req.Header.Set("Authorization", "Bearer "+p.APIKey)
		}
		client := &http.Client{Timeout: 10 * time.Second}
		start := time.Now()
		resp, err := client.Do(req)
		latency := time.Since(start).Milliseconds()
		if err != nil {
			_ = s.Providers().RecordTestResult(p.ID, storage.ProviderTestResult{Success: false, TestedAt: time.Now().UTC().Format(time.RFC3339)})
			c.JSON(http.StatusOK, gin.H{"success": false, "latency_ms": latency, "error": err.Error()})
			return
		}
		resp.Body.Close()
		success := resp.StatusCode < 400
		_ = s.Providers().RecordTestResult(p.ID, storage.ProviderTestResult{Success: success, TestedAt: time.Now().UTC().Format(time.RFC3339)})
		c.JSON(http.StatusOK, gin.H{"success": success, "latency_ms": latency, "status_code": resp.StatusCode})
	})

	// ── models ──
	g.GET("/models", func(c *gin.Context) { anyList(c, s.Models().List) })
	g.POST("/models", func(c *gin.Context) {
		var in storage.CreateModel
		if err := c.ShouldBindJSON(&in); err != nil {
			badRequest(c, err)
			return
		}
		if exists, _ := s.Models().ExistsByName(in.Name, ""); exists {
			conflict(c, "model name already exists")
			return
		}
		m, err := s.Models().Create(in)
		if err == nil {
			bumpEpoch(s)
		}
		created(c, m, err)
	})
	g.PUT("/models/:id", func(c *gin.Context) {
		var in storage.UpdateModel
		if err := c.ShouldBindJSON(&in); err != nil {
			badRequest(c, err)
			return
		}
		m, err := s.Models().Update(c.Param("id"), in)
		ok(c, m, err)
	})
	g.DELETE("/models/:id", func(c *gin.Context) {
		if err := s.Models().Delete(c.Param("id")); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		bumpEpoch(s)
		c.Status(http.StatusNoContent)
	})

	// ── api-keys ──
	g.GET("/api-keys", func(c *gin.Context) { anyList(c, s.APIKeys().List) })
	g.POST("/api-keys", func(c *gin.Context) {
		var in storage.CreateApiKey
		if err := c.ShouldBindJSON(&in); err != nil {
			badRequest(c, err)
			return
		}
		if exists, _ := s.APIKeys().ExistsByName(in.Name, ""); exists {
			conflict(c, "API key name already exists")
			return
		}
		k, err := s.APIKeys().Create(in)
		if err == nil {
			bumpEpoch(s)
		}
		created(c, k, err)
	})
	g.PUT("/api-keys/:id", func(c *gin.Context) {
		var in storage.UpdateApiKey
		if err := c.ShouldBindJSON(&in); err != nil {
			badRequest(c, err)
			return
		}
		k, err := s.APIKeys().Update(c.Param("id"), in)
		ok(c, k, err)
	})
	g.DELETE("/api-keys/:id", func(c *gin.Context) {
		if err := s.APIKeys().Delete(c.Param("id")); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		bumpEpoch(s)
		c.Status(http.StatusNoContent)
	})

	// ── settings ──
	g.GET("/settings", func(c *gin.Context) {
		all, err := s.Settings().ListAll()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, all)
	})
	g.GET("/settings/:key", func(c *gin.Context) {
		v, err := s.Settings().Get(c.Param("key"))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"key": c.Param("key"), "value": v})
	})
	g.PUT("/settings/:key", func(c *gin.Context) {
		var body struct {
			Value string `json:"value"`
		}
		_ = c.ShouldBindJSON(&body)
		if err := s.Settings().Set(c.Param("key"), body.Value); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		bumpEpoch(s)
		c.JSON(http.StatusOK, gin.H{"key": c.Param("key"), "value": body.Value})
	})

	// ── logs ──
	g.GET("/logs", func(c *gin.Context) {
		q := storage.LogQuery{Provider: c.Query("provider"), Model: c.Query("model")}
		q.Limit, _ = strconv.ParseInt(c.Query("limit"), 10, 64)
		q.Offset, _ = strconv.ParseInt(c.Query("offset"), 10, 64)
		page, err := s.Logs().Query(q)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, page)
	})
	g.GET("/logs/:id", func(c *gin.Context) {
		l, err := s.Logs().FindByID(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if l == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		c.JSON(http.StatusOK, l)
	})
	g.DELETE("/logs", func(c *gin.Context) {
		n, err := s.Logs().ClearAll()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"cleared": n})
	})

	// ── stats ──
	g.GET("/stats/overview", func(c *gin.Context) {
		st, err := s.Logs().StatsOverview()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, st)
	})
	g.GET("/stats/by-model", func(c *gin.Context) {
		st, err := s.Logs().StatsByModel()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, st)
	})
	g.GET("/stats/by-provider", func(c *gin.Context) {
		st, err := s.Logs().StatsByProvider()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, st)
	})
	g.GET("/stats/hourly", func(c *gin.Context) {
		hours, _ := strconv.ParseInt(c.DefaultQuery("hours", "24"), 10, 64)
		if hours <= 0 {
			hours = 24
		}
		st, err := s.Logs().StatsHourly(hours)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, st)
	})
	g.GET("/extensions", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"count": plugin.Kernel().Count()})
	})
	g.GET("/provider-presets", func(c *gin.Context) {
		c.JSON(http.StatusOK, provider.Presets)
	})
	// ── OAuth status + disconnect (session init/poll/complete requires driver implementations) ──
	g.GET("/providers/:id/oauth", func(c *gin.Context) {
		cred, _ := s.OAuthCredentials().Get(c.Param("id"))
		if cred == nil {
			c.JSON(http.StatusOK, gin.H{"connected": false})
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"connected": cred.Status == "connected",
			"status":    cred.Status,
			"driver":    cred.DriverKey,
			"expires":   cred.ExpiresAt,
		})
	})
	g.DELETE("/providers/:id/oauth", func(c *gin.Context) {
		_ = s.OAuthCredentials().Delete(c.Param("id"))
		c.Status(http.StatusNoContent)
	})
	g.GET("/config/export", func(c *gin.Context) {
		providers, _ := s.Providers().List()
		models, _ := s.Models().List()
		settings, _ := s.Settings().ListAll()
		c.JSON(http.StatusOK, gin.H{"version": 1, "providers": providers, "models": models, "settings": settings})
	})
	g.POST("/config/import", func(c *gin.Context) {
		var body struct {
			Providers []storage.CreateProvider `json:"providers"`
			Models    []storage.CreateModel    `json:"models"`
			Settings  []storage.Setting        `json:"settings"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			badRequest(c, err)
			return
		}
		var provCount, modelCount, setCount int
		for _, p := range body.Providers {
			if _, err := s.Providers().Create(p); err == nil {
				provCount++
			}
		}
		for _, m := range body.Models {
			if _, err := s.Models().Create(m); err == nil {
				modelCount++
			}
		}
		for _, set := range body.Settings {
			if err := s.Settings().Set(set.Key, set.Value); err == nil {
				setCount++
			}
		}
		c.JSON(http.StatusOK, gin.H{"providers_imported": provCount, "models_imported": modelCount, "settings_imported": setCount})
	})
}

func bearerAuth(token string) gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.GetHeader("Authorization")
		if !strings.HasPrefix(h, "Bearer ") || strings.TrimPrefix(h, "Bearer ") != token {
			c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"message": "unauthorized", "type": "auth_error"}})
			c.Abort()
			return
		}
		c.Next()
	}
}

// anyList renders the result of a list func.
func anyList[T any](c *gin.Context, f func() ([]T, error)) {
	items, err := f()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, items)
}

func ok(c *gin.Context, v any, err error) {
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, v)
}

func created(c *gin.Context, v any, err error) {
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, v)
}

func badRequest(c *gin.Context, err error) {
	c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": err.Error(), "type": "invalid_request"}})
}

func conflict(c *gin.Context, msg string) {
	c.JSON(http.StatusConflict, gin.H{"error": gin.H{"message": msg, "type": "NAME_CONFLICT"}})
}

// bumpEpoch increments the config_epoch setting so multi-replica deployments can
// detect config changes and reload. Ported from admin/settings.rs bump_config_epoch.
func bumpEpoch(s storage.Storage) {
	v, _ := s.Settings().Get("config_epoch")
	n, _ := strconv.ParseInt(v, 10, 64)
	_ = s.Settings().Set("config_epoch", strconv.FormatInt(n+1, 10))
}
