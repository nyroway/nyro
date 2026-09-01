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

## Active Protocols (defined and exposed)

The current iteration focuses on the chat protocols; only these are exposed as
selectable protocols in config and the WebUI.

| Identifier | Display Name | Alias |
|---|---|---|
| `anthropic-messages` | Anthropic Messages | `claude` |
| `openai-chatcompletions` | OpenAI Chat Completions | `openai` |
| `openai-responses` | OpenAI Responses | `codex` |
| `gemini-generatecontent` | Gemini generateContent | `gemini` |

## Declared but Commented (enable when implemented / re-exposed)

| Identifier | Display Name | Alias |
|---|---|---|
| `openai-embeddings` | OpenAI Embeddings | `embed` |

## Not yet declared

| API | Would be | Note |
|---|---|---|
| Gemini Interactions | `gemini-interactions` | GA 2026-06; stateful (`previous_interaction_id`), `steps` replaces `outputs` |
| AWS Bedrock Converse | `bedrock/converse` | cross-model unified schema |
| Azure AI Model Inference | `azure/inference` | deployment in path, `api-version` query |

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
- `openai-embeddings` currently has a working codec
  (`internal/llm/protocol/openai/embeddings`) and e2e tests. It is defined but kept
  commented/unexposed for now so this iteration can focus on the chat protocols;
  re-expose it (and confirm the `/v1/embeddings` ingress) when embeddings work
  resumes.
- A protocol is independent of transport (authentication, URL structure, query
  params); transport is owned by the provider's auth scheme and URL
  construction. `Endpoint.Version` is the version segment of the
  vendor's **URL path**; when a vendor versions the wire format on some other
  axis (Anthropic's `anthropic-version: 2023-06-01` header) that axis belongs to
  the codec and the Authenticator, which is why the endpoint is
  `anthropic-messages/v1`.
