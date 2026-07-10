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
