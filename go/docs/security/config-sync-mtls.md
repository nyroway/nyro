# Config-sync transport and mTLS

The config-sync gRPC channel is how `nyro serve` (control plane) pushes the
live config snapshot — including every upstream's `credentials_json` — to every
connected `nyro proxy` (data plane). Its transport mode is selected only from
the three `--sync-tls-*` paths:

- **No TLS paths:** plaintext, allowed on loopback and refused off-host unless
  a join token is configured (see "The non-loopback plaintext gate").
- **All three TLS paths:** mTLS. The server requires and verifies a proxy
  client certificate, and the proxy verifies the server's certificate and
  identity.

Authentication is a separate layer from encryption: `--sync-token` gates who
may subscribe and works with or without TLS, while mTLS additionally gives each
node a verifiable identity.

Plaintext needs no separate opt-out flag. Supplying only one or two TLS paths
is a startup error. Supplying all three paths but failing to read or validate
any certificate or key is also a startup error; Nyro never downgrades a
requested mTLS configuration to plaintext. Server and proxy must be
configured for the same mode or their connection cannot be established.

## The non-loopback plaintext gate

The stream carries every upstream's `credentials_json`, and there is no way to
trim that — a data plane cannot call an upstream without its credentials — so
an unauthenticated plaintext config-sync port is equivalent to publishing the
whole configuration to anyone who can reach it.

Nyro therefore **refuses to start** when config-sync would cross a network with
neither encryption nor authentication:

| Address | Transport | Auth | Behaviour |
|---|---|---|---|
| loopback | plaintext | none | starts silently — the single-node default |
| loopback | plaintext | token | starts silently |
| non-loopback | plaintext | none | **refuses to start** |
| non-loopback | plaintext | token | starts, warns the token crosses the wire in the clear |
| any | mTLS | certificate | starts silently |

The trigger is the address, not whether TLS is configured. Both `--listen` and
`--sync-listen` default to loopback, so the zero-config single-node path never
trips the gate — and because loopback no longer emits a plaintext warning, a
warning that does appear is worth reading.

Server and proxy enforce this symmetrically. The listener is where the exposure
is created, but a proxy dialling in the clear still pulls credentials across the
network, so `--server` pointing at a non-loopback address is gated too.

**There is no `--insecure`-style override.** The only rejected combination is
non-loopback + plaintext + no token, and its escape hatch is to set a token —
which is itself a security improvement, unlike a flag whose whole function is
to switch a safety check off. Set `--sync-token`, or configure mTLS, or bind to
loopback.

> **Containers:** binding loopback inside a container makes the port
> unreachable from outside it, so any Docker/Kubernetes deployment that exposes
> config-sync lands in the non-loopback row and **must** set a token or mTLS.

### Join tokens

`--sync-token` is repeatable on the server and accepts any of the configured
values, so a token can be rotated with no downtime: add the new one, roll the
proxies onto it, then drop the old one.

```bash
nyro serve --sync-listen 0.0.0.0:19532 --sync-token "$OLD" --sync-token "$NEW"
nyro proxy --server server.internal:19532 --sync-token "$NEW"
```

Prefer the environment (`NYRO_SERVE_SYNC_TOKEN`, `NYRO_PROXY_SYNC_TOKEN`) over
the flag, which exposes the value in `ps`. The same applies to the management
API's `--token`.

**A token authorizes; it does not identify.** Every holder is equally
privileged and indistinguishable, and `node_id` stays self-reported — so any
client that can subscribe can claim any id. The WebUI's node list flags such
ids as unverified for exactly this reason. Only mTLS yields a per-node identity,
derived from the client certificate's SPIFFE SAN.

The embedded data plane of `nyro serve` needs no token: it subscribes over an
in-memory pipe with no socket, and is reported as `conn_mode: inprocess`.

## The three commands

`nyro tool ca` is an **offline** certificate authority — it never runs as a
service. Everything it produces is a plain file you distribute yourself (scp,
Ansible, a Secret, a Docker volume, whatever fits your deployment).

