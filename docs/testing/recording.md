# Recording LLM Fixtures with `nyro-tools`

This document is the SOP for contributing **byte-accurate LLM
fixtures** that drive Nyro's proxy/protocol-matrix tests
(`tests/e2e/proxy/`). Each recorded `.jsonl` file captures one full
real-LLM interaction; CI later replays those bytes through every
ingress protocol Nyro speaks, asserting the round-trip survives.

If you've never recorded before, the [Quick Start](#quick-start)
gets you to a green PR in under five minutes.

---

## Quick Start

```bash
# 1. build the recorder
cargo build -p nyro-tools

# 2. set your real-LLM API key
export DEEPSEEK_API_KEY=sk-...

# 3. record all 4 scenarios for one (vendor, protocol)
#    NOTE: -e must already include the API version segment (e.g. /v1).
target/debug/nyro-tools record \
  --vendor deepseek \
  -p openai-chat \
  -e https://api.deepseek.com/v1 \
  -o tests/e2e/fixtures \
  --model deepseek-chat \
  --reasoning-model deepseek-reasoner \
  --api-key-env DEEPSEEK_API_KEY

# 4. inspect what landed
ls tests/e2e/fixtures/openai-chat/deepseek/

# 5. run the matrix locally
python3 -m pytest tests/e2e/proxy -q -m proxy
```

That single `record` call writes 4 `.jsonl` files
(`basic-nonstream.jsonl`, `basic-stream.jsonl`,
`tool-use-stream.jsonl`, `reasoning-stream.jsonl`) under
`tests/e2e/fixtures/openai-chat/deepseek/`. The pytest matrix picks
them up automatically — there is no allow-list, no manifest, no
generator.

---

## Concepts

### Composite key — `replay_model`

Every fixture carries a single canonical identifier:

```
<vendor>--<protocol>--<scenario>
```

Examples:

```
deepseek--openai-chat--basic-stream
azure--openai-chat--reasoning-stream
google-aistudio--google-content--tool-use-stream
```

`record` writes this string into the `.jsonl`, `replay` keys its
HashMap on it, and pytest reuses it as the route name / vmodel /
target.model — the four values are deliberately identical so no code
ever has to split the string back into pieces.

### The 4 fixed scenarios

| Scenario          | Stream | Reasoning toggle | Anchor token                    |
|-------------------|--------|------------------|---------------------------------|
| `basic-nonstream` | no     | no               | `NYRO_PROBE_BASIC_NONSTREAM`    |
| `basic-stream`    | yes    | no               | `NYRO_PROBE_BASIC_STREAM`       |
| `tool-use-stream` | yes    | no               | `NYRO_PROBE_TOOL_USE_STREAM`    |
| `reasoning-stream`| yes    | yes              | `NYRO_PROBE_REASONING_STREAM`   |

Each scenario embeds its anchor in the user prompt so the LLM echoes
it back; pytest later asserts the anchor survives Nyro's protocol
conversion.

Run `nyro-tools print-scenarios` to dump the full table as JSON.

### Directory layout

```
tests/e2e/fixtures/
├── openai-chat/
│   ├── deepseek/
│   │   ├── basic-nonstream.jsonl
│   │   ├── basic-stream.jsonl
│   │   ├── tool-use-stream.jsonl
│   │   └── reasoning-stream.jsonl
│   └── azure/
│       └── ...
├── openai-responses/
├── anthropic-messages/
└── google-content/
```

`record` derives the `<protocol>/<vendor>/` subpath from
`--upstream-protocol` and `--vendor`; you only point `-o` at the
fixtures root.

### Existing fixtures are kept

`record` is **skip-if-exists**. If `basic-stream.jsonl` already lives
in the target dir it is left untouched; only missing scenario files
are written. To re-record a scenario, delete its `.jsonl` first.

---

## Endpoint URL convention

`-e/--upstream-endpoint` (used by both `record` and `proxy`) **must
include the API version segment**. `nyro-tools` then attaches a
protocol-fixed suffix (`/chat/completions`, `/responses`, `/messages`,
`/models/{model}:...`). This lets non-standard vendors slot in cleanly:

