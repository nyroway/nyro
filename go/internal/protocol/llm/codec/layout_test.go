// This is an external test package (codec_test, not codec) on purpose: it
// blank-imports every leaf package, and those import codec, so testing from
// inside the package would be an import cycle.
package codec_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/nyroway/nyro/go/internal/protocol/llm/codec"

	_ "github.com/nyroway/nyro/go/internal/protocol/llm/codec/anthropic/messages"
	_ "github.com/nyroway/nyro/go/internal/protocol/llm/codec/gemini/generatecontent"
	_ "github.com/nyroway/nyro/go/internal/protocol/llm/codec/openai/chatcompletions"
	_ "github.com/nyroway/nyro/go/internal/protocol/llm/codec/openai/embeddings"
	_ "github.com/nyroway/nyro/go/internal/protocol/llm/codec/openai/responses"
)

const codecRoot = "github.com/nyroway/nyro/go/internal/protocol/llm/codec/"

// TestPackagePathMatchesProtocolID locks the naming rule documented on
// spec.Protocol: a protocol ID is {family}-{api} and its package path is the
// same string with "-" replaced by "/". Replacing every "-" only round-trips
// while the api segment contains none, so this also catches an ID like
// "openai-chat-completions" that would imply a three-level path.
func TestPackagePathMatchesProtocolID(t *testing.T) {
	t.Parallel()
	for _, h := range codec.All() {
		id := string(h.Endpoint().Protocol)
		want := codecRoot + strings.ReplaceAll(id, "-", "/")
		got := reflect.TypeOf(h).PkgPath()
		if got != want {
			t.Errorf("protocol %q is implemented in %q, want %q", id, got, want)
		}
		if n := strings.Count(id, "-"); n != 1 {
			t.Errorf("protocol %q has %d %q separators, want exactly 1", id, n, "-")
		}
	}
}

// TestRegisteredEndpoints pins the set of protocols this binary can speak, so
// adding or dropping one is a deliberate edit rather than a side effect of an
// import change.
func TestRegisteredEndpoints(t *testing.T) {
	t.Parallel()
	want := []string{
		"anthropic-messages/v1",
		"gemini-generatecontent/v1beta",
		"openai-chatcompletions/v1",
		"openai-embeddings/v1",
		"openai-responses/v1",
	}
	all := codec.All()
	got := make([]string, 0, len(all))
	for _, h := range all {
		got = append(got, h.Endpoint().String())
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("registered endpoints = %v, want %v (All() sorts by endpoint string)", got, want)
	}
}

// TestIngressRoutesAreUniqueAndWellFormed guards the route table that
// proxy.NewRouter builds from these declarations: chi panics on a duplicate
// pattern, and a missing method or pattern would silently claim nothing.
func TestIngressRoutesAreUniqueAndWellFormed(t *testing.T) {
	t.Parallel()
	seen := map[string]string{} // "METHOD pattern" -> protocol that claimed it
	for _, h := range codec.All() {
		id := string(h.Endpoint().Protocol)
		routes := h.Capabilities().IngressRoutes
		if len(routes) == 0 {
			t.Errorf("protocol %q declares no ingress routes, so it is unreachable", id)
		}
		for _, rt := range routes {
			if rt.Method == "" || rt.Pattern == "" {
				t.Errorf("protocol %q declares an incomplete route %+v", id, rt)
				continue
			}
			if !strings.HasPrefix(rt.Pattern, "/") {
				t.Errorf("protocol %q route pattern %q must be absolute", id, rt.Pattern)
			}
			key := rt.Method + " " + rt.Pattern
			if prev, dup := seen[key]; dup {
				t.Errorf("route %q claimed by both %q and %q", key, prev, id)
			}
			seen[key] = id
		}
	}
}

// TestEndpointForResolvesEveryProtocol covers the registry lookup that replaced
// the hand-written protocol→endpoint switch.
func TestEndpointForResolvesEveryProtocol(t *testing.T) {
	t.Parallel()
	for _, h := range codec.All() {
		p := h.Endpoint().Protocol
		ep, ok := codec.EndpointFor(p)
		if !ok {
			t.Errorf("EndpointFor(%q) = _, false; want the registered endpoint", p)
			continue
		}
		if ep != h.Endpoint() {
			t.Errorf("EndpointFor(%q) = %v, want %v", p, ep, h.Endpoint())
		}
	}
	if _, ok := codec.EndpointFor("not-aprotocol"); ok {
		t.Error(`EndpointFor("not-aprotocol") = _, true; want false`)
	}
}