```bash
nyro tool ca init [--dir ~/.nyro/pki] [--valid 87600h] [--force]
nyro tool ca sign-server [--dir ~/.nyro/pki] [--valid 8760h] [--out server]
nyro tool ca sign-proxy [--dir ~/.nyro/pki] [--node-id <id>] [--valid 8760h] [--out proxy]
```

- `init` creates (or, without `--force`, reuses) the CA: `ca.pem` +
  `ca-key.pem` in `--dir`.
- `sign-server` issues the server's config-sync certificate, with a **fixed
  identity** encoded as a SPIFFE URI SAN (`spiffe://nyro/server`) — not a DNS/IP
  SAN list. There is no `--advertise`-style flag: the proxy verifies this
  certificate by identity, not by matching a hostname it dialed against a SAN
  list, so the same certificate is valid no matter what address a proxy uses
  to reach the server (direct, load balancer, Kubernetes Service name, IP —
  see "Why identity, not hostname" below).
- `sign-proxy` issues one proxy node's client certificate, with its identity
  encoded as a SPIFFE URI SAN (`spiffe://nyro/proxy/<node-id>`). Run once
  per proxy node (or once per shared cert if you're intentionally pooling
  identity across a fleet — see "elastic scaling" below). `--node-id`
  defaults to a random value if omitted.

All three write fixed filenames — `<out>.pem` / `<out>-key.pem` — into
`--dir`. That directory is purely `nyro tool ca`'s own bookkeeping; server and
proxy never read it directly (see below).

## Runtime: explicit paths only, no directory auto-discovery

`server` and `proxy` do **not** know about `nyro tool ca`'s output directory.
They only ever load three explicit file paths:

```bash
nyro serve --sync-tls-ca ~/.nyro/pki/ca.pem \
            --sync-tls-cert ~/.nyro/pki/server.pem \
            --sync-tls-key ~/.nyro/pki/server-key.pem

nyro proxy --server 127.0.0.1:19532 \
           --sync-tls-ca ~/.nyro/pki/ca.pem \
           --sync-tls-cert ~/.nyro/pki/proxy.pem \
           --sync-tls-key ~/.nyro/pki/proxy-key.pem
```

All three flags must be given together, or not at all — a partial set (e.g.
just `--sync-tls-cert`) is rejected at startup rather than silently
guessing at a directory convention for the missing pieces or falling back to
plaintext. This keeps the loading logic to a single path with no precedence
rules to reason about, and it means a certificate and its CA can never be
silently mismatched from two different sources.

The two lifecycles — `nyro tool ca`'s offline, one-shot signing and the server/proxy's
long-running runtime load — are deliberately decoupled. In practice you write
the three paths once into whatever starts the process (systemd unit,
docker-compose, Helm values), not on every interactive invocation.

### Why identity, not hostname

The proxy's config-sync client verifies the server's certificate by SPIFFE
identity (`spiffe://nyro/server`), not by matching a hostname/IP SAN against
the address in `--server` — the classic web-PKI model most CLI tools
default to. That classic model needs a `--tls-server-name`-style escape
hatch the moment the dial address and the cert's SAN diverge (a load
balancer, a Kubernetes Service name, an IP dialed against a DNS-only cert),
and that escape hatch is itself a footgun: it's easy to reach for as "make
the error go away" and easy to leave silently pointed at the wrong thing
after a rename.

Identity-based verification sidesteps the whole problem: `--server`
can be a direct address, a load balancer, or a Kubernetes Service name — none
of it matters, because the check is "does this certificate say
`spiffe://nyro/server`", not "does this certificate's SAN match the string I
dialed". There is deliberately no override flag for this on the proxy side.

## BYO external PKI

`--sync-tls-ca/-cert/-key` accept any PEM files — certificates from
cert-manager, Vault, or another external PKI work identically to `nyro tool ca`'s
own output. Server and proxy have no notion of "self-signed vs. external"; it's
just three file paths either way.

## Deployment patterns

### Single node (the default)