| Vendor | `-e` value | Final URL (record's basic-stream) |
|--------|------------|-----------------------------------|
| OpenAI | `https://api.openai.com/v1` | `…/v1/chat/completions` |
| DeepSeek | `https://api.deepseek.com/v1` | `…/v1/chat/completions` |
| Anthropic | `https://api.anthropic.com/v1` | `…/v1/messages` |
| Google AI Studio | `https://generativelanguage.googleapis.com/v1beta` | `…/v1beta/models/{model}:streamGenerateContent` |
| Zhipu (openai-chat) | `https://open.bigmodel.cn/api/coding/paas/v4` | `…/api/coding/paas/v4/chat/completions` |
| Zhipu (anthropic-messages) | `https://open.bigmodel.cn/api/anthropic/v1` | `…/api/anthropic/v1/messages` |
| OpenRouter | `https://openrouter.ai/api/v1` | `…/api/v1/chat/completions` |
| Requesty | `https://router.requesty.ai/v1` | `…/v1/chat/completions` |

A bare `https://api.deepseek.com` (no path) is rejected at startup
with a clear hint — this prevents silently building the wrong URL.

**`proxy` semantics**: client SDKs hard-code their version prefix
(`/v1/chat/completions` etc.); proxy strips that prefix and replaces
it with `endpoint.path`, so the same `-e` value works for both
`record` and `proxy`.

**Out of scope**: deeply custom URL shapes that don't fit
`<base>/<version>/<suffix>` — Azure OpenAI deployment-scoped paths,
GCP Vertex AI project-scoped paths, AWS Bedrock signature-based
paths. Those vendors will need dedicated `--flavor` codepaths in
future iterations.

---

## CLI Reference

### `nyro-tools record`

| Flag | Required | Description |
|------|----------|-------------|
| `--vendor <NAME>` | yes | Kebab-case vendor id (must not contain `--`). Examples: `deepseek`, `azure`, `google-aistudio`, `google-vertex`. |
| `-p, --upstream-protocol <P>` | yes | One of `openai-chat`, `openai-responses`, `anthropic-messages`, `google-content`. |
| `-e, --upstream-endpoint <URL>` | yes | Vendor base URL **including the API version path**, e.g. `https://api.deepseek.com/v1`, `https://api.anthropic.com/v1`, `https://generativelanguage.googleapis.com/v1beta`, `https://open.bigmodel.cn/api/coding/paas/v4`. A bare host like `https://api.deepseek.com` is rejected fast. See [Endpoint URL convention](#endpoint-url-convention). |
| `-o, --output-dir <PATH>` | yes | Fixtures root. Subdirs `<protocol>/<vendor>/` are created automatically. |
| `--model <NAME>` | yes | Real LLM model used for non-reasoning scenarios. |
| `--reasoning-model <NAME>` | no | Real LLM model used for `reasoning-*` scenarios; defaults to `--model`. |
| `--api-key-env <NAME>` | yes | Environment variable holding the API key (e.g. `DEEPSEEK_API_KEY`). The key is never persisted. |

`record` always runs all 4 scenarios. Scenarios for which the
selected protocol has no body template (e.g. `tool-use-stream` on
some experimental protocols) are skipped silently.

### `nyro-tools replay`

| Flag | Default | Description |
|------|---------|-------------|
| `-P, --port` | `25208` | Listen port |
| `-H, --host` | `127.0.0.1` | Listen host |
| `-p, --protocol` | required | Which protocol's ingress paths to serve |
| `-i, --input-dir` | required | Fixtures root (recursively scanned) |

A single replay instance only loads fixtures whose `protocol` field
matches `-p`, so you can point `-i tests/e2e/fixtures/` at the entire
tree four times — once per protocol on adjacent ports — without
collision.

### `nyro-tools proxy`

A protocol-aware passthrough used for protocol-debugging sessions.
Strips the client SDK's standard version prefix (`/v1`, `/v1beta`)
and re-attaches the user-supplied `-e` (which may carry a non-standard
version path like `/api/coding/paas/v4`). Headers and bodies are
forwarded verbatim except for hop-by-hop. **Not used by CI.**

| Flag | Default | Description |
|------|---------|-------------|
| `-P, --port` | `25208` | Listen port |
| `-H, --host` | `127.0.0.1` | Listen host |
| `-p, --upstream-protocol` | required | Upstream protocol short name |
| `-e, --upstream-endpoint` | required | Upstream base URL **including the API version path**, same convention as `record`. |

Example: point an OpenAI SDK at `localhost:25208`, upstream goes to Zhipu:

```bash
target/debug/nyro-tools proxy \
  -p openai-chat \
  -e https://open.bigmodel.cn/api/coding/paas/v4
# Client sends POST /v1/chat/completions
# proxy → POST https://open.bigmodel.cn/api/coding/paas/v4/chat/completions
```

### `nyro-tools print-scenarios`

Prints the `Scenario` table as JSON (anchor, stream, expected_fields
per protocol). pytest consumes this as its single source of truth.

---

## Recording a New Vendor

1. Pick the vendor's primary upstream protocol(s). A vendor may
   support more than one — record each as a separate `record`
   invocation. Examples:
   - DeepSeek: `openai-chat`, `anthropic-messages`
   - Azure OpenAI: `openai-chat`, `openai-responses`
   - Google AI Studio: `google-content`
   - Google Vertex: `google-content` (different endpoint)
2. Pick the model names. For most vendors this is the real product
   name (`deepseek-chat`, `claude-3-5-sonnet-20241022`,
   `gemini-2.0-flash`). Reasoning models are typically a separate
   product (`deepseek-reasoner`, `o4-mini`, `gemini-2.5-pro`).
3. Export the API key as an env var that you'll pass via
   `--api-key-env`.
4. Run `nyro-tools record …`. Each successful scenario creates one
   `.jsonl` under `tests/e2e/fixtures/<protocol>/<vendor>/`.
5. Eyeball one of the files — every line must contain
   `"replay_model": "<vendor>--<protocol>--<scenario>"`.
6. Run the matrix locally: `pytest tests/e2e/proxy -m proxy`.
7. Commit `tests/e2e/fixtures/<protocol>/<vendor>/*.jsonl`.

### Auth scheme defaults

`record` ships sensible defaults per protocol:

| Protocol | Auth header |
|----------|-------------|
| `openai-chat`, `openai-responses` | `Authorization: Bearer <key>` |
| `anthropic-messages` | `x-api-key: <key>` + `anthropic-version: 2023-06-01` |
| `google-content` | `x-goog-api-key: <key>` |

For vendors that deviate (e.g. Azure's `api-key:` header) we will
extend `record` rather than ask each contributor to patch CLI flags.

---

## Sensitive-Data Handling

`record` strips a hardcoded blacklist of sensitive headers before
writing the fixture:

- `authorization`
- `x-api-key`
- `x-goog-api-key`
- `cookie`, `set-cookie`
- `proxy-authorization`

The list lives in `crates/nyro-tools/src/fixture.rs`
(`SENSITIVE_HEADER_BLACKLIST`) and is matched case-insensitively.
There is no opt-out flag.

The request body itself is recorded verbatim. **Never put secrets in
prompts.** All scenario prompts ship a fixed anchor token; do not edit
them to embed credentials, customer data, or PII.

---

## Verifying a Fixture

A correctly recorded fixture line looks like this (whitespace added
for readability — the actual file is a single line per record):

```json
{
  "version": 1,
  "replay_model": "deepseek--openai-chat--basic-stream",
  "scenario": "basic-stream",
  "vendor": "deepseek",
  "protocol": "openai-chat",
  "recorded_at": "2026-04-25T22:13:45Z",
  "request": {
    "method": "POST",
    "path": "/v1/chat/completions",
    "headers": {"content-type": "application/json"},
    "body_json": {"model": "deepseek-chat", "stream": true, "messages": [...]}
  },
  "response": {
    "status": 200,
    "headers": {"content-type": "text/event-stream"},
    "body_base64": "ZGF0YTogey..."
  }
}
```

Quick sanity checks:

```bash
# every fixture must have a properly-formed replay_model
jq -r '.replay_model' tests/e2e/fixtures/openai-chat/deepseek/*.jsonl

# nothing sensitive should leak into recorded headers
jq -r '.request.headers' tests/e2e/fixtures/openai-chat/deepseek/*.jsonl
```

If a fixture's `body_base64` is empty (`""`), the upstream returned
zero bytes — usually a 4xx error in disguise. `record` already aborts
on HTTP ≥ 400; if you see this, delete the file and re-record.

---

## PR Checklist

- [ ] Ran `cargo build -p nyro-tools` against current `master`.
- [ ] Recorded all 4 scenarios for at least one (vendor, protocol).
- [ ] No `Authorization` / `x-api-key` / `x-goog-api-key` / `cookie`
      strings appear inside any `.jsonl` (use `grep -i 'authorization'`).
- [ ] `python3 -m pytest tests/e2e/proxy -q -m proxy` passes locally.
- [ ] Updated `docs/testing/coverage-matrix.md` with the new vendor row.

---

## FAQ

**Q. Do I need to record every scenario?**
Strongly preferred — the matrix gets weaker with every gap. If a
vendor genuinely doesn't support a scenario (e.g. no reasoning
model), open an issue first; we'd rather adjust the scenario set than
ship lop-sided coverage.

**Q. The model didn't echo the anchor token. Now what?**
Re-run; LLMs are non-deterministic and most of them comply >99% of
the time when explicitly told to "reply with the literal token X".
If you genuinely cannot get a model to echo the anchor, raise an
issue — the prompt may need refinement upstream rather than per
contributor.

**Q. Can I edit a fixture by hand?**
Don't. The whole point of byte-level fixtures is that they capture
the upstream's wire format. Hand-editing slowly drifts away from
real LLM behaviour. If a fixture is wrong, re-record.

**Q. How big can a fixture get?**
A typical streaming fixture is 2–10 KB after base64. Reasoning
streams can hit 30 KB. If a single fixture exceeds 100 KB you're
probably recording with way too generous `max_tokens` — keep it
tight.

**Q. Can I record multiple vendors in one shot?**
No, one `record` call = one (vendor, protocol). This keeps API key
scoping clean and the directory layout self-documenting.

**Q. Why must `-e` include `/v1` (or similar)?**
So non-standard vendors slot in cleanly. Zhipu's openai-chat lives at
`https://open.bigmodel.cn/api/coding/paas/v4/chat/completions`, not
`/v1/chat/completions`. By forcing the user to spell the version
prefix, `nyro-tools` keeps the suffix logic protocol-fixed and
vendor-agnostic. See [Endpoint URL convention](#endpoint-url-convention)
for the full table.
