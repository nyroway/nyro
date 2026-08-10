package ir

import "encoding/json"

// ResponseFormat constrains the response format. Sealed union.
// Ported from ResponseFormat (serde tag = "type", rename_all = "snake_case").
type ResponseFormat interface{ responseFormat() }

// TextResponse requests plain text output.
type TextResponse struct{}

func (*TextResponse) responseFormat() {}

// JsonObjectResponse requests JSON object output.
type JsonObjectResponse struct{}

func (*JsonObjectResponse) responseFormat() {}

// JsonSchemaResponse requests output conforming to a JSON Schema.
type JsonSchemaResponse struct {
	Name   string
	Schema json.RawMessage
	Strict *bool // optional
}

func (*JsonSchemaResponse) responseFormat() {}
