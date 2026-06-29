package gemini

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSanitizeGeminiSchema(t *testing.T) {
	t.Parallel()
	in := json.RawMessage(`{"$schema":"http://json-schema.org/draft-07/schema","type":"object","properties":{"name":{"type":"string","$ref":"#/definitions/foo"}},"additionalProperties":false,"definitions":{"foo":{"type":"string"}}}`)
	out := sanitizeGeminiSchema(in)
	s := string(out)
	for _, bad := range []string{"$schema", "$ref", "additionalProperties", "definitions"} {
		if strings.Contains(s, bad) {
			t.Errorf("sanitized schema still contains %q: %s", bad, s)
		}
	}
	if !strings.Contains(s, `"type":"object"`) || !strings.Contains(s, `"name"`) {
		t.Errorf("valid keys were stripped: %s", s)
	}
}
