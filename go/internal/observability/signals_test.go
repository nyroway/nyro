package observability

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestLogRecordJSONShape(t *testing.T) {
	code := int32(200)
	r := LogRecord{ID: "req_1", ModelName: "gpt", ClientStatusCode: &code, InputTokens: 10}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{`"id":"req_1"`, `"model_name":"gpt"`, `"client_status_code":200`, `"input_tokens":10`} {
		if !strings.Contains(s, want) {
			t.Errorf("json missing %q in %s", want, s)
		}
	}
}
