package admin

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/nyroway/nyro/go/internal/auth"
	"github.com/nyroway/nyro/go/internal/storage"
)

// MountOAuth registers the OAuth session lifecycle routes under /api/v1/auth.
// Requires a pre-built auth.Registry + auth.SessionStore.
func MountOAuth(g gin.IRouter, s storage.Storage, reg *auth.Registry, sessions *auth.SessionStore) {
	auth_g := g.Group("/api/v1/auth")

	// POST /sessions — init an OAuth flow (body: {driver, provider_id?})
	auth_g.POST("/sessions", func(c *gin.Context) {
		var body struct {
			Driver     string `json:"driver"`
			ProviderID string `json:"provider_id"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			badRequest(c, err)
			return
		}
		driver, ok := reg.Get(body.Driver)
		if !ok {
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": "unknown driver: " + body.Driver, "type": "invalid_request"}})
			return
		}

		start, err := driver.Start(c.Request.Context(), body.ProviderID)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"message": err.Error(), "type": "auth_error"}})
			return
		}

		sessID := auth.GenerateState()
		sessions.Create(&auth.AuthSession{
			ID:          sessID,
			DriverKey:   driver.Name(),
			ProviderID:  body.ProviderID,
			StartResult: start,
			Status:      auth.StatusPending,
		})

		c.JSON(http.StatusOK, gin.H{
			"session_id":                sessID,
			"auth_url":                  start.AuthURL,
			"user_code":                 start.UserCode,
			"device_code":               start.DeviceCode,
			"verification_uri_complete": start.AuthURL,
			"expires_in":                600,
		})
	})

	// GET /sessions/:id — poll session status (device-code flows return credential when ready)
	auth_g.GET("/sessions/:id", func(c *gin.Context) {
		sess, ok := sessions.Get(c.Param("id"))
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "session not found"})
			return
		}

		// For device-code drivers, poll the upstream via the optional capability.
		if sess.Status == auth.StatusPending && sess.StartResult.DeviceCode != "" {
			if driver, dok := reg.Get(sess.DriverKey); dok {
				if dp, ok := driver.(auth.DevicePoller); ok {
					result, err := dp.PollWithDeviceCode(c.Request.Context(), sess.StartResult.DeviceCode)
					switch {
					case err == nil && result.Status == auth.StatusComplete && result.Credential != nil:
						sessions.Update(sess.ID, func(s *auth.AuthSession) {
							s.Status = auth.StatusComplete
							s.Credential = result.Credential
						})
					case err != nil || result.Status == auth.StatusError:
						sessions.Update(sess.ID, func(s *auth.AuthSession) {
							s.Status = auth.StatusError
							if err != nil {
								s.Error = err.Error()
							} else {
								s.Error = result.Error
							}
						})
					}
				}
			}
		}

		sess, _ = sessions.Get(c.Param("id"))
		resp := gin.H{
			"session_id": sess.ID,
			"status":     string(sess.Status),
		}
		if sess.Status == auth.StatusComplete && sess.Credential != nil {
			resp["connected"] = true
		}
		if sess.Error != "" {
			resp["error"] = sess.Error
		}
		c.JSON(http.StatusOK, resp)
	})

	// POST /sessions/:id/complete — user provides auth code (PKCE flows)
	auth_g.POST("/sessions/:id/complete", func(c *gin.Context) {
		var body struct {
			Code string `json:"code"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			badRequest(c, err)
			return
		}

		sess, ok := sessions.Get(c.Param("id"))
		if !ok {
			c.JSON(http.StatusNotFound, gin.H{"error": "session not found"})
			return
		}
		if sess.Status != auth.StatusPending {
			c.JSON(http.StatusConflict, gin.H{"error": "session not pending"})
			return
		}

		driver, dok := reg.Get(sess.DriverKey)
		if !dok {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "driver not found"})
			return
		}

		cred, err := driver.Exchange(c.Request.Context(), sess.ProviderID, body.Code, sess.StartResult.Verifier, sess.StartResult.State)
		if err != nil {
			sessions.Update(sess.ID, func(s *auth.AuthSession) {
				s.Status = auth.StatusError
				s.Error = err.Error()
			})
			c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"message": err.Error(), "type": "auth_error"}})
			return
		}

		sessions.Update(sess.ID, func(s *auth.AuthSession) {
			s.Status = auth.StatusComplete
			s.Credential = &cred
		})
		c.JSON(http.StatusOK, gin.H{"session_id": sess.ID, "status": "complete"})
	})

	// DELETE /sessions/:id — cancel session
	auth_g.DELETE("/sessions/:id", func(c *gin.Context) {
		sessions.Delete(c.Param("id"))
		c.Status(http.StatusNoContent)
	})

	// POST /providers/:id/oauth/connect — bind a ready session to a provider
	g.POST("/api/v1/providers/:id/oauth/connect", func(c *gin.Context) {
		var body struct {
			SessionID string `json:"session_id"`
			Name      string `json:"name"`
			Vendor    string `json:"vendor"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			badRequest(c, err)
			return
		}

		sess, ok := sessions.Get(body.SessionID)
		if !ok || sess.Status != auth.StatusComplete || sess.Credential == nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": "session not ready", "type": "auth_error"}})
			return
		}

		// Create or update the provider with auth_mode=oauth.
		providerID := c.Param("id")
		p, err := s.Providers().Get(providerID)
		if err != nil || p == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "provider not found"})
			return
		}
		p.AuthMode = "oauth"
		enabled := true
		_, err = s.Providers().Update(providerID, storage.UpdateProvider{
			AuthMode:  &p.AuthMode,
			IsEnabled: &enabled,
		})
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		// Store the credential.
		cred := *sess.Credential
		cred.ProviderID = providerID
		_, err = s.OAuthCredentials().Upsert(providerID, storage.UpsertOAuthCredential{
			DriverKey:    cred.DriverKey,
			Scheme:       cred.Scheme,
			AccessToken:  cred.AccessToken,
			RefreshToken: cred.RefreshToken,
			ExpiresAt:    cred.ExpiresAt,
		})
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		bumpEpoch(s)
		sessions.Delete(body.SessionID)
		c.JSON(http.StatusOK, gin.H{"connected": true, "provider_id": providerID})
	})

	// POST /providers/:id/oauth/reconnect — refresh credential for existing provider
	g.POST("/api/v1/providers/:id/oauth/reconnect", func(c *gin.Context) {
		providerID := c.Param("id")
		cred, _ := s.OAuthCredentials().Get(providerID)
		if cred == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "no OAuth credential for provider"})
			return
		}
		driver, ok := reg.Get(cred.DriverKey)
		if !ok {
			c.JSON(http.StatusBadRequest, gin.H{"error": "driver not found: " + cred.DriverKey})
			return
		}

		refreshed, err := driver.Refresh(c.Request.Context(), *cred)
		if err != nil {
			_ = s.OAuthCredentials().FailRefresh(providerID, err.Error())
			c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"message": err.Error(), "type": "auth_error"}})
			return
		}

		_, _ = s.OAuthCredentials().Upsert(providerID, storage.UpsertOAuthCredential{
			DriverKey:    refreshed.DriverKey,
			Scheme:       refreshed.Scheme,
			AccessToken:  refreshed.AccessToken,
			RefreshToken: refreshed.RefreshToken,
			ExpiresAt:    refreshed.ExpiresAt,
		})
		c.JSON(http.StatusOK, gin.H{"connected": true})
	})
}