One command is a complete, usable nyro — the control plane on
`127.0.0.1:19531` and an embedded data plane on `127.0.0.1:19530`:

```bash
nyro serve --auto-migrate
```

**No config-sync port is opened.** The embedded data plane subscribes over an
in-process pipe, which has no listening socket and cannot be dialled from
outside the process, so none of this document's transport rules apply to it —
it needs no TLS and no token. It is otherwise an ordinary subscriber: it runs
the same config-sync client, cache and router as a remote proxy, and appears in
`/api/v1/nodes` with `conn_mode: inprocess`.

`--auto-migrate` lets this first-boot server create its own (default sqlite)
schema; it's off by default regardless of backend (see
`go/docs/schema/database.md`), so drop it once the db already exists.

### Adding a separate proxy node

To attach data planes running elsewhere, open the config-sync listener
(`--sync-listen`, off by default) and point each proxy at it:

```bash
nyro serve --sync-listen 127.0.0.1:19532 --auto-migrate
nyro proxy --listen 127.0.0.1:19530 --server 127.0.0.1:19532
```

Both ends are on loopback here, so no token is required. Off-host they are —
see "The non-loopback plaintext gate".

Add `--disable-proxy` to the server if that node should not serve traffic
itself — a control-plane-only node. The proxy's config source must always be
selected explicitly (`--server` or `--config`).

### Standalone proxy

A standalone proxy reads YAML once at startup and runs without a server, a
database, or config-sync:

```bash
nyro proxy --config ./config.yaml
```

Edit the file and restart the proxy to apply changes. `--config` and
`--server` are mutually exclusive, exactly one is required, and
`--sync-tls-*` flags are invalid with `--config`. See
[Standalone `config.yaml`](../schema/config.md) for the schema.

### Trusted-network plaintext

Plaintext can cross hosts, but it exposes provider credentials in transit. A
join token is mandatory here — without one both processes refuse to start (see
"The non-loopback plaintext gate") — and it only gates who may subscribe: it
travels in the clear alongside the credentials it protects and can be replayed
by anyone who observes it. Use this only on a tightly controlled network, and
prefer mTLS below.

```bash
export NYRO_SERVE_SYNC_TOKEN=... NYRO_PROXY_SYNC_TOKEN=...   # same value

nyro serve --listen 10.0.0.10:19531 \
  --sync-listen 10.0.0.10:19532 \
  --token "$NYRO_SERVE_TOKEN" --auto-migrate
nyro proxy --server 10.0.0.10:19532
```

Both processes log a warning that the token is unencrypted on the wire.

### Cross-host mTLS

