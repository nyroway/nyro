package bootstrap

import (
	"reflect"
	"strings"
	"testing"

	"github.com/nyroway/nyro/go/internal/llm/protocol"
)

const protocolPackageRoot = "github.com/nyroway/nyro/go/internal/llm/protocol/"

func TestLLMProtocolCatalogContainsExplicitBuiltins(t *testing.T) {
	t.Parallel()
	catalog, err := NewLLMProtocolCatalog()
	if err != nil {
		t.Fatalf("NewLLMProtocolCatalog(): %v", err)
	}
	want := []string{
		"anthropic-messages/v1",
		"gemini-generatecontent/v1beta",
		"openai-chatcompletions/v1",
		"openai-embeddings/v1",
		"openai-responses/v1",
	}
	codecs := catalog.IngressEndpoints()
	got := make([]string, 0, len(codecs))
	for _, ingress := range codecs {
		endpoint := ingress.Endpoint()
		got = append(got, endpoint.String())
		if _, ok := catalog.Egress(endpoint); !ok {
			t.Errorf("endpoint %s has no egress codec", endpoint)
		}
		resolved, ok := catalog.EndpointFor(endpoint.Protocol)
		if !ok || resolved != endpoint {
			t.Errorf("EndpointFor(%q) = (%v, %v), want (%v, true)", endpoint.Protocol, resolved, ok, endpoint)
		}
		id := string(endpoint.Protocol)
		if count := strings.Count(id, "-"); count != 1 {
			t.Errorf("protocol %q has %d separators, want 1", id, count)
		}
		wantPackage := protocolPackageRoot + strings.ReplaceAll(id, "-", "/")
		if gotPackage := reflect.TypeOf(ingress).PkgPath(); gotPackage != wantPackage {
			t.Errorf("protocol %q package = %q, want %q", id, gotPackage, wantPackage)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ingress endpoints = %v, want %v", got, want)
	}
	if _, ok := catalog.EndpointFor(protocol.Protocol("not-a-protocol")); ok {
		t.Error("unknown protocol resolved to an endpoint")
	}
}

func TestLLMProtocolIngressRoutesAreUniqueAndWellFormed(t *testing.T) {
	t.Parallel()
	catalog, err := NewLLMProtocolCatalog()
	if err != nil {
		t.Fatalf("NewLLMProtocolCatalog(): %v", err)
	}
	seen := map[string]protocol.Protocol{}
	for _, ingress := range catalog.IngressEndpoints() {
		routes := ingress.Capabilities().IngressRoutes
		if len(routes) == 0 {
			t.Errorf("protocol %q has no ingress routes", ingress.Endpoint().Protocol)
		}
		for _, route := range routes {
			if route.Method == "" || route.Pattern == "" || !strings.HasPrefix(route.Pattern, "/") {
				t.Errorf("protocol %q has invalid route %+v", ingress.Endpoint().Protocol, route)
				continue
			}
			key := route.Method + " " + route.Pattern
			if previous, exists := seen[key]; exists {
				t.Errorf("route %q claimed by %q and %q", key, previous, ingress.Endpoint().Protocol)
			}
			seen[key] = ingress.Endpoint().Protocol
		}
	}
}
