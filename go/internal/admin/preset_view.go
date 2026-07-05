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

// protocolCredentialsView is the control-plane projection of
// provider.CredentialSchemaFor for the /protocol-credentials endpoint: the
// credential fields a protocol needs, independent of any vendor preset.
type protocolCredentialsView struct {
	Protocol string                `json:"protocol"`
	Fields   []credentialFieldView `json:"fields"`
}

type modelDiscoveryView struct {
	Kind   string   `json:"kind"`
	URL    string   `json:"url,omitempty"`
	Values []string `json:"values,omitempty"`
}

// toCredentialFieldViews projects provider.CredentialField values into their
// serializable view, shared by presetView and the protocol-credentials endpoint.
func toCredentialFieldViews(fields []provider.CredentialField) []credentialFieldView {
	out := make([]credentialFieldView, 0, len(fields))
	for _, f := range fields {
		out = append(out, credentialFieldView{
			Name:         f.Name,
			Type:         f.Type,
			Required:     f.Required,
			Default:      f.Default,
			Values:       f.Values,
			Env:          f.Env,
			RequiredWhen: f.RequiredWhen,
		})
	}
	return out
}

// toPresetView projects a provider.Definition into the serializable preset view.
func toPresetView(d provider.Definition) presetView {
	pv := presetView{
		ID:              d.ID,
		Name:            d.Name,
		DefaultProtocol: d.DefaultProtocol,
		DefaultModel:    d.DefaultModel,
		Protocols:       make([]presetProtocol, 0, len(d.Protocols)),
		Credentials:     credentialView{Fields: toCredentialFieldViews(d.Credentials.Fields)},
		Models: modelDiscoveryView{
			Kind:   d.Models.Kind,
			URL:    d.Models.URL,
			Values: d.Models.Values,
		},
	}
	for _, p := range d.Protocols {
		pv.Protocols = append(pv.Protocols, presetProtocol{ID: p.ID, BaseURL: p.BaseURL})
	}
	return pv
}
