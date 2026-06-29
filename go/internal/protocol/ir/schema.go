package ir

import "encoding/json"

// SchemaObject is a tool-parameter JSON Schema, stored with lowercase "type"
// values (JSON Schema canonical form). The Google encoder uppercases "type"
// values on the way out.
//
// Ported from SchemaObject (newtype over serde_json::Value).
//
// TODO(P2): port NewSchemaObject (recursive lowercase normalization) and
// GoogleWire (recursive uppercase) when the Gemini codec lands.
type SchemaObject struct {
	V json.RawMessage
}

// AsValue returns the raw schema bytes.
func (s SchemaObject) AsValue() json.RawMessage { return s.V }
