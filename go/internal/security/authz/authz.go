// Package authz defines transport-neutral authorization inputs and decisions.
package authz

import "github.com/nyroway/nyro/go/internal/security/authn"

// Action identifies an operation subject to authorization policy.
type Action string

const (
	// InvokeModel authorizes invoking a logical LLM model route.
	InvokeModel Action = "invoke_model"
)

// Request is the complete typed input to an authorization policy.
type Request struct {
	Identity authn.Identity
	RouteID  string
	Model    string
	Action   Action
}

// Decision is the result of evaluating an authorization Request.
type Decision struct {
	Allowed bool
	Reason  string
}
