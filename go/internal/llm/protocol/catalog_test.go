package protocol

import (
	"reflect"
	"testing"

	"github.com/nyroway/nyro/go/internal/llm"
)

type fakeIngress struct{ endpoint Endpoint }

func (f fakeIngress) Endpoint() Endpoint       { return f.endpoint }
func (fakeIngress) Capabilities() Capabilities { return Capabilities{} }
func (fakeIngress) IngressCodec()              {}

type fakeEgress struct{ endpoint Endpoint }

func (f fakeEgress) Endpoint() Endpoint       { return f.endpoint }
func (fakeEgress) Capabilities() Capabilities { return Capabilities{} }
func (fakeEgress) EgressCodec()               {}

func testEndpoint(protocol Protocol, workload llm.Workload, version string) Endpoint {
	return Endpoint{Protocol: protocol, Workload: workload, Version: version}
}

func TestNewCatalogRejectsDuplicateIngressEndpoint(t *testing.T) {
	t.Parallel()
	ep := testEndpoint("openai-chatcompletions", llm.WorkloadChat, "v1")
	_, err := NewCatalog([]IngressCodec{fakeIngress{ep}, fakeIngress{ep}}, nil)
	if err == nil {
		t.Fatal("NewCatalog() error = nil")
	}
}

func TestNewCatalogRejectsDuplicateEgressEndpoint(t *testing.T) {
	t.Parallel()
	ep := testEndpoint("openai-chatcompletions", llm.WorkloadChat, "v1")
	_, err := NewCatalog(nil, []EgressCodec{fakeEgress{ep}, fakeEgress{ep}})
	if err == nil {
		t.Fatal("NewCatalog() error = nil")
	}
}

func TestNewCatalogRejectsEmptyEndpoint(t *testing.T) {
	t.Parallel()
	tests := []Endpoint{
		{Workload: llm.WorkloadChat, Version: "v1"},
		{Protocol: "openai-chatcompletions", Version: "v1"},
		{Protocol: "openai-chatcompletions", Workload: llm.WorkloadChat},
	}
	for _, ep := range tests {
		if _, err := NewCatalog([]IngressCodec{fakeIngress{ep}}, nil); err == nil {
			t.Errorf("NewCatalog(%+v) error = nil", ep)
		}
	}
}

func TestCatalogCopiesInputsAndReturnsDeterministicIngressOrder(t *testing.T) {
	t.Parallel()
	a := testEndpoint("openai-responses", llm.WorkloadChat, "v1")
	b := testEndpoint("anthropic-messages", llm.WorkloadChat, "v1")
	c := testEndpoint("openai-embeddings", llm.WorkloadEmbedding, "v1")
	ingress := []IngressCodec{fakeIngress{a}, fakeIngress{c}, fakeIngress{b}}
	catalog, err := NewCatalog(ingress, nil)
	if err != nil {
		t.Fatalf("NewCatalog(): %v", err)
	}
	ingress[0] = fakeIngress{testEndpoint("changed", llm.WorkloadChat, "v9")}

	gotCodecs := catalog.IngressEndpoints()
	got := make([]Endpoint, 0, len(gotCodecs))
	for _, codec := range gotCodecs {
		got = append(got, codec.Endpoint())
	}
	want := []Endpoint{b, c, a}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("IngressEndpoints() = %+v, want %+v", got, want)
	}
	gotCodecs[0] = fakeIngress{a}
	if gotAgain := catalog.IngressEndpoints()[0].Endpoint(); gotAgain != b {
		t.Fatalf("IngressEndpoints() exposed catalog slice: first = %+v, want %+v", gotAgain, b)
	}
}

func TestCatalogKeepsIngressAndEgressIndependent(t *testing.T) {
	t.Parallel()
	ep := testEndpoint("openai-chatcompletions", llm.WorkloadChat, "v1")
	ingress := fakeIngress{ep}
	egress := fakeEgress{ep}
	catalog, err := NewCatalog([]IngressCodec{ingress}, []EgressCodec{egress})
	if err != nil {
		t.Fatalf("NewCatalog(): %v", err)
	}
	if got, ok := catalog.Ingress(ep); !ok || got != ingress {
		t.Fatalf("Ingress() = (%v, %v)", got, ok)
	}
	if got, ok := catalog.Egress(ep); !ok || got != egress {
		t.Fatalf("Egress() = (%v, %v)", got, ok)
	}
}

func TestCatalogEndpointForReturnsLowestCompleteEndpoint(t *testing.T) {
	t.Parallel()
	low := testEndpoint("shared", llm.WorkloadChat, "v1")
	highWorkload := testEndpoint("shared", llm.WorkloadEmbedding, "v1")
	highVersion := testEndpoint("shared", llm.WorkloadChat, "v2")
	catalog, err := NewCatalog(nil, []EgressCodec{
		fakeEgress{highVersion},
		fakeEgress{highWorkload},
		fakeEgress{low},
	})
	if err != nil {
		t.Fatalf("NewCatalog(): %v", err)
	}
	if got, ok := catalog.EndpointFor("shared"); !ok || got != low {
		t.Fatalf("EndpointFor(shared) = (%+v, %v), want (%+v, true)", got, ok, low)
	}
	if _, ok := catalog.EndpointFor("missing"); ok {
		t.Fatal("EndpointFor(missing) found an endpoint")
	}
}
