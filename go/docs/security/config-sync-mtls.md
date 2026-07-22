# Config-sync transport and mTLS

The config-sync gRPC channel is how `nyro server` (control plane) pushes the
live config snapshot — including every upstream's `credentials_json` — to every
connected `nyro proxy` (data plane). Its transport mode is selected only from
the three `--sync-tls-*` paths:

- **No TLS paths:** plaintext. The server warns that the stream carries
  upstream credentials without encryption or client authentication; the proxy
  warns that it has no transport encryption or server authentication.
- **All three TLS paths:** mTLS. The server requires and verifies a proxy
  client certificate, and the proxy verifies the server's certificate and
  identity.

Plaintext needs no separate opt-out flag. Supplying only one or two TLS paths
is a startup error. Supplying all three paths but failing to read or validate
any certificate or key is also a startup error; Nyro never downgrades a
requested mTLS configuration to plaintext. Server and proxy must be
configured for the same mode or their connection cannot be established.

> **Naming note.** The subcommands are `server` / `proxy`, but the PKI layer
> still uses the historical identifiers `admin` / `gateway`: the SPIFFE SANs
> (`spiffe://nyro/admin`, `spiffe://nyro/gateway/<node-id>`) and the default
> certificate basenames (`admin.pem`, `gateway.pem`). Those are a
> compatibility surface — renaming them would invalidate every certificate
> already issued — so they were deliberately left alone when the CLI was
> renamed. Read `admin` as "the control plane's identity" and `gateway` as
> "a data plane node's identity" throughout this document.

## The three commands

`nyro ca` is an **offline** certificate authority — it never runs as a
service. Everything it produces is a plain file you distribute yourself (scp,
Ansible, a Secret, a Docker volume, whatever fits your deployment).

```bash
nyro ca init [--dir ~/.nyro/pki] [--valid 87600h] [--force]
nyro ca sign-server [--dir ~/.nyro/pki] [--valid 8760h] [--out admin]
nyro ca sign-proxy [--dir ~/.nyro/pki] [--node-id <id>] [--valid 8760h] [--out gateway]
```

- `init` creates (or, without `--force`, reuses) the CA: `ca.pem` +
  `ca-key.pem` in `--dir`.
- `sign-server` issues the server's config-sync certificate, with a **fixed
  identity** encoded as a SPIFFE URI SAN (`spiffe://nyro/admin`) — not a DNS/IP
  SAN list. There is no `--advertise`-style flag: the proxy verifies this
  certificate by identity, not by matching a hostname it dialed against a SAN
  list, so the same certificate is valid no matter what address a proxy uses
  to reach the server (direct, load balancer, Kubernetes Service name, IP —
  see "Why identity, not hostname" below).
- `sign-proxy` issues one proxy node's client certificate, with its identity
  encoded as a SPIFFE URI SAN (`spiffe://nyro/gateway/<node-id>`). Run once
  per proxy node (or once per shared cert if you're intentionally pooling
  identity across a fleet — see "elastic scaling" below). `--node-id`
  defaults to a random value if omitted.

All three write fixed filenames — `<out>.pem` / `<out>-key.pem` — into
`--dir`. That directory is purely `nyro ca`'s own bookkeeping; server and
proxy never read it directly (see below).

## Runtime: explicit paths only, no directory auto-discovery

`server` and `proxy` do **not** know about `nyro ca`'s output directory.
They only ever load three explicit file paths:

```bash
nyro server --sync-tls-ca ~/.nyro/pki/ca.pem \
            --sync-tls-cert ~/.nyro/pki/admin.pem \
            --sync-tls-key ~/.nyro/pki/admin-key.pem

nyro proxy --server 127.0.0.1:19532 \
           --sync-tls-ca ~/.nyro/pki/ca.pem \
           --sync-tls-cert ~/.nyro/pki/gateway.pem \
           --sync-tls-key ~/.nyro/pki/gateway-key.pem
```

All three flags must be given together, or not at all — a partial set (e.g.
just `--sync-tls-cert`) is rejected at startup rather than silently
guessing at a directory convention for the missing pieces or falling back to
plaintext. This keeps the loading logic to a single path with no precedence
rules to reason about, and it means a certificate and its CA can never be
silently mismatched from two different sources.

The two lifecycles — `nyro ca`'s offline, one-shot signing and the server/proxy's
long-running runtime load — are deliberately decoupled. In practice you write
the three paths once into whatever starts the process (systemd unit,
docker-compose, Helm values), not on every interactive invocation.

### Why identity, not hostname

The proxy's config-sync client verifies the server's certificate by SPIFFE
identity (`spiffe://nyro/admin`), not by matching a hostname/IP SAN against
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
`spiffe://nyro/admin`", not "does this certificate's SAN match the string I
dialed". There is deliberately no override flag for this on the proxy side.

## BYO external PKI

`--sync-tls-ca/-cert/-key` accept any PEM files — certificates from
cert-manager, Vault, or another external PKI work identically to `nyro ca`'s
own output. Server and proxy have no notion of "self-signed vs. external"; it's
just three file paths either way.

## Deployment patterns

### Local server and proxy

For same-host development or shadow testing, keep both listeners on loopback
and omit all TLS paths. Both processes log the expected plaintext warning.

```bash
nyro server --auto-migrate
nyro proxy --listen 127.0.0.1:19530 \
  --server 127.0.0.1:19532
```

