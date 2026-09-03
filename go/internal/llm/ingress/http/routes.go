package httpingress

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/nyroway/nyro/go/internal/llm/protocol"
)

type installedRoute struct {
	method  string
	pattern string
	codec   protocol.IngressCodec
}

func catalogRoutes(catalog *protocol.Catalog) ([]installedRoute, error) {
	seen := map[string]protocol.Endpoint{
		http.MethodGet + "\x00/v1/models": {},
	}
	var routes []installedRoute
	for _, codec := range catalog.IngressEndpoints() {
		for _, declared := range codec.Capabilities().IngressRoutes {
			method := strings.ToUpper(strings.TrimSpace(declared.Method))
			pattern := strings.TrimSpace(declared.Pattern)
			if method == "" || pattern == "" {
				return nil, fmt.Errorf("LLM HTTP ingress: endpoint %s declares an empty method or pattern", codec.Endpoint())
			}
			key := method + "\x00" + pattern
			if previous, exists := seen[key]; exists {
				if previous.Protocol == "" {
					return nil, fmt.Errorf("LLM HTTP ingress: duplicate route %s %s conflicts with models endpoint", method, pattern)
				}
				return nil, fmt.Errorf("LLM HTTP ingress: duplicate route %s %s for endpoints %s and %s", method, pattern, previous, codec.Endpoint())
			}
			seen[key] = codec.Endpoint()
			routes = append(routes, installedRoute{method: method, pattern: pattern, codec: codec})
		}
	}
	return routes, nil
}

func routeParams(request *http.Request) map[string]string {
	routeContext := chi.RouteContext(request.Context())
	if routeContext == nil {
		return nil
	}
	params := make(map[string]string, len(routeContext.URLParams.Keys))
	for index, key := range routeContext.URLParams.Keys {
		if index < len(routeContext.URLParams.Values) {
			params[key] = routeContext.URLParams.Values[index]
		}
	}
	return params
}
