# Protocol Identity

A protocol ID identifies a concrete API wire surface - the request/response
schema of a specific vendor API operation (an "interface"). It is NOT a provider
"family". Protocol IDs are vendor-prefixed for readability but vendor-orthogonal
in use: any provider that speaks a given wire format can use that protocol
(the vendor is expressed by the upstream's `provider` field).

Canonical endpoint form is `{protocol}/{version}` (e.g.
`openai-chatcompletions/v1`). The previous three-layer
`{protocol}/{name}/{version}` form is collapsed: because each protocol now maps
to a single logical operation, the middle `name` layer was redundant.

## Active Protocols (defined and exposed)

The current iteration focuses on the chat protocols; only these are exposed as
selectable protocols in config and the WebUI.

- `anthropic-messages` - Anthropic Messages API / Messages API / alias `claude`
- `openai-chatcompletions` - OpenAI Chat Completions API / ChatCompletions API / alias `openai`
- `openai-responses` - OpenAI Responses API / Responses API / alias `responses`
- `gemini-generatecontent` - Gemini GenerateContent API / GenerateContent API / alias `gemini`

## Declared but Commented (enable when implemented / re-exposed)

- `openai-embeddings` - OpenAI Embeddings API / Embeddings API / alias `embeddings`
- `gemini-interactions` - Gemini Interactions API / Interactions API / alias `interactions`
- `bedrock-converse` - AWS Bedrock Converse API / Converse API / alias `bedrock`
- `azure-modelinference` - Azure AI Model Inference API / ModelInference API / alias `azure`

## Notes

- `openai-chatcompletions`, `openai-responses`, and `openai-embeddings` are
  separate protocols (distinct request/response schemas) even though they share
  the OpenAI vendor prefix.
- The former `openai-compatible` "family" (which grouped chat-completions and
  embeddings) is removed; every protocol is now interface-level, unifying the
  concept across the whole set.
- `openai-embeddings` currently has a working codec (`codec/embeddings`) and
  e2e tests. It is defined but kept commented/unexposed for now so this
  iteration can focus on the chat protocols; re-expose it (and confirm the
  `/v1/embeddings` ingress) when embeddings work resumes.
- A protocol is independent of transport (authentication, URL structure, query
  params); transport is owned by the provider's auth scheme and URL
  construction.
