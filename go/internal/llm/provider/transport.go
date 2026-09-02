package provider

import "context"

type Transport interface {
	// Do transfers ownership of every non-nil Response Body to the caller,
	// including when it also returns an error. A successful Response must have
	// a non-nil Body; Runtime rejects violations instead of dereferencing nil.
	Do(context.Context, Request) (*Response, error)
	CloseIdleConnections()
}
