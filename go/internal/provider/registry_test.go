package provider_test

import (
	"testing"

	"github.com/nyroway/nyro/go/internal/provider"
)

// allBuiltinIDs is the full set of built-in provider IDs; tests iterate it to
// guarantee every vendor is registered.
//
// Temporarily reduced to anthropic/openai/gemini/deepseek/openrouter; other
// vendor providers were removed and can be reinstated (with their own files)
// later. The "custom" fallback provider was removed in favor of
// scheme-keyed AuthenticatorFor (see authenticator.go).
var allBuiltinIDs = []string{
	"openai", "anthropic", "gemini", "deepseek", "openrouter",
}

func TestDefinitionsReturnsAllBuiltins(t *testing.T) {
	defs := provider.Definitions()
	if len(defs) != len(allBuiltinIDs) {
		t.Fatalf("Definitions() returned %d, want %d", len(defs), len(allBuiltinIDs))
	}
	seen := map[string]bool{}
	for _, d := range defs {
		seen[d.ID] = true
	}
	for _, id := range allBuiltinIDs {
		if !seen[id] {
			t.Errorf("Definitions() missing %q", id)
		}
	}
}

func TestHealthCheckModelReturnsDefaultModel(t *testing.T) {
	def := provider.Definition{DefaultModel: "explicit-model"}
	if got := provider.HealthCheckModel(def); got != "explicit-model" {
		t.Fatalf("HealthCheckModel() = %q, want explicit-model", got)
	}
	def.DefaultModel = ""
	if got := provider.HealthCheckModel(def); got != "" {
		t.Fatalf("HealthCheckModel() with empty DefaultModel = %q, want empty", got)
	}
}

func TestLookupNormalizesID(t *testing.T) {
	def, ok := provider.Lookup(" OpenAI ")
	if !ok || def.ID != "openai" {
		t.Fatalf("Lookup(\" OpenAI \") = %+v, %v; want openai", def, ok)
	}
}

func TestDuplicateRegistrationPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("Register of duplicate ID did not panic")
		}
	}()
	provider.Register(provider.Definition{ID: "openai"})
}

// hasCredentialField reports whether d declares a credential field named name.
func hasCredentialField(d provider.Definition, name string) bool {
	for _, f := range d.Credentials.Fields {
		if f.Name == name {
			return true
		}
	}
	return false
}
