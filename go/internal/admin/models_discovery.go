package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/nyroway/nyro/go/internal/provider"
	"github.com/nyroway/nyro/go/internal/storage"
)

// modelsCacheTTL bounds how long a live-discovered model list is reused
// before the next request re-fetches it. Short enough that adding a model
// upstream-side shows up quickly in the control plane, long enough that
// opening the upstream edit form repeatedly doesn't hammer the vendor API.
const modelsCacheTTL = 30 * time.Second

type modelsCacheEntry struct {
	models    []string
	expiresAt time.Time
}

// modelsCache is a short-TTL, in-memory cache of live-discovered model
// lists, keyed by upstream id. Discovery results are never persisted to
// storage (see docs/schema/config.md) — this cache is the only place they
// live, and only briefly.
type modelsCache struct {
	mu      sync.Mutex
	entries map[string]modelsCacheEntry
}

var discoveryCache = &modelsCache{entries: map[string]modelsCacheEntry{}}

func (c *modelsCache) get(id string) ([]string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[id]
	if !ok || time.Now().After(e.expiresAt) {
		return nil, false
	}
	return e.models, true
}

func (c *modelsCache) set(id string, models []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[id] = modelsCacheEntry{models: models, expiresAt: time.Now().Add(modelsCacheTTL)}
}

// invalidate drops any cached discovery result for id. Call this whenever an
// upstream's models_url/provider/credentials/base_url could have changed
// (i.e. on update), so a stale list isn't served after an edit.
func (c *modelsCache) invalidate(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, id)
}

// modelsDiscoveryURL returns the model-discovery endpoint to query for u:
// u's own ModelsURL if set, else its provider preset's default ModelsURL.
// Returns "" when neither is available (a "custom" upstream with no
// models_url and no static models, or an unknown/unregistered provider) —
// callers must treat that as "discovery not possible," not an error.
func modelsDiscoveryURL(u storage.Upstream) string {
	if u.ModelsURL != "" {
		return u.ModelsURL
	}
	if def, ok := provider.Lookup(u.Provider); ok {
		return def.ModelsURL
	}
	return ""
}

// modelsForUpstream returns u's model list: its manual models_json if set
// (no network call, no caching needed — it's already a local read), else a
// live discovery fetch against modelsDiscoveryURL(u), cached for
// modelsCacheTTL. Returns an empty slice (not an error) when discovery isn't
// possible (no URL resolved) so callers can render an empty dropdown rather
// than failing the whole request.
func modelsForUpstream(ctx context.Context, u storage.Upstream) ([]string, error) {
	if len(u.ModelsJSON) > 0 {
		var models []string
		if err := json.Unmarshal(u.ModelsJSON, &models); err != nil {
			return nil, err
		}
		return models, nil
	}
	discoveryURL := modelsDiscoveryURL(u)
	if discoveryURL == "" {
		return []string{}, nil
	}
	if cached, ok := discoveryCache.get(u.ID); ok {
		return cached, nil
	}
	models, err := fetchModels(ctx, u, discoveryURL)
	if err != nil {
		return nil, err
	}
	discoveryCache.set(u.ID, models)
	return models, nil
}

// fetchModels performs the live discovery HTTP call and parses the
// response, which every current provider preset's ModelsURL returns in the
// OpenAI-shaped {"data":[{"id":"..."}]} form (this holds for OpenAI,
// Anthropic's /v1/models, and Gemini's OpenAI-compatible models endpoint —
// all three of today's exposed presets' ModelsURL values use this shape).
func fetchModels(ctx context.Context, u storage.Upstream, discoveryURL string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, discoveryURL, nil)
	if err != nil {
		return nil, err
	}
	auth, err := provider.AuthenticatorFor(u.Provider, u.Protocol, provider.UpstreamRuntime{
		Name:            u.Name,
		Provider:        u.Provider,
		Protocol:        u.Protocol,
		BaseURL:         u.BaseURL,
		CredentialsJSON: u.CredentialsJSON,
		ProxyURL:        u.ProxyURL,
	})
	if err != nil {
		return nil, err
	}
	if err := auth.Apply(ctx, req); err != nil {
		return nil, err
	}
	client := testHTTPClient(u.ProxyURL, 10*time.Second)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("discovery request failed: %s", resp.Status)
	}
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	models := make([]string, 0, len(body.Data))
	for _, d := range body.Data {
		if d.ID != "" {
			models = append(models, d.ID)
		}
	}
	return models, nil
}
