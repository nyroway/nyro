package telemetry

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestLogRecordJSONShape(t *testing.T) {
	code := int32(200)
	r := LogRecord{
		ID: "req_1", UpstreamID: "upstream-1", RouteID: "route-1", RouteModel: "gpt",
		ConsumerID: "consumer-1", ResponseStatusCode: &code, InputTokens: 10,
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{
		`"id":"req_1"`, `"upstream_id":"upstream-1"`, `"route_id":"route-1"`,
		`"route_model":"gpt"`, `"consumer_id":"consumer-1"`,
		`"response_status_code":200`, `"input_tokens":10`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("json missing %q in %s", want, s)
		}
	}
	for _, legacy := range []string{"provider_id", "model_name", "api_key_id", "client_status_code"} {
		if strings.Contains(s, legacy) {
			t.Errorf("json contains legacy field %q in %s", legacy, s)
		}
	}
}
