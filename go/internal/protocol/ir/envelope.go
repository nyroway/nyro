package ir

import "encoding/json"

// RawEnvelope is a snapshot of the original inbound request, captured before
// any codec transformation. Preserved for pass-through mode, audit logging,
// and debug round-trip verification.
type RawEnvelope struct {
	Body    json.RawMessage   // optional original JSON body (verbatim)
	Headers map[string]string // flattened, lowercase keys
	Method  string
	Path    string
}

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
