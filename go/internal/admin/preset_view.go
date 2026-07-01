package admin

import "github.com/nyroway/nyro/go/internal/provider"

// presetView is the control-plane projection of a provider Definition for the
// provider-presets endpoint (WebUI dropdown / new Go frontend). It is a flat,
// serializable view derived from the single source of truth
// (provider.Definitions): no channels, no OAuth, English-only names.
type presetView struct {
	ID              string             `json:"id"`
	Name            string             `json:"name"`
	DefaultProtocol string             `json:"default_protocol"`
	DefaultModel    string             `json:"default_model,omitempty"`
	Protocols       []presetProtocol   `json:"protocols"`
	Credentials     credentialView     `json:"credentials"`
	Models          modelDiscoveryView `json:"models"`
}

type presetProtocol struct {
	ID      string `json:"id"`
	BaseURL string `json:"base_url,omitempty"`
}

type credentialView struct {
	Fields []credentialFieldView `json:"fields"`
}

type credentialFieldView struct {
	Name         string         `json:"name"`
	Type         string         `json:"type"`
	Required     bool           `json:"required"`
	Default      string         `json:"default,omitempty"`
	Values       []string       `json:"values,omitempty"`
	Env          string         `json:"env,omitempty"`
	RequiredWhen map[string]any `json:"required_when,omitempty"`
}

type modelDiscoveryView struct {
	Kind   string   `json:"kind"`
	URL    string   `json:"url,omitempty"`
	Values []string `json:"values,omitempty"`
}

// toPresetView projects a provider.Definition into the serializable preset view.
func toPresetView(d provider.Definition) presetView {
	pv := presetView{
		ID:              d.ID,
		Name:            d.Name,
		DefaultProtocol: d.DefaultProtocol,
		DefaultModel:    d.DefaultModel,
		Protocols:       make([]presetProtocol, 0, len(d.Protocols)),
		Credentials:     credentialView{Fields: make([]credentialFieldView, 0, len(d.Credentials.Fields))},
		Models: modelDiscoveryView{
			Kind:   d.Models.Kind,
			URL:    d.Models.URL,
			Values: d.Models.Values,
		},
	}
	for _, p := range d.Protocols {
		pv.Protocols = append(pv.Protocols, presetProtocol{ID: p.ID, BaseURL: p.BaseURL})
	}
	for _, f := range d.Credentials.Fields {
		pv.Credentials.Fields = append(pv.Credentials.Fields, credentialFieldView{
			Name:         f.Name,
			Type:         f.Type,
			Required:     f.Required,
			Default:      f.Default,
			Values:       f.Values,
			Env:          f.Env,
			RequiredWhen: f.RequiredWhen,
		})
	}
	return pv
}
