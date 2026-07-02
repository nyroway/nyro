package provider_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/nyroway/nyro/go/internal/provider"
)

// allBuiltinIDs is the full set of built-in provider IDs; tests iterate it to
// guarantee every vendor is registered with a provider-specific concrete type.
var allBuiltinIDs = []string{
	"openai", "anthropic", "gemini", "deepseek",
	"moonshotai", "zhipuai", "xai", "openrouter",
	"nvidia", "minimax", "zai", "ollama",
	"aws-bedrock", "gcp-vertex", "azure-foundry", "custom",
}

func TestProvidersAreConcreteImplementations(t *testing.T) {
	for _, id := range allBuiltinIDs {
		p, ok := provider.Get(id)
		if !ok {
			t.Fatalf("%s provider not found", id)
		}
		if got := reflect.TypeOf(p).Name(); got == "DefaultProvider" {
			t.Fatalf("%s registered as DefaultProvider; want provider-specific concrete type", id)
		}
	}
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

func TestHealthCheckModelFallsBackToFirstDiscoveredStaticModel(t *testing.T) {
	def := provider.Definition{Models: provider.ModelDiscovery{Values: []string{"first-model", "second-model"}}}
	if got := provider.HealthCheckModel(def); got != "first-model" {
		t.Fatalf("HealthCheckModel() = %q, want first-model", got)
	}
	def.DefaultModel = "explicit-model"
	if got := provider.HealthCheckModel(def); got != "explicit-model" {
		t.Fatalf("HealthCheckModel() with DefaultModel = %q, want explicit-model", got)
	}
}

func TestGetNormalizesAliases(t *testing.T) {
	cases := map[string]string{
		"zhipu": "zhipuai", "glm": "zhipuai", "GLM": "zhipuai",
		"z.ai": "zai", "grok": "xai", " XAI ": "xai",
	}
	for alias, want := range cases {
		p, ok := provider.Get(alias)
		if !ok {
			t.Fatalf("Get(%q) not found", alias)
		}
		if got := p.Definition().ID; got != want {
			t.Errorf("Get(%q).Definition().ID = %q, want %q", alias, got, want)
		}
	}
}

func TestLookupNormalizesAliases(t *testing.T) {
	def, ok := provider.Lookup("zhipu")
	if !ok || def.ID != "zhipuai" {
		t.Fatalf("Lookup(zhipu) = %+v, %v; want zhipuai", def, ok)
	}
}

func TestResolveFallsBackToCustom(t *testing.T) {
	p := provider.Resolve("this-id-does-not-exist")
	if got := p.Definition().ID; got != "custom" {
		t.Errorf("Resolve(unknown) = %q, want custom", got)
	}
	if p := provider.Resolve(""); p.Definition().ID != "custom" {
		t.Errorf("Resolve(\"\") = %q, want custom", p.Definition().ID)
	}
}

func TestResolveHitsRealProviderBeforeFallback(t *testing.T) {
	p := provider.Resolve("anthropic")
	if got := p.Definition().ID; got != "anthropic" {
		t.Errorf("Resolve(anthropic) = %q, want anthropic (not custom fallback)", got)
	}
	// Alias normalization applies inside Resolve too (via Get).
	if got := provider.Resolve("zhipu").Definition().ID; got != "zhipuai" {
		t.Errorf("Resolve(zhipu) = %q, want zhipuai", got)
	}
}

func TestDuplicateRegistrationPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("Register of duplicate ID did not panic")
		}
	}()
	provider.Register(dupProvider{})
}

// dupProvider collides with the built-in "openai" ID to assert Register panics
// on duplicates (mirroring database/sql.Register).
type dupProvider struct{}

func (dupProvider) Definition() provider.Definition { return provider.Definition{ID: "openai"} }
func (dupProvider) NewAuthenticator(context.Context, provider.UpstreamRuntime) (provider.Authenticator, error) {
	return provider.NoopAuthenticator{}, nil
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
