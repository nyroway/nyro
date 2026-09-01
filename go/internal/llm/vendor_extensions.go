package llm

import "encoding/json"

// VendorExtensions is the three-segment model for fields that have no home in
// the canonical IR schema:
//   - Ingress: extra fields from the client body, forwarded to egress if the
//     egress vendor understands them.
//   - Egress: fields injected by the egress codec / provider adapter just
//     before the upstream call.
//   - PassthroughSafe: fields the gateway does not understand but is allowed
//     to copy verbatim after a whitelist check.
type VendorExtensions struct {
	Ingress         map[string]json.RawMessage
	Egress          map[string]json.RawMessage
	PassthroughSafe map[string]json.RawMessage
}

// IsEmpty reports whether all three segments are empty.
func (v VendorExtensions) IsEmpty() bool {
	return len(v.Ingress) == 0 && len(v.Egress) == 0 && len(v.PassthroughSafe) == 0
}
