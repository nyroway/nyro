package provider

import (
	_ "embed"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"time"
)

//go:embed models.dev.json
var modelsDevSnapshot []byte

// ModelCapability is the per-model info surfaced to the WebUI. Ported (scoped)
// from db/models.rs ModelCapabilities.
type ModelCapability struct {
	Provider         string   `json:"provider"`
	ModelID          string   `json:"model_id"`
	ContextWindow    uint64   `json:"context_window"`
	InputCost        *float64 `json:"input_cost,omitempty"`
	OutputCost       *float64 `json:"output_cost,omitempty"`
	InputModalities  []string `json:"input_modalities"`
	OutputModalities []string `json:"output_modalities"`
	ToolCall         bool     `json:"tool_call"`
	Reasoning        bool     `json:"reasoning"`
}

// rawModelDevEntry is kept for reference; the actual parsing inlines the
// struct in loadFromSnapshot to avoid unused-type warnings.

// CatalogCache holds the parsed models.dev data with periodic refresh.
type CatalogCache struct {
	mu        sync.RWMutex
	data      map[string][]ModelCapability
	loaded    time.Time
	ttl       time.Duration
	sourceURL string
}

// NewCatalogCache creates a cache with a 24h TTL.
func NewCatalogCache() *CatalogCache {
	c := &CatalogCache{
		ttl:       24 * time.Hour,
		sourceURL: "https://models.dev/models.json",
	}
	c.loadFromSnapshot()
	return c
}

func (c *CatalogCache) loadFromSnapshot() {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(modelsDevSnapshot, &raw); err != nil {
		return
	}
	data := map[string][]ModelCapability{}
	for vendor, rawEntry := range raw {
		var entry struct {
			Models []struct {
				ID            string `json:"id"`
				Name          string `json:"name"`
				ContextWindow int    `json:"limit"`
				Pricing       struct {
					Input  float64 `json:"input"`
					Output float64 `json:"output"`
				} `json:"pricing"`
				Architecture struct {
					InputModalities  []string `json:"input_modalities"`
					OutputModalities []string `json:"output_modalities"`
				} `json:"architecture"`
				Params struct {
					Tools bool `json:"tools"`
				} `json:"params"`
			} `json:"models"`
		}
		if json.Unmarshal(rawEntry, &entry) != nil {
			continue
		}
		var caps []ModelCapability
		for _, m := range entry.Models {
			cap := ModelCapability{
				Provider:         vendor,
				ModelID:          m.ID,
				ContextWindow:    uint64(m.ContextWindow),
				InputModalities:  m.Architecture.InputModalities,
				OutputModalities: m.Architecture.OutputModalities,
				ToolCall:         m.Params.Tools,
			}
			if m.Pricing.Input > 0 {
				cap.InputCost = &m.Pricing.Input
			}
			if m.Pricing.Output > 0 {
				cap.OutputCost = &m.Pricing.Output
			}
			if cap.ContextWindow == 0 {
				cap.ContextWindow = uint64(m.ContextWindow)
			}
			cap.Reasoning = hasReasoning(m.Architecture.OutputModalities)
			caps = append(caps, cap)
		}
		data[vendor] = caps
	}
	c.mu.Lock()
	c.data = data
	c.loaded = time.Now()
	c.mu.Unlock()
}

func hasReasoning(modalities []string) bool {
	for _, m := range modalities {
		if m == "reasoning" || m == "thinking" {
			return true
		}
	}
	return false
}

// Get returns capabilities for a vendor. Returns nil if not found.
func (c *CatalogCache) Get(vendor string) []ModelCapability {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.data[vendor]
}

// LookupModel finds a model by vendor + model ID.
func (c *CatalogCache) LookupModel(vendor, modelID string) *ModelCapability {
	for _, cap := range c.Get(vendor) {
		if cap.ModelID == modelID {
			return &cap
		}
	}
	return nil
}

// RefreshIfNeeded reloads from the source URL if the cache is stale.
func (c *CatalogCache) RefreshIfNeeded() {
	c.mu.RLock()
	if time.Since(c.loaded) < c.ttl {
		c.mu.RUnlock()
		return
	}
	c.mu.RUnlock()
	c.refresh()
}

func (c *CatalogCache) refresh() {
	resp, err := http.Get(c.sourceURL)
	if err != nil || resp.StatusCode != 200 {
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var raw map[string]json.RawMessage
	if json.Unmarshal(body, &raw) != nil {
		return
	}
	// Reuse loadFromSnapshot logic but with the new data.
	// For simplicity, replace the embedded snapshot.
	modelsDevSnapshot = body
	c.loadFromSnapshot()
}

// List returns all known vendors + their model count.
func (c *CatalogCache) List() map[string]int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make(map[string]int, len(c.data))
	for v, caps := range c.data {
		out[v] = len(caps)
	}
	return out
}

// DefaultCatalog is the process-wide catalog cache.
var DefaultCatalog = NewCatalogCache()
