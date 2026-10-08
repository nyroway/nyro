# Rust control plane

[中文](rust-serve_CN.md)

The root `nyro` binary supports three deployments:

| Command | Configuration source | Role |
|---|---|---|
| `nyro proxy --config resources.yaml` | File, read once | Data plane |
| `nyro serve --database control.db --admin-token-file admin.token` | Dedicated SQLite | Control plane |
| `nyro serve --database control.db --admin-token-file admin.token --enable-proxy` | Dedicated SQLite + memory sync | Control and data plane |
| `nyro proxy --server https://control.example.com --sync-token-file sync.token --node-id edge-1` | HTTP full snapshots | Remote data plane |

`serve` does not open a proxy listener unless `--enable-proxy` / `NYRO_ENABLE_PROXY=true` is supplied. There is no `--disable-proxy`. A new database starts with empty valid resources; no seed file or explicit publish step is needed. The old draft/snapshot database and old file structure are incompatible and refused. Use a new dedicated database. Released desktop/server stores are independent and are not migrated.

## Startup

Create an admin token file containing 16–1024 visible ASCII characters. New SQLite files use Unix mode `0600`; existing files with group/other permissions are refused. Protect the directory and backups: upstream and consumer secrets are stored in plaintext JSON.

```sh
nyro serve --database ./control.db --admin-token-file ./admin.token --enable-proxy
```

For PostgreSQL, replace `--database` with `--postgres-url-file /run/secrets/control-postgres-url`. The file contains a PostgreSQL URL for a dedicated UTF-8 database. TLS defaults to certificate/hostname verification; disabling TLS is limited to loopback fixtures. One control process owns the database connection; pooling/reconnection is not performed. See [schema](../database/schema.md).

Startup environment equivalents: `NYRO_DATABASE`, `NYRO_POSTGRES_URL_FILE`, `NYRO_ADMIN_TOKEN_FILE`, `NYRO_ADMIN_LISTEN`, `NYRO_ENABLE_PROXY`, `NYRO_SYNC_LISTEN`, `NYRO_SYNC_TOKEN_FILE`, `NYRO_LISTEN`, `NYRO_CONCURRENCY`.

## Resource API

The admin listener defaults to `127.0.0.1:19531` and must use loopback. Use `Authorization: Bearer <admin-token>`. This token is separate from consumer and synchronization credentials.

| Method and path | Action |
|---|---|
| `GET /v1/resources` | Read all resources with secrets redacted |
| `GET /v1/resources/{kind}` | List one collection |
| `POST /v1/resources/{kind}` | Create a resource |
| `GET /v1/resources/{kind}/{id}` | Read a resource |
| `PUT /v1/resources/{kind}/{id}` | Replace or rename a resource |
| `DELETE /v1/resources/{kind}/{id}` | Delete a resource |
| `GET /v1/nodes/{node-id}` | Read last sent, applied, or rejected sync status |

`kind` is `upstreams`, `models`, `mcps`, or `consumers`. POST/PUT require `Content-Type: application/json` and one complete resource object using the same fields as [YAML resources](rust-proxy.md). A PUT containing a different `id` renames the resource and updates grants/references without changing its UID. Deleting a referenced upstream fails; deleting a model/MCP removes its grants. Renaming MCP changes its `/mcp/{id}` entry.

GET removes `secret` and exposes `has_secret: true`. PUT may omit a secret to retain the corresponding existing target/credential secret; supplying a secret rotates it. New credentials require a secret. Removing credentials or setting target `auth: null` explicitly removes that authentication. Egress proxy URLs are also hidden as `has_proxy_url: true`, since they can contain passwords. In a supplied `egress` object, omitting `proxy_url` retains its existing value, supplying a URL replaces it, and `proxy_url: null` clears it. Omitted collections on a replaced resource use their declared defaults. Environment substitution applies to startup YAML only; API and database resources contain resolved values.

A successful mutation validates the complete candidate, commits a transaction, then distributes a full snapshot. There is no draft, global revision editing workflow, or publish endpoint. An accepted write continues even if the caller disconnects. A storage failure can disable writes until restart; if commit outcome is uncertain, reopen and inspect durable state before retrying.

Saved configuration and active configuration are distinct: a save response does not mean every data plane has activated it. Inspect node feedback and data-plane `/readyz`. Rejected candidates preserve the previous generation and in-flight requests.

## HTTP and memory synchronization

Embedded proxy mode transfers typed snapshots over memory using the same application/acknowledgment loop as remote HTTP. It does not expose a network sync endpoint by default.

For remote nodes, configure a dedicated loopback listener and a distinct token:

```sh
nyro serve --database ./control.db --admin-token-file ./admin.token \
  --sync-listen 127.0.0.1:19532 --sync-token-file ./sync.token
```

Expose **only port 19532 through an HTTPS reverse proxy**, with a timeout longer than 35 seconds. The built-in listener does not terminate TLS and refuses non-loopback bind addresses. Keep port 19531 local; the sync token never grants admin access. Protect responses from caching or logging bodies.

Remote nodes POST `/v1/config/sync`. Each control-process epoch starts a new revision sequence. Content fingerprints suppress duplicate runtime rebuilds. Long polling returns complete snapshots; clients distinguish receipt from successful activation, reconnect with full fetches, back off transient failures, and avoid repeatedly rebuilding a rejected version. The control plane currently supports one owner; no service discovery, distributed counters, gRPC, incremental sync, or offline snapshot cache is required.

## Local verification

Run `cargo test -p nyro-control -p nyro-sync -p nyro`, then `cargo build -p nyro` and `python3 tests/rust_resource_smoke.py`. The process test exercises file, remote HTTP and embedded memory deployments using local fixtures.

PostgreSQL integration requires an empty disposable database, supplied through `NYRO_TEST_CONTROL_POSTGRES_URL`: run `cargo test -p nyro-control --test resources postgres_resources -- --ignored`. The test leaves its resources in that database. It is not run without an explicit database setting.
