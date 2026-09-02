package provider

import (
	"fmt"
	"sort"
	"strings"
)

type Registration struct {
	Definition Definition
	Factory    Factory
	Fallback   bool
}

type Catalog struct {
	definitions map[string]Definition
	factories   map[string]Factory
	fallback    Factory
}

func NewCatalog(registrations ...Registration) (*Catalog, error) {
	catalog := &Catalog{
		definitions: make(map[string]Definition, len(registrations)),
		factories:   make(map[string]Factory, len(registrations)),
	}
	seen := make(map[string]struct{}, len(registrations))
	for _, registration := range registrations {
		id := normalizeID(registration.Definition.ID)
		if id == "" {
			return nil, fmt.Errorf("provider: empty provider ID")
		}
		if registration.Factory == nil {
			return nil, fmt.Errorf("provider %q: nil driver factory", id)
		}
		if _, exists := seen[id]; exists {
			return nil, fmt.Errorf("provider: duplicate registration: %q", id)
		}
		seen[id] = struct{}{}
		if registration.Fallback {
			if catalog.fallback != nil {
				return nil, fmt.Errorf("provider: duplicate fallback registration")
			}
			catalog.fallback = registration.Factory
			continue
		}
		definition := cloneDefinition(registration.Definition)
		definition.ID = id
		catalog.definitions[id] = definition
		catalog.factories[id] = registration.Factory
	}
	if catalog.fallback == nil {
		return nil, fmt.Errorf("provider: missing fallback registration")
	}
	return catalog, nil
}

func (c *Catalog) Lookup(id string) (Definition, bool) {
	if c == nil {
		return Definition{}, false
	}
	definition, ok := c.definitions[normalizeID(id)]
	return cloneDefinition(definition), ok
}

func (c *Catalog) Definitions() []Definition {
	if c == nil {
		return nil
	}
	definitions := make([]Definition, 0, len(c.definitions))
	for _, definition := range c.definitions {
		definitions = append(definitions, cloneDefinition(definition))
	}
	sort.Slice(definitions, func(i, j int) bool {
		if definitions[i].Priority != definitions[j].Priority {
			return definitions[i].Priority < definitions[j].Priority
		}
		return definitions[i].ID < definitions[j].ID
	})
	return definitions
}

func (c *Catalog) DriverFor(id string) Factory {
	if c != nil {
		if factory, ok := c.factories[normalizeID(id)]; ok {
			return factory
		}
		return c.fallback
	}
	return nil
}

func normalizeID(id string) string {
	return strings.ToLower(strings.TrimSpace(id))
}

func cloneDefinition(definition Definition) Definition {
	clone := definition
	clone.Protocols = append([]Protocol(nil), definition.Protocols...)
	clone.Credentials.Fields = make([]CredentialField, len(definition.Credentials.Fields))
	for i, field := range definition.Credentials.Fields {
		clone.Credentials.Fields[i] = field
		clone.Credentials.Fields[i].Values = append([]string(nil), field.Values...)
		clone.Credentials.Fields[i].RequiredWhen = cloneStringMap(field.RequiredWhen)
	}
	clone.Extra = cloneStringMap(definition.Extra)
	return clone
}

func cloneStringMap(source map[string]any) map[string]any {
	if source == nil {
		return nil
	}
	clone := make(map[string]any, len(source))
	for key, value := range source {
		clone[key] = cloneValue(value)
	}
	return clone
}

func cloneValue(value any) any {
	switch value := value.(type) {
	case map[string]any:
		return cloneStringMap(value)
	case []any:
		clone := make([]any, len(value))
		for i := range value {
			clone[i] = cloneValue(value[i])
		}
		return clone
	case []string:
		return append([]string(nil), value...)
	default:
		return value
	}
}
