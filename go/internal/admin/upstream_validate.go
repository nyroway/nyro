package admin

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/nyroway/nyro/go/internal/provider"
	"github.com/nyroway/nyro/go/internal/storage"
)

// normalizeEmptyModelsJSON collapses a models_json update payload that
// unmarshals to an empty array (e.g. the WebUI sending `"models":[]` to
// clear a static list when switching an upstream to URL discovery) down to
// nil, matching "unset" rather than storing the literal string "[]" — which
// would otherwise read back as len > 0 and be treated as "static list still
// present" everywhere that checks for one (validateUpstreamFields,
// modelsForUpstream's manual-vs-discovery branch).
func normalizeEmptyModelsJSON(in *storage.UpdateUpstream) {
	if in.ModelsJSON == nil {
		return
	}
	var models []string
	if json.Unmarshal(*in.ModelsJSON, &models) == nil && len(models) == 0 {
		var empty json.RawMessage
		in.ModelsJSON = &empty
	}
}

// validateModelsMutualExclusion enforces the one invariant that must hold
// for every upstream regardless of how it was created: models_json and
// models_url can't both be set (matching the YAML config-loading path's
// `models` xor `models_url` rule — see go/docs/schema/config.md). This is
// checked on every create/update against the resulting merged state, since
// an update that sets one field without clearing the other would otherwise
// silently leave both set (the manual list would keep shadowing the new
// discovery URL — see modelsForUpstream).
func validateModelsMutualExclusion(modelsJSON json.RawMessage, modelsURL string) error {
	if len(modelsJSON) > 0 && modelsURL != "" {
		return errors.New("models and models_url are mutually exclusive")
	}
	return nil
}

// validateNewUpstreamFields additionally enforces the invariants that only
// make sense to require up front, at creation time: provider is required
// and must resolve to a registered preset or "custom", and "custom" (no
// preset to fall back on for base_url) requires an explicit base_url.
// Unlike the YAML config-loading path, the admin API does not require a
// freshly created "custom" upstream to already have a model source
// (models/models_url) — the control plane allows filling that in later via
// a follow-up update, unlike a one-shot declarative config load.
func validateNewUpstreamFields(providerID, baseURL string, modelsJSON json.RawMessage, modelsURL string) error {
	if strings.TrimSpace(providerID) == "" {
		return errors.New("provider is required")
	}
	if err := validateModelsMutualExclusion(modelsJSON, modelsURL); err != nil {
		return err
	}
	if providerID == "custom" {
		if baseURL == "" {
			return errors.New(`base_url is required for provider "custom"`)
		}
		return nil
	}
	if _, ok := provider.Lookup(providerID); !ok {
		return fmt.Errorf("unknown provider %q", providerID)
	}
	return nil
}
