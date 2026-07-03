# Nyro Go cutover runbook (P6)

The Go gateway (`go/`) is feature-complete and tested. This runbook covers the
dual-run shadow phase and the cutover from the Rust gateway. It is an
**operational** checklist — the code is done; this is deployment.

> **WebUI note:** the root `webui/` directory is Rust-only for the duration
> of the parallel period — it targets the Rust admin API and is not being
> ported. Go WebUI work lives entirely in `go/webui/`, targets the Go admin
> API schema directly, and is documented in `go/README.md` (## Go WebUI) and
> `go/webui/README.md`.

## 0. Build the Go gateway

```bash
cd go
go build -o /tmp/nyro .
# optional UI:
(cd ../webui && pnpm install && pnpm build)
```

## 1. Stand up both gateways side-by-side

- Rust gateway on the production port (e.g. `:19530`).
- Go gateway on shadow ports (data plane + control plane share one storage/DB):

```bash
# data plane (shadow):
/tmp/nyro gateway --addr 127.0.0.1:19529
# control plane (admin API + WebUI):
/tmp/nyro admin --addr 127.0.0.1:19531 --webui-dir ../webui/dist --admin-token <token>
```

Both read the same upstream config (point the Go gateway at the same providers;
config is managed via its own `/api/v1` admin API or seeded from flags for the
shadow phase).

## 2. Shadow traffic + parity diff

Mirror a sample of real client traffic to the Go gateway and diff the responses
using the parity normalizer (`internal/parity`):

- For each mirrored request, run it through both gateways.
- Normalize both responses (`parity.NormalizeJSON` drops volatile fields:
  generated ids, timestamps, fingerprints) and compare.
- Investigate any normalized mismatch — those are real parity gaps to fix
  before cutover.

Streaming responses: diff per-frame after normalizing each SSE payload. Token
boundaries may differ; compare the concatenated content + final usage.

This is also where the **deferred OAuth acquisition flows** (Claude PKCE /
Codex / Vertex) get validated against the Rust gateway's wire behavior, and
where `tpm/tpd` token-quota accounting is wired once usage is captured.

## 3. Gradual cutover

Once shadow parity is clean:

1. Move a small fraction of clients to the Go gateway (DNS / load balancer /
   port flip).
2. Monitor error rate, latency, and the Go `/api/v1/stats/overview`.
3. Ramp to 100%.

## 4. Rollback

Keep the Rust gateway warm during ramp. On regression, flip traffic back — the
Rust gateway is untouched and still authoritative. No data migration is needed
for rollback (each gateway has its own storage).

## 5. Retire Rust

After the Go gateway serves 100% cleanly for the agreed bake period:

1. Final verification: `go test ./...` + `make -C go build`.
2. Stop the Rust gateway.
3. Remove/archive the Rust crates (`crates/`, `src-server/`, `src-tauri/`).
4. Promote `go/` to the repo root (import-path rewrite) if desired.

## Known parity gaps to close before/during cutover

- **OAuth acquisition flows** (Claude/Codex/Vertex): framework is in
  `internal/auth`; concrete driver flows + background refresh must be ported
  and validated here.
- **Token-quota accounting** (`tpm/tpd`): wire usage into the request log.
- **Codec edge cases** flagged `TODO(P5)` in each codec (multimodal parts,
  Anthropic cache_control raw round-trip, Gemini schema uppercasing,
  Responses reasoning-item passback, full event sequence).
- **Vendor metadata** (presets/channels → auth-mode resolution).
