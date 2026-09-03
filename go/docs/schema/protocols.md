# Protocol Identity

A protocol ID identifies a concrete API wire surface - the request/response
schema of a specific vendor API operation (an "interface"). It is NOT a provider
"family". Protocol IDs are family-prefixed for readability but vendor-orthogonal
in use: any provider that speaks a given wire format can use that protocol
(the vendor is expressed by the upstream's `provider` field).

Canonical endpoint form is `{protocol}/{version}` (e.g.
`openai-chatcompletions/v1`). The previous three-layer
`{protocol}/{name}/{version}` form is collapsed: because each protocol now maps
to a single logical operation, the middle `name` layer was redundant.

## Naming rule

```
Protocol ID  = {family}-{api}
package path = strings.ReplaceAll(id, "-", "/")
```

- **family** is the brand that owns the wire format's naming: `openai`,
  `anthropic`, `gemini`, and in future `bedrock` / `azure`. Note that family is
  *not* the vendor. Gemini is a protocol family with more than one API under it
  (`generateContent` and Interactions), so the family is `gemini`, not `google`.
  Likewise Bedrock Converse and Azure AI Model Inference are platform-branded
  rather than company-branded.
- **api** is the vendor's own API name, lowercased with all separators removed.
- There is **exactly one `-`**, and the api segment must not contain another —
  the ID↔path bijection depends on it. `internal/bootstrap/llm_protocols_test.go`
  asserts every explicitly composed codec's package path matches its protocol ID.

The HTTP ingress path (`/v1/chat/completions`) is unrelated to the package
path; it is declared by each ingress codec in its `Capabilities.IngressRoutes`.

## Alias rule

Each protocol has **at most one** alias: the single word users actually say,
never a mechanical abbreviation of the full name. An alias is permanently bound
to the protocol it was introduced for and does **not** follow the vendor's
recommended default API — `gemini` stays on `generateContent` even though Google
now steers new projects to the Interactions API. New protocols get no alias by
default. Alias collisions are resolved at add time by refusing the alias (a
future `gemini-embedcontent` does not get `embed`, which is taken).

## Compiled protocols and HTTP ingress

Bootstrap explicitly compiles all five codecs below and the LLM HTTP ingress
mounts every declared route. `Selectable` is a narrower UI/CLI choice: the four
chat protocols are offered by protocol selectors, while OpenAI Embeddings is
compiled and routable but intentionally omitted from those selectors.

| Identifier | Display Name | Alias | HTTP ingress | Selectable |
|---|---|---|---|---|
| `anthropic-messages` | Anthropic Messages | `claude` | `POST /v1/messages` | yes |
| `gemini-generatecontent` | Gemini generateContent | `gemini` | `POST /v1beta/models/{resource:.+:.+}` | yes |
| `openai-chatcompletions` | OpenAI Chat Completions | `openai` | `POST /v1/chat/completions` | yes |
| `openai-embeddings` | OpenAI Embeddings | `embed` | `POST /v1/embeddings` | no |
| `openai-responses` | OpenAI Responses | `codex` | `POST /v1/responses` | yes |

## Not yet declared

| API | Would be | Note |
|---|---|---|
| Gemini Interactions | `gemini-interactions` | GA 2026-06; stateful (`previous_interaction_id`), `steps` replaces `outputs` |
| AWS Bedrock Converse | `bedrock-converse` | cross-model unified schema |
| Azure AI Model Inference | `azure-inference` | deployment in path, `api-version` query |

## Notes

- Display names label the API and carry no `API` suffix — the suffix was true of
  every entry and so distinguished none of them.
- `openai-chatcompletions`, `openai-responses`, and `openai-embeddings` are
  separate protocols (distinct request/response schemas) even though they share
  the OpenAI family prefix.
- This schema has no released consumers yet, so there is no back-compat alias
  set: each protocol has exactly the alias listed above, and dropped identifiers
  are rejected as unknown protocols. The authoritative list of protocols and
  rejected identifiers is `internal/llm/protocol/protocols.json` — the contract shared with
  the WebUI; both `internal/llm/protocol/contract_test.go` and
  `webui/src/lib/protocol.contract.test.ts` assert their tables match it, so the
  two hand-maintained copies cannot drift.
- The former `openai-compatible` "family" (which grouped chat-completions and
  embeddings) is removed; every protocol is now interface-level, unifying the
  concept across the whole set.
- `openai-embeddings` has a compiled codec, a mounted `/v1/embeddings` HTTP
  ingress, and end-to-end coverage. Its `Selectable=false` flag only keeps it
  out of the current WebUI and CLI protocol selectors.
- A protocol is independent of transport (authentication, URL structure, query
  params); transport is owned by the provider's auth scheme and URL
  construction. `Endpoint.Version` is the version segment of the
  vendor's **URL path**; when a vendor versions the wire format on some other
  axis (Anthropic's `anthropic-version: 2023-06-01` header) that axis belongs to
  the codec and the Authenticator, which is why the endpoint is
  `anthropic-messages/v1`.
