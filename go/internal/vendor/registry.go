package vendor

import (
	"strings"
	"sync"

	"github.com/nyroway/nyro/go/internal/protocol/ids"
)

// Registry maps vendor keys to Vendor implementations. Resolution is
// three-tier: vendorID → protocol default → nil.
type Registry struct {
	mu              sync.RWMutex
	vendors         map[string]Vendor
	protocolDefault map[ids.Protocol]string
}

// NewRegistry builds an empty registry.
func NewRegistry() *Registry {
	return &Registry{
		vendors:         map[string]Vendor{},
		protocolDefault: map[ids.Protocol]string{},
	}
}

// Register adds a vendor under its ID and optionally sets it as the protocol
// default.
func (r *Registry) Register(v Vendor, protocols ...ids.Protocol) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.vendors[v.ID()] = v
	for _, p := range protocols {
		r.protocolDefault[p] = v.ID()
	}
}

// Resolve finds a vendor by ID, falling back to the protocol default, then nil.
func (r *Registry) Resolve(vendorID string, protocol string) Vendor {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if vendorID != "" {
		key := normalizeVendorID(vendorID)
		if v, ok := r.vendors[key]; ok {
			return v
		}
	}
	if p, err := ids.ParseProtocol(protocol); err == nil {
		if defaultID, ok := r.protocolDefault[p]; ok {
			return r.vendors[defaultID]
		}
	}
	return nil
}

// normalizeVendorID maps common aliases (zhipu↔zhipuai, z.ai↔zai, etc.).
func normalizeVendorID(id string) string {
	id = strings.TrimSpace(strings.ToLower(id))
	switch id {
	case "zhipu", "glm":
		return "zhipuai"
	case "z.ai":
		return "zai"
	case "grok":
		return "xai"
	}
	return id
}

var global = NewRegistry()

// Global returns the process-wide registry. Vendors register via init().
func Global() *Registry { return global }
