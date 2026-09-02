// Package authn defines transport-neutral inbound authentication values.
package authn

// Credentials contains normalized credentials extracted by an ingress.
// Outbound Provider credentials do not use this type.
type Credentials struct {
	APIKey string
}

// Identity is the authenticated subject attached to one request. Anonymous is
// true when policy permits a request without credentials.
type Identity struct {
	Subject           string
	CredentialID      string
	CredentialName    string
	CredentialPreview string
	Anonymous         bool
}