Run the three `nyro tool ca` commands once (from CI or an operator's machine),
distribute `ca.pem` plus the relevant leaf cert/key pair to each host, then
start both processes with complete TLS path sets:

```bash
nyro tool ca init
nyro tool ca sign-server
nyro tool ca sign-proxy --node-id proxy-1

nyro serve --sync-listen 0.0.0.0:19532 --auto-migrate \
  --sync-tls-ca ~/.nyro/pki/ca.pem \
  --sync-tls-cert ~/.nyro/pki/server.pem \
  --sync-tls-key ~/.nyro/pki/server-key.pem
nyro proxy --server server.internal:19532 \
  --sync-tls-ca ~/.nyro/pki/ca.pem \
  --sync-tls-cert ~/.nyro/pki/proxy.pem \
  --sync-tls-key ~/.nyro/pki/proxy-key.pem
```

### Multiple server replicas

A replica pushes its own writes immediately. To also pick up writes handled by
a SIBLING replica it polls the shared `config_epoch`, and whether that is even
possible is already stated by the DSN — only `postgres://` can have a second
writer — so the cadence is derived from the backend rather than configured.
There is no poll-interval flag: on postgres, replicas poll automatically; on
sqlite, polling is skipped because no sibling exists.

Apply the schema with DDL a DBA reviews (print it with `nyro tool migrate
dump`/`diff`, see `go/docs/schema/migrations.md`) before first boot instead of
passing `--auto-migrate` here — this is exactly the shared-database production
case that workflow is for:

```bash
# server-1
nyro serve --listen 10.0.0.11:19531 \
  --sync-listen 10.0.0.11:19532 \
  --dsn "$NYRO_SHARED_DSN" \
  --token "$NYRO_SERVE_TOKEN"

# server-2
nyro serve --listen 10.0.0.12:19531 \
  --sync-listen 10.0.0.12:19532 \
  --dsn "$NYRO_SHARED_DSN" \
  --token "$NYRO_SERVE_TOKEN"
```

Use the same shared PostgreSQL DSN on every replica and add complete
`--sync-tls-*` sets to both replicas and every proxy unless the config-sync
network is deliberately trusted for plaintext. Each replica also runs its own
embedded data plane unless given `--disable-proxy`.

### Config-sync listener disabled (the default)

`--sync-listen` is empty unless set, so no config-sync port is opened. A
standalone proxy driven by a YAML file is therefore fully independent of the
control plane:

```bash
nyro serve --auto-migrate
nyro proxy --config ./config.yaml
```

Control-plane changes do not reach a proxy configured this way. `--sync-tls-*`
flags are rejected when `--sync-listen` is empty.

## HTTP TLS and the optional management token

The server's REST/WebUI `--listen` endpoint and the proxy's client API
`--listen` endpoint serve HTTP. The config-sync `--sync-tls-*` flags do not enable
HTTPS on either endpoint. Terminate public or cross-host HTTPS in a reverse
proxy, ingress, load balancer, or service mesh.

`nyro serve --token <value>` optionally adds Bearer authentication to
`/api/v1` routes; it does not authenticate config-sync. Omitting it is allowed,
but a non-loopback server `--listen` address emits a warning that control-plane
routes are unauthenticated. Use a token for exposed management APIs, and carry it
over deployment-layer HTTPS so the token itself is not sent in cleartext.

## Elastic scaling

**Elastic scaling (containers/k8s):** `ca.pem` is safe to bake into the image
(it's a public certificate, not a secret). `proxy.pem`/`proxy-key.pem`
should **not** — mount them via a Kubernetes Secret (`items`/`subPath` to
rename into whatever path your command line expects), or use cert-manager to
issue a fresh per-pod certificate on scheduling. Either way, the private key
never lands in the image layer.

## Certificate lifetime and rotation

- CA: 10 years by default (`nyro tool ca init --valid`).
- Leaf certificates (`server`/`proxy` identities): 1 year by default (`--valid` on
  `sign-server`/`sign-proxy`).
- Rotation is manual and offline: re-run the relevant `sign-*` command,
  redistribute the new cert/key, restart the process. There is no online
  enrollment or auto-renewal (see "Non-goals" below).
- Because rotation is manual, server and proxy log a startup **and** daily
  warning once a loaded leaf certificate is within 30 days of expiring
  (`pki.ExpiryWarningWindow`). Watch for `"certificate expiring soon"` in
  logs — an expired certificate doesn't crash the process, but subsequent
  mTLS handshakes fail and config-sync stalls until it is renewed.

## Non-goals (by design)

- **No per-node secrets.** `--sync-token` is a shared join credential, not a
  per-node one: there is no issuing, revoking or rotating of individual node
  secrets. Per-node identity is mTLS's job, derived from the client
  certificate's SPIFFE SAN.
- **No online enrollment service.** Provisioning new proxy certs at scale
  is cert-manager's/SPIRE's job, not something nyro re-implements.
- **No CRL/OCSP revocation.** If a certificate needs to be revoked before its
  natural expiry, rotate the CA (`nyro tool ca init --force`) and re-sign
  everything — there's no partial-revocation mechanism. Short leaf TTLs (the
  1-year default, or shorter if you set `--valid` tighter) are the mitigation
  for a lost/compromised leaf key.

A future iteration could add online enrollment (short-lived tokens exchanged
for certs) if fleet size makes offline signing impractical — deliberately out
of scope for now.
