package admin

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/nyroway/nyro/go/internal/version"
)

func TestAdminPublicGatewayURLSetting(t *testing.T) {
	r, _ := newEngine(t, "")

	for _, tc := range []struct {
		name  string
		value string
		want  string
	}{
		{name: "clears", value: "", want: ""},
		{name: "trims and removes trailing slash", value: "  https://ai.example.com/  ", want: "https://ai.example.com"},
		{name: "allows local HTTP", value: "http://127.0.0.1:19530", want: "http://127.0.0.1:19530"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(r, http.MethodPut, "/api/v1/settings/gateway.public_url", "", []byte(`{"value":`+mustJSON(t, tc.value)+`}`))
			if rec.Code != http.StatusOK {
				t.Fatalf("PUT → %d %s", rec.Code, rec.Body.String())
			}
			var got struct {
				Value string `json:"value"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.Value != tc.want {
				t.Errorf("stored value = %q, want %q", got.Value, tc.want)
			}
		})
	}

	for _, value := range []string{
		"ftp://ai.example.com",
		"https://ai.example.com/v1",
		"https://ai.example.com?tenant=one",
		"https://ai.example.com?",
		"https://user:pass@ai.example.com",
		"https://ai.example.com#",
	} {
		t.Run("rejects "+value, func(t *testing.T) {
			rec := do(r, http.MethodPut, "/api/v1/settings/gateway.public_url", "", []byte(`{"value":`+mustJSON(t, value)+`}`))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("PUT → %d %s, want 400", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestAdminStateSettings(t *testing.T) {
	r, backend := newEngine(t, "")
	rec := do(r, http.MethodPut, "/api/v1/settings", "", []byte(`{
		"values": {
			"state.type": " redis ",
			"state.url": " redis://user:secret@redis.internal:6379/2 "
		}
	}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT batch → %d %s", rec.Code, rec.Body.String())
	}
	var response struct {
		Values map[string]string `json:"values"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"state.type": "redis",
		"state.url":  "redis://user:secret@redis.internal:6379/2",
	}
	for key, wantValue := range want {
		if response.Values[key] != wantValue {
			t.Errorf("response %s = %q, want %q", key, response.Values[key], wantValue)
		}
		got, err := backend.Storage().Settings().Get(key)
		if err != nil || got != wantValue {
			t.Errorf("stored %s = %q, %v; want %q", key, got, err, wantValue)
		}
	}

	for _, tc := range []struct {
		name string
		body string
	}{
		{name: "missing URL", body: `{"values":{"state.type":"redis"}}`},
		{name: "missing type", body: `{"values":{"state.url":"redis://redis.internal:6379/0"}}`},
		{name: "unsupported TLS", body: `{"values":{"state.type":"redis","state.url":"rediss://redis.internal:6379/0"}}`},
		{name: "memory with URL", body: `{"values":{"state.type":"memory","state.url":"redis://redis.internal:6379/0"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, backend := newEngine(t, "")
			settings := backend.Storage().Settings()
			if err := settings.SetMany(map[string]string{"state.type": "memory", "state.url": ""}); err != nil {
				t.Fatal(err)
			}
			rec := do(r, http.MethodPut, "/api/v1/settings", "", []byte(tc.body))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("PUT batch → %d %s, want 400", rec.Code, rec.Body.String())
			}
			for key, wantValue := range map[string]string{"state.type": "memory", "state.url": ""} {
				got, err := settings.Get(key)
				if err != nil || got != wantValue {
					t.Errorf("stored %s after rejection = %q, %v; want %q", key, got, err, wantValue)
				}
			}
		})
	}

	for _, key := range []string{"state.type", "state.url"} {
		t.Run("rejects single-key "+key, func(t *testing.T) {
			rec := do(r, http.MethodPut, "/api/v1/settings/"+key, "", []byte(`{"value":"memory"}`))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("PUT single key → %d %s, want 400", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestAdminStatusIncludesVersion(t *testing.T) {
	r, _ := newEngine(t, "")
	rec := do(r, http.MethodGet, "/api/v1/status", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status → %d %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Version != version.Version {
		t.Errorf("version = %q, want %q", got.Version, version.Version)
	}
}

func mustJSON(t *testing.T, value string) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}
