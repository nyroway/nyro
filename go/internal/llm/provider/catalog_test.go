package provider

import (
	"context"
	"reflect"
	"testing"

	"github.com/nyroway/nyro/go/internal/llm"
	"github.com/nyroway/nyro/go/internal/llm/protocol"
)

type fakeDriver struct{ name string }

func (fakeDriver) ExtendRequest(context.Context, UpstreamRuntime, llm.ModelRequest) error {
	return nil
}

func (d fakeDriver) Prepare(_ context.Context, _ UpstreamRuntime, wire protocol.WireRequest) (Request, error) {
	return Request{Method: wire.Method, URL: wire.Path, Headers: wire.Headers, Body: wire.Body, Stream: wire.Stream}, nil
}

func (d fakeDriver) Classify(response Response) Classification {
	return Classification{Failed: response.StatusCode >= 400}
}

func (fakeDriver) ExtendResponse(context.Context, UpstreamRuntime, *llm.ChatResponse) error {
	return nil
}

func (fakeDriver) ExtendError(context.Context, UpstreamRuntime, *llm.Error) (ErrorClassification, error) {
	return ErrorClassification{}, nil
}

func fakeRegistration(id string, priority int, name string) Registration {
	return Registration{
		Definition: Definition{ID: id, Name: name, Priority: priority},
		Factory:    func() Driver { return fakeDriver{name: id} },
	}
}

func fakeFallback() Registration {
	registration := fakeRegistration("generic", 0, "Generic")
	registration.Fallback = true
	return registration
}

func TestStandardDriverLeavesStatusRetryPolicyToRuntimeSettings(t *testing.T) {
	driver := Generic().Factory()
	for _, status := range []int{429, 500, 503} {
		classification := driver.Classify(Response{StatusCode: status})
		if !classification.Failed {
			t.Errorf("status %d: Failed = false, want true", status)
		}
		if classification.Retryable {
			t.Errorf("status %d: Retryable = true, want status-only retry policy left to retry_on_status", status)
		}
	}
}

func TestNewCatalogRejectsEmptyAndDuplicateIDs(t *testing.T) {
	t.Parallel()
	if _, err := NewCatalog(fakeFallback(), fakeRegistration("", 1, "Empty")); err == nil {
		t.Fatal("NewCatalog() accepted an empty provider ID")
	}
	if _, err := NewCatalog(
		fakeFallback(),
		fakeRegistration("openai", 1, "First"),
		fakeRegistration(" OpenAI ", 2, "Duplicate"),
	); err == nil {
		t.Fatal("NewCatalog() accepted normalized duplicate provider IDs")
	}
	if _, err := NewCatalog(fakeFallback(), fakeRegistration("generic", 1, "Duplicate fallback ID")); err == nil {
		t.Fatal("NewCatalog() accepted a registration duplicating the fallback ID")
	}
}

func TestCatalogLookupNormalizesIDWithoutMutation(t *testing.T) {
	t.Parallel()
	registration := fakeRegistration("openai", 2, "OpenAI")
	registration.Definition.Protocols = []Protocol{{ID: "chat", BaseURL: "https://original.example"}}
	registration.Definition.Credentials.Fields = []CredentialField{{
		Name:         "api_key",
		Values:       []string{"one"},
		RequiredWhen: map[string]any{"mode": "api_key"},
	}}
	registration.Definition.Extra = map[string]any{"version": "original"}
	catalog, err := NewCatalog(fakeFallback(), registration)
	if err != nil {
		t.Fatalf("NewCatalog(): %v", err)
	}

	registration.Definition.Protocols[0].BaseURL = "https://changed.example"
	registration.Definition.Credentials.Fields[0].Values[0] = "changed"
	registration.Definition.Credentials.Fields[0].RequiredWhen["mode"] = "changed"
	registration.Definition.Extra["version"] = "changed"

	definition, ok := catalog.Lookup(" OpenAI ")
	if !ok {
		t.Fatal("Lookup() did not normalize the provider ID")
	}
	if got := definition.Protocols[0].BaseURL; got != "https://original.example" {
		t.Fatalf("Lookup().Protocols[0].BaseURL = %q", got)
	}
	if got := definition.Credentials.Fields[0].Values[0]; got != "one" {
		t.Fatalf("Lookup().Credentials.Fields[0].Values[0] = %q", got)
	}
	if got := definition.Credentials.Fields[0].RequiredWhen["mode"]; got != "api_key" {
		t.Fatalf("Lookup().RequiredWhen[mode] = %v", got)
	}
	if got := definition.Extra["version"]; got != "original" {
		t.Fatalf("Lookup().Extra[version] = %v", got)
	}

	definition.Protocols[0].BaseURL = "https://caller-mutated.example"
	again, _ := catalog.Lookup("openai")
	if got := again.Protocols[0].BaseURL; got != "https://original.example" {
		t.Fatalf("Lookup() exposed catalog state: %q", got)
	}
}

func TestCatalogDefinitionsUsePriorityThenIDOrder(t *testing.T) {
	t.Parallel()
	catalog, err := NewCatalog(
		fakeRegistration("zeta", 2, "Zeta"),
		fakeFallback(),
		fakeRegistration("beta", 1, "Beta"),
		fakeRegistration("alpha", 1, "Alpha"),
	)
	if err != nil {
		t.Fatalf("NewCatalog(): %v", err)
	}
	definitions := catalog.Definitions()
	got := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		got = append(got, definition.ID)
	}
	if want := []string{"alpha", "beta", "zeta"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Definitions() IDs = %v, want %v", got, want)
	}
	definitions[0].Name = "mutated"
	again := catalog.Definitions()
	if again[0].Name != "Alpha" {
		t.Fatalf("Definitions() exposed catalog state: %q", again[0].Name)
	}
}

func TestCatalogResolvesUnknownProviderToGenericDriver(t *testing.T) {
	t.Parallel()
	catalog, err := NewCatalog(fakeFallback(), fakeRegistration("known", 1, "Known"))
	if err != nil {
		t.Fatalf("NewCatalog(): %v", err)
	}
	if got := catalog.DriverFor("known")().(fakeDriver).name; got != "known" {
		t.Fatalf("DriverFor(known) = %q", got)
	}
	for _, id := range []string{"", "custom", "unknown"} {
		if got := catalog.DriverFor(id)().(fakeDriver).name; got != "generic" {
			t.Errorf("DriverFor(%q) = %q, want generic", id, got)
		}
	}
}

func TestBuiltinsContainExpectedProviderIDs(t *testing.T) {
	t.Parallel()
	catalog, err := NewCatalog(Generic(), OpenAI(), Anthropic(), Gemini(), DeepSeek(), OpenRouter())
	if err != nil {
		t.Fatalf("NewCatalog(): %v", err)
	}
	definitions := catalog.Definitions()
	got := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		got = append(got, definition.ID)
	}
	want := []string{"anthropic", "openai", "gemini", "deepseek", "openrouter"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Definitions() IDs = %v, want %v", got, want)
	}
	if _, ok := catalog.Lookup("generic"); ok {
		t.Fatal("generic fallback is exposed as a provider preset")
	}
}
