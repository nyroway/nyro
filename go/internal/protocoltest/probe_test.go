package protocoltest

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/nyroway/nyro/go/internal/proxy"
)

// TestProbeCapabilities is a one-off diagnostic (not part of the golden matrix):
// it drives real requests through nyro to a live backend and reports whether
// each endpoint/model accepts multi-turn tool, multimodal, and reasoning
// scenarios — so we know which are worth adding to the matrix before recording.
//
// It hits the network and needs a key, so it is gated on NYRO_TEST_MIMO_API_KEY
// (Xiaomi MiMo — a fixed-model backend exposing chat/responses/anthropic). It
// writes nothing (no cassettes, no goldens).
func TestProbeCapabilities(t *testing.T) {
	key := os.Getenv("NYRO_TEST_MIMO_API_KEY")
	if key == "" {
		t.Skip("set NYRO_TEST_MIMO_API_KEY to probe the MiMo backend")
	}
	const host = "https://api.xiaomimimo.com"

	// A 1x1 PNG — the point is whether the endpoint accepts an image part.
	const tinyPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="

	// Capabilities (Anthropic Messages inbound). Model varies: only mimo-v2.5 has
	// multimodal; mimo-v2.5-pro covers text/tool/reasoning.
	caps := []struct{ name, model, body string }{
		{
			"multi-turn tool", "mimo-v2.5-pro",
			`{"model":"conversion-test-model","max_tokens":200,` +
				`"tools":[{"name":"get_weather","description":"Get the weather for a city",` +
				`"input_schema":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}}],` +
				`"messages":[` +
				`{"role":"user","content":"What is the weather in Paris?"},` +
				`{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"get_weather","input":{"city":"Paris"}}]},` +
				`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"18C and sunny"}]}` +
				`]}`,
		},
		{
			"reasoning (thinking)", "mimo-v2.5-pro",
			`{"model":"conversion-test-model","max_tokens":2000,"thinking":{"type":"enabled","budget_tokens":1024},` +
				`"messages":[{"role":"user","content":"What is 17*23? Think step by step."}]}`,
		},
		{
			"multimodal (image)", "mimo-v2.5",
			`{"model":"conversion-test-model","max_tokens":100,"messages":[{"role":"user","content":[` +
				`{"type":"text","text":"What color is this image?"},` +
				`{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + tinyPNG + `"}}` +
				`]}]}`,
		},
	}

	// Each MiMo endpoint. openai-chat/responses take Bearer auth (provider
	// "openrouter" is a convenient Bearer preset); the anthropic-compatible
	// endpoint lives under /anthropic and takes x-api-key (provider "anthropic").
	backends := []struct{ provider, protocol, baseURL string }{
		{"openrouter", "openai-chat", host},
		{"openrouter", "openai-responses", host},
		{"anthropic", "anthropic-messages", host + "/anthropic"},
	}

	for _, b := range backends {
		cell := Cell{
			In:  Inbound{Name: "anthropic-messages", Path: "/v1/messages"},
			Out: Outbound{Provider: b.provider, Protocol: b.protocol},
		}
		for _, c := range caps {
			t.Run(b.protocol+"/"+c.name, func(t *testing.T) {
				gw := buildGateway(t, cell, nil, b.baseURL, key, c.model) // tr=nil → real network
				router := proxy.NewRouter(gw)
				req := httptest.NewRequest(http.MethodPost, cell.In.Path, strings.NewReader(c.body))
				req.Header.Set("Content-Type", "application/json")
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, req)

				status := "OK"
				if rec.Code != http.StatusOK {
					status = "REJECTED"
				}
				t.Logf("[%s] %s via mimo %s (%s) → HTTP %d%s\n    %s",
					status, c.name, b.protocol, c.model, rec.Code, detectFeatures(rec.Body.String()), snippet(rec.Body.String()))
			})
		}
	}
}

// detectFeatures reports which Anthropic-response features the client body
// carries (the response is translated back to the inbound Anthropic protocol).
func detectFeatures(clientBody string) string {
	var f []string
	if strings.Contains(clientBody, `"type":"tool_use"`) {
		f = append(f, "tool_use")
	}
	if strings.Contains(clientBody, `"type":"thinking"`) || strings.Contains(clientBody, `"type":"redacted_thinking"`) {
		f = append(f, "thinking")
	}
	if strings.Contains(clientBody, `"type":"text"`) {
		f = append(f, "text")
	}
	if len(f) == 0 {
		return ""
	}
	return "  [" + strings.Join(f, ",") + "]"
}

func snippet(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 320 {
		return s[:320] + "…"
	}
	return s
}
