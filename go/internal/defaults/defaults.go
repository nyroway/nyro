// Package defaults holds compile-time default addresses shared by the server
// and client commands so they stay in sync from a single source of truth.
//
// Layer: 0 (foundation) — stdlib only; no internal imports.
package defaults

const (
	ControlPlaneAddr    = "127.0.0.1:19531"
	DataPlaneAddr       = "127.0.0.1:19530"
	ControlPlaneBaseURL = "http://" + ControlPlaneAddr
)
