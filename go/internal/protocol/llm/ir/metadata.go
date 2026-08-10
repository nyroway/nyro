package ir

import "github.com/nyroway/nyro/go/internal/protocol/llm/spec"

// RequestMetadata carries request-scoped metadata alongside the core IR.
type RequestMetadata struct {
	// SourceProtocol is the protocol the client spoke.
	SourceProtocol *spec.ProtocolEndpoint
	// Raw is the preserved envelope for pass-through / audit.
	Raw *RawEnvelope
	// Vendor is the three-segment vendor extension bag.
	Vendor VendorExtensions
}
