package bootstrap

import (
	"reflect"
	"testing"
)

func TestNewLLMProviderCatalogComposesBuiltinsExplicitly(t *testing.T) {
	t.Parallel()
	catalog, err := NewLLMProviderCatalog()
	if err != nil {
		t.Fatalf("NewLLMProviderCatalog(): %v", err)
	}
	definitions := catalog.Definitions()
	got := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		got = append(got, definition.ID)
	}
	want := []string{"anthropic", "openai", "gemini", "deepseek", "openrouter"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("provider IDs = %v, want %v", got, want)
	}
	if catalog.DriverFor("custom") == nil {
		t.Fatal("custom provider did not resolve to the generic driver")
	}
}
