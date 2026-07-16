package protocoltest

import "testing"

// The conversion matrix is every inbound protocol (client-facing) crossed with
// every outbound protocol (upstream). Cassettes are keyed by outbound protocol
// and reused across inbounds, so filling out new inbound rows needs no new
// recording — only offline golden regeneration.

// Inbound protocols. Gemini carries the model + action in the path, so it needs
// a distinct streaming path; the others stream via a body flag.
var (
	inAnthropic  = Inbound{Name: "anthropic-messages", Path: "/v1/messages"}
	inOpenAIChat = Inbound{Name: "openai-chat", Path: "/v1/chat/completions"}
	inResponses  = Inbound{Name: "openai-responses", Path: "/v1/responses"}
	inGemini     = Inbound{
		Name:       "google-gemini",
		Path:       "/v1beta/models/" + routeModel + ":generateContent",
		StreamPath: "/v1beta/models/" + routeModel + ":streamGenerateContent",
	}
)

// Outbound protocols. google-gemini leaves Path empty (the codec embeds the
// model in the path, so the harness skips the path assertion).
var (
	outOpenAIChat = Outbound{Provider: "openai", Protocol: "openai-chat", Path: "/v1/chat/completions"}
	outResponses  = Outbound{Provider: "openai", Protocol: "openai-responses", Path: "/v1/responses"}
	outAnthropic  = Outbound{Provider: "anthropic", Protocol: "anthropic-messages", Path: "/v1/messages"}
	outGemini     = Outbound{Provider: "gemini", Protocol: "google-gemini", Path: ""}
)

var (
	inbounds  = []Inbound{inAnthropic, inOpenAIChat, inResponses, inGemini}
	outbounds = []Outbound{outOpenAIChat, outResponses, outAnthropic, outGemini}
)

// scenarioSpecs are the scenarios run against every cell; Request is filled per
// inbound protocol from scenarioBodies.
var scenarioSpecs = []Scenario{
	{Name: "text"},
	{Name: "text_stream", Stream: true},
	{Name: "tool"},
}

// scenarioBodies holds each scenario's client request in each inbound protocol's
// wire format. Model is routeModel where the protocol carries it in the body;
// Gemini omits it (path-encoded).
var scenarioBodies = map[string]map[string]string{
	"anthropic-messages": {
		"text":        `{"model":"conversion-test-model","max_tokens":100,"messages":[{"role":"user","content":"Hello"}]}`,
		"text_stream": `{"model":"conversion-test-model","max_tokens":100,"stream":true,"messages":[{"role":"user","content":"Hello"}]}`,
		"tool": `{"model":"conversion-test-model","max_tokens":100,` +
			`"messages":[{"role":"user","content":"What is the weather in Paris?"}],` +
			`"tools":[{"name":"get_weather","description":"Get the weather for a city",` +
			`"input_schema":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}}]}`,
	},
	"openai-chat": {
		"text":        `{"model":"conversion-test-model","max_tokens":100,"messages":[{"role":"user","content":"Hello"}]}`,
		"text_stream": `{"model":"conversion-test-model","max_tokens":100,"stream":true,"messages":[{"role":"user","content":"Hello"}]}`,
		"tool": `{"model":"conversion-test-model","max_tokens":100,` +
			`"messages":[{"role":"user","content":"What is the weather in Paris?"}],` +
			`"tools":[{"type":"function","function":{"name":"get_weather","description":"Get the weather for a city",` +
			`"parameters":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}}}]}`,
	},
	"openai-responses": {
		"text":        `{"model":"conversion-test-model","max_output_tokens":100,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"Hello"}]}]}`,
		"text_stream": `{"model":"conversion-test-model","max_output_tokens":100,"stream":true,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"Hello"}]}]}`,
		"tool": `{"model":"conversion-test-model","max_output_tokens":100,` +
			`"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"What is the weather in Paris?"}]}],` +
			`"tools":[{"type":"function","name":"get_weather","description":"Get the weather for a city",` +
			`"parameters":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}}]}`,
	},
	"google-gemini": {
		"text":        `{"contents":[{"role":"user","parts":[{"text":"Hello"}]}],"generationConfig":{"maxOutputTokens":100}}`,
		"text_stream": `{"contents":[{"role":"user","parts":[{"text":"Hello"}]}],"generationConfig":{"maxOutputTokens":100}}`,
		"tool": `{"contents":[{"role":"user","parts":[{"text":"What is the weather in Paris?"}]}],` +
			`"generationConfig":{"maxOutputTokens":100},` +
			`"tools":[{"functionDeclarations":[{"name":"get_weather","description":"Get the weather for a city",` +
			`"parameters":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}}]}]}`,
	},
}

func TestConversionMatrix(t *testing.T) {
	for _, in := range inbounds {
		for _, out := range outbounds {
			cell := Cell{In: in, Out: out}
			for _, spec := range scenarioSpecs {
				sc := spec
				sc.Request = scenarioBodies[in.Name][spec.Name]
				t.Run(cell.dir()+"/"+sc.Name, func(t *testing.T) {
					RunCell(t, cell, sc)
				})
			}
		}
	}
}
