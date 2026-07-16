package protocoltest

import "testing"

// Slice-1 cells: Anthropic inbound → OpenAI (real cross-protocol translation)
// and → Anthropic (identity sentinel: still round-trips through IR, must not
// lose fidelity). More cells are added as the matrix is filled out.
var (
	anthropicToOpenAI = Cell{
		In:  Inbound{Name: "anthropic-messages", Path: "/v1/messages"},
		Out: Outbound{Provider: "openai", Protocol: "openai-chat", Path: "/v1/chat/completions"},
	}
	anthropicToResponses = Cell{
		In:  Inbound{Name: "anthropic-messages", Path: "/v1/messages"},
		Out: Outbound{Provider: "openai", Protocol: "openai-responses", Path: "/v1/responses"},
	}
	anthropicToAnthropic = Cell{
		In:  Inbound{Name: "anthropic-messages", Path: "/v1/messages"},
		Out: Outbound{Provider: "anthropic", Protocol: "anthropic-messages", Path: "/v1/messages"},
	}
)

// Scenarios are Anthropic Messages wire bodies (model == routeModel). Each runs
// against every slice-1 cell.
var scenarios = []Scenario{
	{
		Name:    "text",
		Request: `{"model":"conversion-test-model","max_tokens":100,"messages":[{"role":"user","content":"Hello"}]}`,
	},
	{
		Name:    "text_stream",
		Stream:  true,
		Request: `{"model":"conversion-test-model","max_tokens":100,"stream":true,"messages":[{"role":"user","content":"Hello"}]}`,
	},
	{
		Name: "tool",
		Request: `{"model":"conversion-test-model","max_tokens":100,` +
			`"messages":[{"role":"user","content":"What is the weather in Paris?"}],` +
			`"tools":[{"name":"get_weather","description":"Get the weather for a city",` +
			`"input_schema":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}}]}`,
	},
}

func TestConversionMatrix(t *testing.T) {
	cells := []Cell{anthropicToOpenAI, anthropicToResponses, anthropicToAnthropic}
	for _, cell := range cells {
		for _, sc := range scenarios {
			t.Run(cell.dir()+"/"+sc.Name, func(t *testing.T) {
				RunCell(t, cell, sc)
			})
		}
	}
}
