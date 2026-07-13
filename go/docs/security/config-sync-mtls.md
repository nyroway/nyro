# Config-sync transport and mTLS

The config-sync gRPC channel is how admin (control plane) pushes the live
config snapshot — including every upstream's `credentials_json` — to every
connected gateway (data plane). Its transport mode is selected only from the
three `--config-tls-*` paths:

- **No TLS paths:** plaintext. Admin warns that the stream carries upstream
  credentials without encryption or client authentication; gateway warns
  that it has no transport encryption or server authentication.
- **All three TLS paths:** mTLS. Admin requires and verifies a gateway client
  certificate, and gateway verifies admin's certificate and identity.

Plaintext needs no separate opt-out flag. Supplying only one or two TLS paths
is a startup error. Supplying all three paths but failing to read or validate
any certificate or key is also a startup error; Nyro never downgrades a
requested mTLS configuration to plaintext. Admin and gateway must be
configured for the same mode or their connection cannot be established.

## The three commands

`nyro ca` is an **offline** certificate authority — it never runs as a
service. Everything it produces is a plain file you distribute yourself (scp,
Ansible, a Secret, a Docker volume, whatever fits your deployment).

```bash
nyro ca init [--dir ~/.nyro/pki] [--valid 87600h] [--force]
nyro ca sign-admin [--dir ~/.nyro/pki] [--valid 8760h] [--out admin]
nyro ca sign-gateway [--dir ~/.nyro/pki] [--node-id <id>] [--valid 8760h] [--out gateway]
```

- `init` creates (or, without `--force`, reuses) the CA: `ca.pem` +
  `ca-key.pem` in `--dir`.
- `sign-admin` issues admin's server certificate, with a **fixed identity**
  encoded as a SPIFFE URI SAN (`spiffe://nyro/admin`) — not a DNS/IP SAN list.
  There is no `--advertise`-style flag: gateway verifies this certificate by
  identity, not by matching a hostname it dialed against a SAN list, so the
  same certificate is valid no matter what address a gateway uses to reach
  admin (direct, load balancer, Kubernetes Service name, IP — see "Why
  identity, not hostname" below).
- `sign-gateway` issues one gateway's client certificate, with its identity
  encoded as a SPIFFE URI SAN (`spiffe://nyro/gateway/<node-id>`). Run once
  per gateway node (or once per shared cert if you're intentionally pooling
  identity across a fleet — see "elastic scaling" below). `--node-id`
  defaults to a random value if omitted.

All three write fixed filenames — `<out>.pem` / `<out>-key.pem` — into
`--dir`. That directory is purely `nyro ca`'s own bookkeeping; admin/gateway
never read it directly (see below).

## Runtime: explicit paths only, no directory auto-discovery

`admin` and `gateway` do **not** know about `nyro ca`'s output directory.
They only ever load three explicit file paths:

```bash
nyro admin --config-tls-ca ~/.nyro/pki/ca.pem \
           --config-tls-cert ~/.nyro/pki/admin.pem \
           --config-tls-key ~/.nyro/pki/admin-key.pem

nyro gateway --config-server 127.0.0.1:19532 \
             --config-tls-ca ~/.nyro/pki/ca.pem \
             --config-tls-cert ~/.nyro/pki/gateway.pem \
             --config-tls-key ~/.nyro/pki/gateway-key.pem
```

All three flags must be given together, or not at all — a partial set (e.g.
just `--config-tls-cert`) is rejected at startup rather than silently
guessing at a directory convention for the missing pieces or falling back to
plaintext. This keeps the loading logic to a single path with no precedence
rules to reason about, and it means a certificate and its CA can never be
silently mismatched from two different sources.

The two lifecycles — `nyro ca`'s offline, one-shot signing and admin/gateway's
long-running runtime load — are deliberately decoupled. In practice you write
the three paths once into whatever starts the process (systemd unit,
docker-compose, Helm values), not on every interactive invocation.

### Why identity, not hostname

gateway's config-sync client verifies admin's server certificate by SPIFFE
identity (`spiffe://nyro/admin`), not by matching a hostname/IP SAN against
the address in `--config-server` — the classic web-PKI model most CLI tools
default to. That classic model needs a `--config-server-name`-style escape
hatch the moment the dial address and the cert's SAN diverge (a load
balancer, a Kubernetes Service name, an IP dialed against a DNS-only cert),
and that escape hatch is itself a footgun: it's easy to reach for as "make
the error go away" and easy to leave silently pointed at the wrong thing
after a rename.

Identity-based verification sidesteps the whole problem: `--config-server`
can be a direct address, a load balancer, or a Kubernetes Service name — none
of it matters, because the check is "does this certificate say
`spiffe://nyro/admin`", not "does this certificate's SAN match the string I
dialed". There is deliberately no override flag for this on the gateway side.

## BYO external PKI

`--config-tls-ca/-cert/-key` accept any PEM files — certificates from
cert-manager, Vault, or another external PKI work identically to `nyro ca`'s
own output. admin/gateway have no notion of "self-signed vs. external"; it's
just three file paths either way.

## Deployment patterns

### Local Admin and gateway

For same-host development or shadow testing, keep both listeners on loopback
and omit all TLS paths. Both processes log the expected plaintext warning.

```bash
nyro admin
nyro gateway --listen 127.0.0.1:19530 \
  --config-server 127.0.0.1:19532
```

