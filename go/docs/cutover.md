# Nyro Go cutover runbook (P6)

The Go implementation (`go/`) is feature-complete and tested. This runbook covers the
dual-run shadow phase and the cutover from the Rust gateway. It is an
**operational** checklist — the code is done; this is deployment.

> **WebUI note:** the root `webui/` directory is Rust-only for the duration
> of the parallel period — it targets the Rust admin API and is not being
> ported. Go WebUI work lives entirely in `go/webui/`, targets the Go server's
> management API schema directly, and is documented in `go/README.md` (## Go WebUI) and
> `go/webui/README.md`.

## 0. Build the Go binary

```bash
cd go
go build -o /tmp/nyro .
# optional UI:
(cd webui && pnpm install && pnpm build)
```

## 1. Stand up both data planes side-by-side

- Rust gateway on the production port (e.g. `:19530`).
- Go proxy on a shadow port, initially using a standalone snapshot, and the Go
  server on its loopback control-plane port with config-sync disabled:

```bash
# data plane (shadow):
/tmp/nyro proxy --listen 127.0.0.1:19529 \
  --config ./config.yaml
# control plane (management API + WebUI):
/tmp/nyro server --listen 127.0.0.1:19531 \
  --sync-listen= --webui-dir ./webui/dist \
  --token "$NYRO_SERVER_TOKEN" --auto-migrate
```

`--auto-migrate` above lets this first-boot server create its own (default
sqlite) schema; drop it once the db already exists. It's off by default
regardless of backend — see `go/docs/schema/database.md` for the
postgres migration workflow.

The standalone proxy reads `config.yaml` once at startup; edit the file and
restart to apply a change. The server manages its own database, but with
`--sync-listen=` its changes are not pushed to that proxy. This is the
simplest isolated shadow setup. The config schema is documented in
[Standalone `config.yaml`](schema/config.md).

To hot-reload the proxy's config from the server instead of a static
`--config` YAML file, enable the server's config-sync gRPC server and point
the proxy at it. This channel carries every upstream's `credentials_json`,
so no TLS paths select plaintext with a security warning, while all three
`--sync-tls-ca/-cert/-key` paths select mTLS. A partial set or a certificate
load failure stops startup; there is no downgrade to plaintext. See
[config-sync transport and mTLS](security/config-sync-mtls.md) for the full
`nyro ca` workflow. For same-host shadow testing, plaintext + loopback is the
fastest path:

```bash
# control plane, with config-sync enabled (loopback, plaintext + WARN):
/tmp/nyro server --listen 127.0.0.1:19531 \
  --sync-listen 127.0.0.1:19532 --token "$NYRO_SERVER_TOKEN" --auto-migrate
# data plane, subscribing to config-sync instead of --config:
/tmp/nyro proxy --listen 127.0.0.1:19529 \
  --server 127.0.0.1:19532
```

Plaintext can also be used across a tightly controlled trusted network, but
the warnings remain: provider credentials are unencrypted, the server is not
authenticated to the proxy, and proxy clients are not authenticated to the
server. For normal cross-host deployments, sign certificates and configure a
complete TLS path set on both processes:

```bash
/tmp/nyro ca init
/tmp/nyro ca sign-server
/tmp/nyro ca sign-proxy --node-id gw-1
# distribute ca.pem + admin.{pem,key.pem} / gateway.{pem,key.pem}, then:
/tmp/nyro server --sync-listen 0.0.0.0:19532 --auto-migrate \
  --sync-tls-ca ~/.nyro/pki/ca.pem --sync-tls-cert ~/.nyro/pki/admin.pem --sync-tls-key ~/.nyro/pki/admin-key.pem
/tmp/nyro proxy --server server.internal:19532 \
  --sync-tls-ca ~/.nyro/pki/ca.pem --sync-tls-cert ~/.nyro/pki/gateway.pem --sync-tls-key ~/.nyro/pki/gateway-key.pem
```

`--config` and `--server` are mutually exclusive — exactly one must
be set. `--sync-listen=` disables the config-sync server; explicitly setting
`--config-poll-interval` or any `--sync-tls-*` flag in that mode is an error.
Connected proxies are visible on the server at
`GET /api/v1/nodes` (and the WebUI's Nodes page) — a best-effort, in-memory
view that reflects only currently-open connections.

For multiple server replicas sharing one database (typically PostgreSQL), set a
positive polling interval on every replica so a write handled by one is noticed
and pushed by the others. Since this is exactly the shared postgres case
`go/docs/schema/database.md` covers, apply the schema with
DDL a DBA reviews (print it with `nyro migrate dump`/`diff`; see
`go/docs/schema/migrations.md`) before first boot instead of passing
`--auto-migrate` here:

```bash
# Run on each server host, with a distinct --listen/--sync-listen address.
/tmp/nyro server --listen 10.0.0.11:19531 \
  --sync-listen 10.0.0.11:19532 \
  --dsn "$NYRO_SHARED_DSN" --config-poll-interval 1s \
  --token "$NYRO_SERVER_TOKEN"
```

The polling default is `0` (disabled); a single server still pushes its own
writes immediately. Add the complete mTLS path set above to every replica and
proxy unless the config-sync network is deliberately trusted for plaintext.

The server's REST/WebUI listener and the proxy's client listener are HTTP-only. The
config-sync TLS flags secure only the gRPC channel; terminate HTTPS for those
HTTP listeners at a reverse proxy, ingress, load balancer, or service mesh.
`--token` is optional Bearer protection for the server's `/api/v1` routes and
does not secure config-sync. A non-loopback server listener without `--token`
logs a warning instead of refusing to start; exposed management APIs should use a token
over deployment-layer HTTPS.

## 2. Shadow traffic + parity diff

Mirror a sample of real client traffic to the Go proxy and diff the responses
using the parity normalizer (`internal/parity`):

- For each mirrored request, run it through both data planes.
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

1. Move a small fraction of clients to the Go proxy (DNS / load balancer /
   port flip).
2. Monitor error rate, latency, and the Go `/api/v1/stats/overview`.
3. Ramp to 100%.

## 4. Rollback

Keep the Rust gateway warm during ramp. On regression, flip traffic back — the
Rust gateway and its storage are untouched and still authoritative, so no
rollback data migration is needed.

## 5. Retire Rust

After the Go proxy serves 100% cleanly for the agreed bake period:

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
