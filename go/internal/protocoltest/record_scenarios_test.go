package protocoltest

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nyroway/nyro/go/internal/proxy"
)

// TestRecordScenarios is a gated one-off that records real cassettes for the
// new matrix scenarios (multi-turn tool, multimodal, reasoning) through fixed
// backends: Xiaomi MiMo for openai-chat/openai-responses/anthropic-messages and
// Google (gemini-3.1-flash-lite) for google-gemini. It drives an Anthropic
// inbound request per outbound protocol; cassettes are keyed by outbound and
// reused across inbounds. Run with NYRO_TEST_RECORD_NEW=1 + the keys below.
func TestRecordScenarios(t *testing.T) {
	if os.Getenv("NYRO_TEST_RECORD_NEW") == "" {
		t.Skip("set NYRO_TEST_RECORD_NEW=1 + NYRO_TEST_MIMO_API_KEY + NYRO_TEST_GEMINI_API_KEY")
	}
	mimoKey := os.Getenv("NYRO_TEST_MIMO_API_KEY")
	gemKey := os.Getenv("NYRO_TEST_GEMINI_API_KEY")
	const mimoHost = "https://api.xiaomimimo.com"
	const gemHost = "https://generativelanguage.googleapis.com"

	const tinyPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="

	// Anthropic-inbound scenario bodies (recorded once per outbound). multimodal
	// is disabled on MiMo pro, so it uses mimo-v2.5; others use mimo-v2.5-pro.
	scenarios := []struct {
		name, body string
		multimodal bool
	}{
		{"multiturn_tool", `{"model":"conversion-test-model","max_tokens":200,` +
			`"tools":[{"name":"get_weather","description":"Get the weather for a city",` +
			`"input_schema":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}}],` +
			`"messages":[{"role":"user","content":"What is the weather in Paris?"},` +
			`{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"get_weather","input":{"city":"Paris"}}]},` +
			`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"18C and sunny"}]}]}`, false},
		{"reasoning", `{"model":"conversion-test-model","max_tokens":2000,"thinking":{"type":"enabled","budget_tokens":1024},` +
			`"messages":[{"role":"user","content":"What is 17*23? Think step by step."}]}`, false},
		{"multimodal", `{"model":"conversion-test-model","max_tokens":100,"messages":[{"role":"user","content":[` +
			`{"type":"text","text":"Describe this image in one word."},` +
			`{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + tinyPNG + `"}}]}]}`, true},
	}

	targets := []struct{ protocol, provider, baseURL, key, model, mmModel string }{
		{"openai-chat", "openrouter", mimoHost, mimoKey, "mimo-v2.5-pro", "mimo-v2.5"},
		{"openai-responses", "openrouter", mimoHost, mimoKey, "mimo-v2.5-pro", "mimo-v2.5"},
		{"anthropic-messages", "anthropic", mimoHost + "/anthropic", mimoKey, "mimo-v2.5-pro", "mimo-v2.5"},
		{"google-gemini", "gemini", gemHost, gemKey, "gemini-3.1-flash-lite", "gemini-3.1-flash-lite"},
	}

	for _, tg := range targets {
		if tg.key == "" {
			t.Logf("SKIP %s — no key", tg.protocol)
			continue
		}
		for _, sc := range scenarios {
			model := tg.model
			if sc.multimodal {
				model = tg.mmModel
			}
			t.Run(tg.protocol+"/"+sc.name, func(t *testing.T) {
				casPath := filepath.Join("testdata", "cassettes", tg.protocol, sc.name+".json")
				tr := &recordTransport{real: http.DefaultTransport, path: casPath}
				cell := Cell{
					In:  Inbound{Name: "anthropic-messages", Path: "/v1/messages"},
					Out: Outbound{Provider: tg.provider, Protocol: tg.protocol},
				}
				gw := buildGateway(t, cell, tr, tg.baseURL, tg.key, model)
				router := proxy.NewRouter(gw)
				req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(sc.body))
				req.Header.Set("Content-Type", "application/json")
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, req)

				if rec.Code != http.StatusOK {
					t.Errorf("[REJECTED] %s/%s (%s) HTTP %d: %s", tg.protocol, sc.name, model, rec.Code, snippet(rec.Body.String()))
					return
				}
				t.Logf("[OK] %s/%s (%s)%s → %s", tg.protocol, sc.name, model, detectFeatures(rec.Body.String()), snippet(rec.Body.String()))
			})
		}
	}
}
