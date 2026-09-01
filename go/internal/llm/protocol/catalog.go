package protocol

import (
	"fmt"
	"sort"
)

type Catalog struct {
	ingress      map[Endpoint]IngressCodec
	egress       map[Endpoint]EgressCodec
	ingressOrder []IngressCodec
	egressOrder  []EgressCodec
}

func NewCatalog(ingress []IngressCodec, egress []EgressCodec) (*Catalog, error) {
	catalog := &Catalog{
		ingress:      make(map[Endpoint]IngressCodec, len(ingress)),
		egress:       make(map[Endpoint]EgressCodec, len(egress)),
		ingressOrder: append([]IngressCodec(nil), ingress...),
		egressOrder:  append([]EgressCodec(nil), egress...),
	}
	for _, codec := range catalog.ingressOrder {
		if codec == nil {
			return nil, fmt.Errorf("nil ingress codec")
		}
		ep := codec.Endpoint()
		if err := validateEndpoint(ep); err != nil {
			return nil, fmt.Errorf("ingress codec: %w", err)
		}
		if _, exists := catalog.ingress[ep]; exists {
			return nil, fmt.Errorf("duplicate ingress endpoint: %s", ep)
		}
		catalog.ingress[ep] = codec
	}
	for _, codec := range catalog.egressOrder {
		if codec == nil {
			return nil, fmt.Errorf("nil egress codec")
		}
		ep := codec.Endpoint()
		if err := validateEndpoint(ep); err != nil {
			return nil, fmt.Errorf("egress codec: %w", err)
		}
		if _, exists := catalog.egress[ep]; exists {
			return nil, fmt.Errorf("duplicate egress endpoint: %s", ep)
		}
		catalog.egress[ep] = codec
	}

	sort.Slice(catalog.ingressOrder, func(i, j int) bool {
		return endpointLess(catalog.ingressOrder[i].Endpoint(), catalog.ingressOrder[j].Endpoint())
	})
	sort.Slice(catalog.egressOrder, func(i, j int) bool {
		return endpointLess(catalog.egressOrder[i].Endpoint(), catalog.egressOrder[j].Endpoint())
	})
	return catalog, nil
}

func (c *Catalog) Ingress(endpoint Endpoint) (IngressCodec, bool) {
	codec, ok := c.ingress[endpoint]
	return codec, ok
}

func (c *Catalog) Egress(endpoint Endpoint) (EgressCodec, bool) {
	codec, ok := c.egress[endpoint]
	return codec, ok
}

func (c *Catalog) EndpointFor(p Protocol) (Endpoint, bool) {
	for _, codec := range c.egressOrder {
		if endpoint := codec.Endpoint(); endpoint.Protocol == p {
			return endpoint, true
		}
	}
	return Endpoint{}, false
}

func (c *Catalog) IngressEndpoints() []IngressCodec {
	return append([]IngressCodec(nil), c.ingressOrder...)
}

func validateEndpoint(endpoint Endpoint) error {
	switch {
	case endpoint.Protocol == "":
		return fmt.Errorf("empty protocol")
	case endpoint.Workload == "":
		return fmt.Errorf("empty workload")
	case endpoint.Version == "":
		return fmt.Errorf("empty version")
	default:
		return nil
	}
}

func endpointLess(a, b Endpoint) bool {
	if a.Protocol != b.Protocol {
		return a.Protocol < b.Protocol
	}
	if a.Workload != b.Workload {
		return a.Workload < b.Workload
	}
	return a.Version < b.Version
}
