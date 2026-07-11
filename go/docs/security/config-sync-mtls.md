# config-sync mTLS

The config-sync gRPC channel is how admin (control plane) pushes the live
config snapshot — including every upstream's `credentials_json` — to every
connected gateway (data plane). It always requires one of:

- **mTLS** (`--config-tls-ca/-cert/-key`, all three together), or
- **`--config-insecure`**, an explicit, unconditional opt-out of transport
  security. There is no address-based exemption (not even loopback) — the
  flag alone decides, matching CockroachDB's `--insecure`/`--certs-dir`
  model. Passing it always logs a startup warning.

There is no third state and no implicit default: a config-sync listener/dial
target with no certs and no `--config-insecure` refuses to start.

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
admin --config-tls-ca ~/.nyro/pki/ca.pem \
      --config-tls-cert ~/.nyro/pki/admin.pem \
      --config-tls-key ~/.nyro/pki/admin-key.pem

gateway --config-tls-ca ~/.nyro/pki/ca.pem \
        --config-tls-cert ~/.nyro/pki/gateway.pem \
        --config-tls-key ~/.nyro/pki/gateway-key.pem
```

All three flags must be given together, or not at all — a partial set (e.g.
just `--config-tls-cert`) is rejected at startup rather than silently
guessing at a directory convention for the missing pieces. This keeps the
loading logic to a single path with no precedence rules to reason about, and
it means a certificate and its CA can never be silently mismatched from two
different sources.

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

## Four scenarios

**Same host** (shadow testing, local dev): skip PKI entirely.

```bash
admin --config-listen 127.0.0.1:19532 --config-insecure
gateway --config-server 127.0.0.1:19532 --config-insecure
```

**Cross-host, self-signed**: run the three `nyro ca` commands once (from CI or
an operator's machine), distribute `ca.pem` + the relevant leaf cert/key pair
to each host, then start both processes with the three `--config-tls-*` flags
pointed at the distributed files.

**BYO external PKI**: point `--config-tls-ca/-cert/-key` at cert-manager- or
Vault-issued files directly.

**Elastic scaling (containers/k8s)**: `ca.pem` is safe to bake into the image
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
  logs — an expired certificate doesn't crash the process, it just makes the
  mTLS handshake fail, silently stalling config-sync until it's renewed.

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
