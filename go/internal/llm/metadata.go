package llm

// RequestMetadata carries request-scoped metadata alongside the core IR.
type RequestMetadata struct {
	// Vendor is the three-segment vendor extension bag.
	Vendor VendorExtensions
}
