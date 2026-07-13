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
(cd webui && pnpm install && pnpm build)
```

## 1. Stand up both gateways side-by-side

- Rust gateway on the production port (e.g. `:19530`).
- Go gateway on a shadow port, initially using a standalone snapshot, and Go
  Admin on its loopback control-plane port with config-sync disabled:

```bash
# data plane (shadow):
/tmp/nyro gateway --listen 127.0.0.1:19529 \
  --config-file ./config.yaml
# control plane (admin API + WebUI):
/tmp/nyro admin --listen 127.0.0.1:19531 \
  --config-listen= --webui-dir ./webui/dist \
  --token "$NYRO_ADMIN_TOKEN"
```

The standalone gateway reads `config.yaml` once at startup; edit the file and
restart to apply a change. Admin manages its own database, but with
`--config-listen=` its changes are not pushed to that gateway. This is the
simplest isolated shadow setup. The config schema is documented in
[Standalone `config.yaml`](schema/config.md).

To hot-reload the gateway's config from the admin instead of a static
`--config-file` YAML file, enable the admin's config-sync gRPC server and point
the gateway at it. This channel carries every upstream's `credentials_json`,
so no TLS paths select plaintext with a security warning, while all three
`--config-tls-ca/-cert/-key` paths select mTLS. A partial set or a certificate
load failure stops startup; there is no downgrade to plaintext. See
[config-sync transport and mTLS](security/config-sync-mtls.md) for the full
`nyro ca` workflow. For same-host shadow testing, plaintext + loopback is the
fastest path:

```bash
# control plane, with config-sync enabled (loopback, plaintext + WARN):
/tmp/nyro admin --listen 127.0.0.1:19531 \
  --config-listen 127.0.0.1:19532 --token "$NYRO_ADMIN_TOKEN"
# data plane, subscribing to config-sync instead of --config-file:
/tmp/nyro gateway --listen 127.0.0.1:19529 \
  --config-server 127.0.0.1:19532
```

Plaintext can also be used across a tightly controlled trusted network, but
the warnings remain: provider credentials are unencrypted, Admin is not
authenticated to gateway, and gateway clients are not authenticated to
Admin. For normal cross-host deployments, sign certificates and configure a
complete TLS path set on both processes:

```bash
/tmp/nyro ca init
/tmp/nyro ca sign-admin
/tmp/nyro ca sign-gateway --node-id gw-1
# distribute ca.pem + admin.{pem,key.pem} / gateway.{pem,key.pem}, then:
/tmp/nyro admin --config-listen 0.0.0.0:19532 \
  --config-tls-ca ~/.nyro/pki/ca.pem --config-tls-cert ~/.nyro/pki/admin.pem --config-tls-key ~/.nyro/pki/admin-key.pem
/tmp/nyro gateway --config-server admin.internal:19532 \
  --config-tls-ca ~/.nyro/pki/ca.pem --config-tls-cert ~/.nyro/pki/gateway.pem --config-tls-key ~/.nyro/pki/gateway-key.pem
```

`--config-file` and `--config-server` are mutually exclusive — exactly one must
be set. `--config-listen=` disables the config-sync server; explicitly setting
`--config-poll-interval` or any `--config-tls-*` flag in that mode is an error.
Connected gateways are visible on the admin at
`GET /api/v1/nodes` (and the WebUI's Nodes page) — a best-effort, in-memory
view that reflects only currently-open connections.

For multiple Admin replicas sharing one database (typically PostgreSQL or
MySQL), set a positive polling interval on every replica so a write handled by
one is noticed and pushed by the others:

```bash
# Run on each Admin host, with a distinct --listen/--config-listen address.
/tmp/nyro admin --listen 10.0.0.11:19531 \
  --config-listen 10.0.0.11:19532 \
  --dsn "$NYRO_SHARED_DSN" --config-poll-interval 1s \
  --token "$NYRO_ADMIN_TOKEN"
```

The polling default is `0` (disabled); a single Admin still pushes its own
writes immediately. Add the complete mTLS path set above to every replica and
gateway unless the config-sync network is deliberately trusted for plaintext.

Admin's REST/WebUI listener and gateway's client listener are HTTP-only. The
config-sync TLS flags secure only the gRPC channel; terminate HTTPS for those
HTTP listeners at a reverse proxy, ingress, load balancer, or service mesh.
`--token` is optional Bearer protection for Admin `/api/v1` routes and does
not secure config-sync. A non-loopback Admin listener without `--token` logs a
warning instead of refusing to start; exposed Admin APIs should use a token
over deployment-layer HTTPS.

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
Rust gateway and its storage are untouched and still authoritative, so no
rollback data migration is needed.

## 5. Retire Rust

After the Go gateway serves 100% cleanly for the agreed bake period:

1. Final verification from `go/`: `go test ./...` + `go build ./...`.
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
