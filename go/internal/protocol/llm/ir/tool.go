package ir

import "encoding/json"

// ToolSpec is a user-defined tool specification.
type ToolSpec struct {
	Name         string
	Description  string          // optional
	Parameters   json.RawMessage // JSON Schema for input parameters
	Strict       *bool           // optional (OpenAI + Anthropic)
	CacheControl *CacheControl   // optional (Anthropic)
	Meta         json.RawMessage // optional vendor extras
}

// ToolChoice controls how the model selects tools. Sealed union.
// Ported from ToolChoice (serde rename_all = "snake_case").
type ToolChoice interface{ toolChoice() }

// AutoToolChoice lets the model decide whether to call a tool.
type AutoToolChoice struct{}

func (*AutoToolChoice) toolChoice() {}

// NoneToolChoice forbids tool calls.
type NoneToolChoice struct{}

func (*NoneToolChoice) toolChoice() {}

// RequiredToolChoice forces at least one tool call.
type RequiredToolChoice struct{}

func (*RequiredToolChoice) toolChoice() {}

// NamedToolChoice forces a specific tool by name.
type NamedToolChoice struct{ Name string }

func (*NamedToolChoice) toolChoice() {}

// RawToolChoice is a pass-through raw value for protocol-specific options.
type RawToolChoice struct{ Raw json.RawMessage }

func (*RawToolChoice) toolChoice() {}
