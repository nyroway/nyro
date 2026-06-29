package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestJSON(t *testing.T) {
	rec := httptest.NewRecorder()
	JSON(rec, http.StatusCreated, map[string]any{"ok": true, "n": 3})
	if rec.Code != http.StatusCreated {
		t.Errorf("status = %d, want 201", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["ok"] != true || got["n"] != float64(3) {
		t.Errorf("body = %s", rec.Body.String())
	}
}

func TestError(t *testing.T) {
	rec := httptest.NewRecorder()
	Error(rec, http.StatusBadRequest, "bad input", "invalid_request")
	var got map[string]map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["error"]["message"] != "bad input" || got["error"]["type"] != "invalid_request" {
		t.Errorf("body = %s", rec.Body.String())
	}
}

func TestDecode(t *testing.T) {
	req := httptest.NewRequest("POST", "/", strings.NewReader(`{"a":"b"}`))
	var v map[string]string
	if err := Decode(req, &v); err != nil {
		t.Fatal(err)
	}
	if v["a"] != "b" {
		t.Errorf("decoded = %v", v)
	}
}
