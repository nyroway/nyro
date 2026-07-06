package provider

import (
	"encoding/json"
	"errors"
)

// DefaultAnthropicVersion is the anthropic-version header value applied by
// AuthenticatorFor for the anthropic-messages protocol. It mirrors
// defaultAnthropicVersion (anthropic.go) but is protocol-keyed rather than
// tied to the AnthropicProvider vendor struct.
const DefaultAnthropicVersion = defaultAnthropicVersion

// AuthenticatorFor returns the outbound Authenticator for one upstream, keyed
// by protocol rather than provider id. This is the data plane's protocol-first
// resolution entry point, replacing the old id-based Resolve/NewAuthenticator
// path: dispatch depends only on the protocol an upstream speaks, not on which
// named vendor (if any) it happens to be configured as.
func AuthenticatorFor(protocol string, up UpstreamRuntime) (Authenticator, error) {
	switch protocol {
	case ProtocolOpenAIChatCompletions, ProtocolOpenAIResponses:
		return NewBearerAuthenticator(up.CredentialsJSON)
	case ProtocolAnthropicMessages:
		return newAnthropicAuthenticator(up.CredentialsJSON)
	case ProtocolGeminiGenerateContent:
		return newGeminiContentAuthenticator(up.CredentialsJSON)
	default:
		if len(up.CredentialsJSON) == 0 {
			return NoopAuthenticator{}, nil
		}
		return NewBearerAuthenticator(up.CredentialsJSON)
	}
}

// newAnthropicAuthenticator builds the x-api-key + anthropic-version
// authenticator used for the anthropic-messages protocol.
func newAnthropicAuthenticator(credentials json.RawMessage) (Authenticator, error) {
	var c apiKeyCredentials
	if err := json.Unmarshal(credentials, &c); err != nil {
		return nil, err
	}
	if c.APIKey == "" {
		return nil, errors.New("provider: api_key is required")
	}
	return anthropicAuthenticator{apiKey: c.APIKey, version: DefaultAnthropicVersion}, nil
}

// newGeminiContentAuthenticator builds the fixed x-goog-api-key authenticator
// used for the gemini-generatecontent protocol, reusing geminiAuthenticator (gemini.go)
// with openaiCompatible forced false (Vertex/OAuth branching is out of scope).
func newGeminiContentAuthenticator(credentials json.RawMessage) (Authenticator, error) {
	var c apiKeyCredentials
	if err := json.Unmarshal(credentials, &c); err != nil {
		return nil, err
	}
	if c.APIKey == "" {
		return nil, errors.New("provider: api_key is required")
	}
	return geminiAuthenticator{apiKey: c.APIKey, openaiCompatible: false}, nil
}