The server defaults to HTTP on `127.0.0.1:19531` and config-sync on
`127.0.0.1:19532`; the proxy's config source must still be selected explicitly.
`--auto-migrate` lets this first-boot server create its own (default sqlite)
schema; it's off by default regardless of backend (see
`go/docs/schema/database.md`), so drop it once the db already exists.

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

Plaintext can cross hosts, but it exposes provider credentials in transit and
does not authenticate the server or its proxy clients. Use it only on a
tightly controlled trusted network:

```bash
nyro server --listen 10.0.0.10:19531 \
  --sync-listen 10.0.0.10:19532 \
  --token "$NYRO_SERVER_TOKEN" --auto-migrate
nyro proxy --server 10.0.0.10:19532
```

The plaintext warnings are unconditional; a private or loopback address does
not suppress them.

### Cross-host mTLS

Run the three `nyro ca` commands once (from CI or an operator's machine),
distribute `ca.pem` plus the relevant leaf cert/key pair to each host, then
start both processes with complete TLS path sets:

```bash
nyro ca init
nyro ca sign-server
nyro ca sign-proxy --node-id gw-1

nyro server --sync-listen 0.0.0.0:19532 --auto-migrate \
  --sync-tls-ca ~/.nyro/pki/ca.pem \
  --sync-tls-cert ~/.nyro/pki/admin.pem \
  --sync-tls-key ~/.nyro/pki/admin-key.pem
nyro proxy --server server.internal:19532 \
  --sync-tls-ca ~/.nyro/pki/ca.pem \
  --sync-tls-cert ~/.nyro/pki/gateway.pem \
  --sync-tls-key ~/.nyro/pki/gateway-key.pem
```

### Multiple server replicas

The server's `--config-poll-interval` defaults to `0`, so a single replica
pushes its own writes immediately without polling. Replicas that share a
database must each opt into a positive polling interval so writes handled by
one server are also pushed to proxies connected to the others. Apply the schema with DDL a
DBA reviews (print it with `nyro migrate dump`/`diff`, see
`go/docs/schema/migrations.md`) before first boot instead of passing
`--auto-migrate` here — this is exactly the shared-database production case
that workflow is for:

```bash
# server-1
nyro server --listen 10.0.0.11:19531 \
  --sync-listen 10.0.0.11:19532 \
  --dsn "$NYRO_SHARED_DSN" --config-poll-interval 1s \
  --token "$NYRO_SERVER_TOKEN"

# server-2
nyro server --listen 10.0.0.12:19531 \
  --sync-listen 10.0.0.12:19532 \
  --dsn "$NYRO_SHARED_DSN" --config-poll-interval 1s \
  --token "$NYRO_SERVER_TOKEN"
```

Use the same shared DSN on every replica (PostgreSQL) and add complete
`--sync-tls-*` sets to both replicas and every proxy unless the config-sync
network is deliberately trusted for plaintext. Polling is per server process;
it is not needed by the proxies.

### Config-sync disabled

Set the config-sync listener to the empty string when the deployment needs
the management API but no config-sync server:

```bash
nyro server --sync-listen= --auto-migrate
nyro proxy --config ./config.yaml
```

With config-sync disabled, control-plane changes do not update the standalone
proxy.
Explicit `--config-poll-interval` or `--sync-tls-*` flags are rejected when
`--sync-listen` is empty.

## HTTP TLS and the optional management token

The server's REST/WebUI `--listen` endpoint and the proxy's client API
`--listen` endpoint serve HTTP. The config-sync `--sync-tls-*` flags do not enable
HTTPS on either endpoint. Terminate public or cross-host HTTPS in a reverse
proxy, ingress, load balancer, or service mesh.

`nyro server --token <value>` optionally adds Bearer authentication to
`/api/v1` routes; it does not authenticate config-sync. Omitting it is allowed,
but a non-loopback server `--listen` address emits a warning that control-plane
routes are unauthenticated. Use a token for exposed management APIs, and carry it
over deployment-layer HTTPS so the token itself is not sent in cleartext.

## Elastic scaling

**Elastic scaling (containers/k8s):** `ca.pem` is safe to bake into the image
(it's a public certificate, not a secret). `gateway.pem`/`gateway-key.pem`
should **not** — mount them via a Kubernetes Secret (`items`/`subPath` to
rename into whatever path your command line expects), or use cert-manager to
issue a fresh per-pod certificate on scheduling. Either way, the private key
never lands in the image layer.

## Certificate lifetime and rotation

- CA: 10 years by default (`nyro ca init --valid`).
- Leaf certificates (`admin`/`gateway` identities): 1 year by default (`--valid` on
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

- **No bearer token / API key on this channel.** Node identity comes from
  the client certificate's SPIFFE SAN, not a shared secret.
- **No online enrollment service.** Provisioning new proxy certs at scale
  is cert-manager's/SPIRE's job, not something nyro re-implements.
- **No CRL/OCSP revocation.** If a certificate needs to be revoked before its
  natural expiry, rotate the CA (`nyro ca init --force`) and re-sign
  everything — there's no partial-revocation mechanism. Short leaf TTLs (the
  1-year default, or shorter if you set `--valid` tighter) are the mitigation
  for a lost/compromised leaf key.

A future iteration could add online enrollment (short-lived tokens exchanged
for certs) if fleet size makes offline signing impractical — deliberately out
of scope for now.
