# Protocol Identity

A protocol ID identifies a concrete API wire surface - the request/response
schema of a specific vendor API operation (an "interface"). It is NOT a provider
"family". Protocol IDs are vendor-prefixed for readability but vendor-orthogonal
in use: any provider that speaks a given wire format can use that protocol
(the vendor is expressed by the upstream's `provider` field).

Canonical endpoint form is `{protocol}/{version}` (e.g.
`openai-chat/v1`). The previous three-layer
`{protocol}/{name}/{version}` form is collapsed: because each protocol now maps
to a single logical operation, the middle `name` layer was redundant.

## Active Protocols (defined and exposed)

The current iteration focuses on the chat protocols; only these are exposed as
selectable protocols in config and the WebUI.

| Identifier | Display Name | Alias |
|---|---|---|
| `anthropic-messages` | Anthropic Messages API | `claude` |
| `openai-chat` | OpenAI Compatible API | `openai` |
| `openai-responses` | OpenAI Responses API | `codex`, `openai-resp` |
| `google-gemini` | Google Gemini API | `gemini` |

## Declared but Commented (enable when implemented / re-exposed)

| Identifier | Display Name | Alias |
|---|---|---|
| `openai-embeddings` | OpenAI Embeddings API | `embed`, `openai-embed` |

## Notes

- `openai-chat`, `openai-responses`, and `openai-embeddings` are
  separate protocols (distinct request/response schemas) even though they share
  the OpenAI vendor prefix.
- This schema has no released consumers yet, so there is no back-compat alias
  set: each protocol has exactly the aliases listed above, and old/dropped
  identifiers (e.g. `openai-chatcompletions`, `gemini-generatecontent`,
  `responses`, `embeddings`) are rejected as unknown protocols.
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
