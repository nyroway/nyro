// Package provider implements the vendor axis: the Vendor interface (auth
// headers, URL building, request/response mutation hooks), the 7-step
// build_request / parse_response pipeline, and the vendor registry. Vendors
// encapsulate upstream-provider quirks; a generic OpenAI-compatible adapter
// backs user-defined "custom" providers.
//
// Ported from crates/nyro-core/src/provider.
package provider
