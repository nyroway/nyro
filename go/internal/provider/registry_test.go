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
