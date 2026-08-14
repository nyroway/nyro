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
	r, _ := newEngine(t, "")

	tests := []struct {
		name       string
		key        string
		value      string
		wantStatus int
		want       string
	}{
		{name: "trims redis type", key: "state.type", value: " redis ", wantStatus: http.StatusOK, want: "redis"},
		{name: "rejects unknown type", key: "state.type", value: "etcd", wantStatus: http.StatusBadRequest},
		{name: "trims redis url", key: "state.url", value: " redis://127.0.0.1:6379/0 ", wantStatus: http.StatusOK, want: "redis://127.0.0.1:6379/0"},
		{name: "rejects wrong scheme", key: "state.url", value: "http://127.0.0.1:6379", wantStatus: http.StatusBadRequest},
		{name: "clears url", key: "state.url", value: "", wantStatus: http.StatusOK, want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := []byte("{\"value\":" + mustJSON(t, tt.value) + "}")
			rec := do(r, http.MethodPut, "/api/v1/settings/"+tt.key, "", body)
			if rec.Code != tt.wantStatus {
				t.Fatalf("PUT → %d %s, want %d", rec.Code, rec.Body.String(), tt.wantStatus)
			}
			if tt.wantStatus != http.StatusOK {
				return
			}
			var got map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got["value"] != tt.want {
				t.Errorf("stored value = %q, want %q", got["value"], tt.want)
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