Admin defaults to HTTP on `127.0.0.1:19531` and config-sync on
`127.0.0.1:19532`; gateway's config source must still be selected explicitly.

### Standalone gateway

A standalone gateway reads YAML once at startup and runs without Admin, a
database, or config-sync:

```bash
nyro gateway --config-file ./config.yaml
```

Edit the file and restart the gateway to apply changes. `--config-file` and
`--config-server` are mutually exclusive, exactly one is required, and
`--config-tls-*` flags are invalid with `--config-file`. See
[Standalone `config.yaml`](../schema/config.md) for the schema.

### Trusted-network plaintext

Plaintext can cross hosts, but it exposes provider credentials in transit and
does not authenticate the Admin server or gateway clients. Use it only on a
tightly controlled trusted network:

```bash
nyro admin --listen 10.0.0.10:19531 \
  --config-listen 10.0.0.10:19532 \
  --token "$NYRO_ADMIN_TOKEN"
nyro gateway --config-server 10.0.0.10:19532
```

The plaintext warnings are unconditional; a private or loopback address does
not suppress them.

### Cross-host mTLS

Run the three `nyro ca` commands once (from CI or an operator's machine),
distribute `ca.pem` plus the relevant leaf cert/key pair to each host, then
start both processes with complete TLS path sets:

```bash
nyro ca init
nyro ca sign-admin
nyro ca sign-gateway --node-id gw-1

nyro admin --config-listen 0.0.0.0:19532 \
  --config-tls-ca ~/.nyro/pki/ca.pem \
  --config-tls-cert ~/.nyro/pki/admin.pem \
  --config-tls-key ~/.nyro/pki/admin-key.pem
nyro gateway --config-server admin.internal:19532 \
  --config-tls-ca ~/.nyro/pki/ca.pem \
  --config-tls-cert ~/.nyro/pki/gateway.pem \
  --config-tls-key ~/.nyro/pki/gateway-key.pem
```

### Multiple Admin replicas

Admin's `--config-poll-interval` defaults to `0`, so a single replica pushes
its own writes immediately without polling. Replicas that share a database
must each opt into a positive polling interval so writes handled by one Admin
are also pushed to gateways connected to the others:

```bash
# admin-1
nyro admin --listen 10.0.0.11:19531 \
  --config-listen 10.0.0.11:19532 \
  --dsn "$NYRO_SHARED_DSN" --config-poll-interval 1s \
  --token "$NYRO_ADMIN_TOKEN"

# admin-2
nyro admin --listen 10.0.0.12:19531 \
  --config-listen 10.0.0.12:19532 \
  --dsn "$NYRO_SHARED_DSN" --config-poll-interval 1s \
  --token "$NYRO_ADMIN_TOKEN"
```

Use the same shared DSN on every replica (typically PostgreSQL or MySQL) and
add complete `--config-tls-*` sets to both replicas and every gateway unless
the config-sync network is deliberately trusted for plaintext. Polling is per
Admin process; it is not needed by the gateways.

### Config-sync disabled

Set the Admin listener to the empty string when the deployment needs the
management API but no config-sync server:

```bash
nyro admin --config-listen=
nyro gateway --config-file ./config.yaml
```

With config-sync disabled, Admin changes do not update the standalone gateway.
Explicit `--config-poll-interval` or `--config-tls-*` flags are rejected when
`--config-listen` is empty.

## HTTP TLS and the optional Admin token

Admin's REST/WebUI `--listen` endpoint and gateway's client API `--listen`
endpoint serve HTTP. The config-sync `--config-tls-*` flags do not enable
HTTPS on either endpoint. Terminate public or cross-host HTTPS in a reverse
proxy, ingress, load balancer, or service mesh.

`nyro admin --token <value>` optionally adds Bearer authentication to
`/api/v1` routes; it does not authenticate config-sync. Omitting it is allowed,
but a non-loopback Admin `--listen` address emits a warning that control-plane
routes are unauthenticated. Use a token for exposed Admin APIs, and carry it
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
- Leaf certificates (admin/gateway): 1 year by default (`--valid` on
  `sign-admin`/`sign-gateway`).
- Rotation is manual and offline: re-run the relevant `sign-*` command,
  redistribute the new cert/key, restart the process. There is no online
  enrollment or auto-renewal (see "Non-goals" below).
- Because rotation is manual, admin/gateway log a startup **and** daily
  warning once a loaded leaf certificate is within 30 days of expiring
  (`pki.ExpiryWarningWindow`). Watch for `"certificate expiring soon"` in
  logs — an expired certificate doesn't crash the process, but subsequent
  mTLS handshakes fail and config-sync stalls until it is renewed.

## Non-goals (by design)

- **No bearer token / API key on this channel.** Node identity comes from
  the client certificate's SPIFFE SAN, not a shared secret.
- **No online enrollment service.** Provisioning new gateway certs at scale
  is cert-manager's/SPIRE's job, not something nyro re-implements.
- **No CRL/OCSP revocation.** If a certificate needs to be revoked before its
  natural expiry, rotate the CA (`nyro ca init --force`) and re-sign
  everything — there's no partial-revocation mechanism. Short leaf TTLs (the
  1-year default, or shorter if you set `--valid` tighter) are the mitigation
  for a lost/compromised leaf key.

A future iteration could add online enrollment (short-lived tokens exchanged
for certs) if fleet size makes offline signing impractical — deliberately out
of scope for now.
