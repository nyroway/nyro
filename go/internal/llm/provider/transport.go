package provider

import "context"

type Transport interface {
	Do(context.Context, Request) (*Response, error)
	CloseIdleConnections()
}
