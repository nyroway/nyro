package admin

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/nyroway/nyro/go/internal/auth"
	"github.com/nyroway/nyro/go/internal/storage"
	"github.com/nyroway/nyro/go/internal/web"
)

// MountOAuth registers the OAuth session lifecycle routes under /api/v1/auth.
// Requires a pre-built auth.Registry + auth.SessionStore.
func MountOAuth(r chi.Router, s storage.Storage, reg *auth.Registry, sessions *auth.SessionStore) {
	r.Route("/api/v1/auth", func(ag chi.Router) {
		// POST /sessions — init an OAuth flow (body: {driver, provider_id?})
		ag.Post("/sessions", func(w http.ResponseWriter, r *http.Request) {
			var body struct {
				Driver     string `json:"driver"`
				ProviderID string `json:"provider_id"`
			}
			if err := web.Decode(r, &body); err != nil {
				badRequest(w, err)
				return
			}
			driver, ok := reg.Get(body.Driver)
			if !ok {
				web.Error(w, http.StatusBadRequest, "unknown driver: "+body.Driver, "invalid_request")
				return
			}

			start, err := driver.Start(r.Context(), body.ProviderID)
			if err != nil {
				web.Error(w, http.StatusBadGateway, err.Error(), "auth_error")
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

			web.JSON(w, http.StatusOK, map[string]any{
				"session_id":                sessID,
				"auth_url":                  start.AuthURL,
				"user_code":                 start.UserCode,
				"device_code":               start.DeviceCode,
				"verification_uri_complete": start.AuthURL,
				"expires_in":                600,
			})
		})

		// GET /sessions/:id — poll session status (device-code flows return credential when ready)
		ag.Get("/sessions/{id}", func(w http.ResponseWriter, r *http.Request) {
			sess, ok := sessions.Get(chi.URLParam(r, "id"))
			if !ok {
				web.JSON(w, http.StatusNotFound, map[string]any{"error": "session not found"})
				return
			}

			// For device-code drivers, poll the upstream via the optional capability.
			if sess.Status == auth.StatusPending && sess.StartResult.DeviceCode != "" {
				if driver, dok := reg.Get(sess.DriverKey); dok {
					if dp, ok := driver.(auth.DevicePoller); ok {
						result, err := dp.PollWithDeviceCode(r.Context(), sess.StartResult.DeviceCode)
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

			sess, _ = sessions.Get(chi.URLParam(r, "id"))
			resp := map[string]any{
				"session_id": sess.ID,
				"status":     string(sess.Status),
			}
			if sess.Status == auth.StatusComplete && sess.Credential != nil {
				resp["connected"] = true
			}
			if sess.Error != "" {
				resp["error"] = sess.Error
			}
			web.JSON(w, http.StatusOK, resp)
		})

		// POST /sessions/:id/complete — user provides auth code (PKCE flows)
		ag.Post("/sessions/{id}/complete", func(w http.ResponseWriter, r *http.Request) {
			var body struct {
				Code string `json:"code"`
			}
			if err := web.Decode(r, &body); err != nil {
				badRequest(w, err)
				return
			}

			sess, ok := sessions.Get(chi.URLParam(r, "id"))
			if !ok {
				web.JSON(w, http.StatusNotFound, map[string]any{"error": "session not found"})
				return
			}
			if sess.Status != auth.StatusPending {
				web.JSON(w, http.StatusConflict, map[string]any{"error": "session not pending"})
				return
			}

			driver, dok := reg.Get(sess.DriverKey)
			if !dok {
				web.JSON(w, http.StatusInternalServerError, map[string]any{"error": "driver not found"})
				return
			}

			cred, err := driver.Exchange(r.Context(), sess.ProviderID, body.Code, sess.StartResult.Verifier, sess.StartResult.State)
			if err != nil {
				sessions.Update(sess.ID, func(s *auth.AuthSession) {
					s.Status = auth.StatusError
					s.Error = err.Error()
				})
				web.Error(w, http.StatusBadGateway, err.Error(), "auth_error")
				return
			}

			sessions.Update(sess.ID, func(s *auth.AuthSession) {
				s.Status = auth.StatusComplete
				s.Credential = &cred
			})
			web.JSON(w, http.StatusOK, map[string]any{"session_id": sess.ID, "status": "complete"})
		})

		// DELETE /sessions/:id — cancel session
		ag.Delete("/sessions/{id}", func(w http.ResponseWriter, r *http.Request) {
			sessions.Delete(chi.URLParam(r, "id"))
			w.WriteHeader(http.StatusNoContent)
		})
	})

	// POST /providers/:id/oauth/connect — bind a ready session to a provider.
	// (Registered on the top-level router, mirroring the gin layout where these
	// lived on the /api/v1 group, not under /api/v1/auth.)
	r.Post("/api/v1/providers/{id}/oauth/connect", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			SessionID string `json:"session_id"`
			Name      string `json:"name"`
			Vendor    string `json:"vendor"`
		}
		if err := web.Decode(r, &body); err != nil {
			badRequest(w, err)
			return
		}

		sess, ok := sessions.Get(body.SessionID)
		if !ok || sess.Status != auth.StatusComplete || sess.Credential == nil {
			web.Error(w, http.StatusBadRequest, "session not ready", "auth_error")
			return
		}

		// Create or update the provider with auth_mode=oauth.
		providerID := chi.URLParam(r, "id")
		p, err := s.Providers().Get(providerID)
		if err != nil || p == nil {
			web.JSON(w, http.StatusNotFound, map[string]any{"error": "provider not found"})
			return
		}
		p.AuthMode = "oauth"
		enabled := true
		_, err = s.Providers().Update(providerID, storage.UpdateProvider{
			AuthMode:  &p.AuthMode,
			IsEnabled: &enabled,
		})
		if err != nil {
			web.JSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}

		// Store the credential.
		cred := *sess.Credential
		_, err = s.OAuthCredentials().Upsert(providerID, storage.UpsertOAuthCredential{
			DriverKey:    cred.DriverKey,
			Scheme:       cred.Scheme,
			AccessToken:  cred.AccessToken,
			RefreshToken: cred.RefreshToken,
			ExpiresAt:    cred.ExpiresAt,
		})
		if err != nil {
			web.JSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}

		bumpEpoch(s)
		sessions.Delete(body.SessionID)
		web.JSON(w, http.StatusOK, map[string]any{"connected": true, "provider_id": providerID})
	})

	// POST /providers/:id/oauth/reconnect — refresh credential for existing provider.
	r.Post("/api/v1/providers/{id}/oauth/reconnect", func(w http.ResponseWriter, r *http.Request) {
		providerID := chi.URLParam(r, "id")
		cred, _ := s.OAuthCredentials().Get(providerID)
		if cred == nil {
			web.JSON(w, http.StatusNotFound, map[string]any{"error": "no OAuth credential for provider"})
			return
		}
		driver, ok := reg.Get(cred.DriverKey)
		if !ok {
			web.JSON(w, http.StatusBadRequest, map[string]any{"error": "driver not found: " + cred.DriverKey})
			return
		}

		refreshed, err := driver.Refresh(r.Context(), *cred)
		if err != nil {
			_ = s.OAuthCredentials().FailRefresh(providerID, err.Error())
			web.Error(w, http.StatusBadGateway, err.Error(), "auth_error")
			return
		}

		_, _ = s.OAuthCredentials().Upsert(providerID, storage.UpsertOAuthCredential{
			DriverKey:    refreshed.DriverKey,
			Scheme:       refreshed.Scheme,
			AccessToken:  refreshed.AccessToken,
			RefreshToken: refreshed.RefreshToken,
			ExpiresAt:    refreshed.ExpiresAt,
		})
		web.JSON(w, http.StatusOK, map[string]any{"connected": true})
	})
}
