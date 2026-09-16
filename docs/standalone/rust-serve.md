# Experimental Rust SQLite server

[中文](rust-serve_CN.md)

The source-built root `nyro serve` adds a local SQLite configuration control plane to the [experimental Rust proxy](rust-proxy.md). It uses the same LLM runtime and configuration format. This is the first part of G10: whole-configuration drafts and explicit publication. Entity CRUD, WebUI, OAuth management, PostgreSQL, legacy data import, persistent usage budgets and multiple server processes remain outside this increment. Released `nyro-server` and desktop entries continue to use their existing databases.

## Start and restart

Copy [rust-proxy.yaml](rust-proxy.yaml) to `nyro.yaml` and set your provider endpoints, models and credentials. Create a private token file and initialize a dedicated database:

```sh
umask 077
openssl rand -hex 32 > admin.token
cargo run -p nyro -- serve \
  --database control.sqlite \
  --config nyro.yaml \
  --admin-token-file admin.token
```

`--database` and `--admin-token-file` are required. The admin token must have 16–1024 visible ASCII characters; trailing CR/LF is stripped and the entire file is limited to 1024 bytes. It is separate from `security.api_keys`, which authenticate data-plane clients. The admin listener defaults to `127.0.0.1:19531`; `--admin-listen` accepts only a loopback IP address. The data listener comes from `server.listen` in the published configuration (default `127.0.0.1:19530`); use distinct addresses.

`--config` seeds an empty dedicated database with both draft and published revision `1`. An initialized database rejects `--config`; omit it on restart:

```sh
cargo run -p nyro -- serve \
  --database control.sqlite \
  --admin-token-file admin.token
```

Startup validates and loads the durable **published** snapshot. An unpublished draft survives restart but does not become active. Editing the seed YAML does not reimport it. SIGHUP reload is unsupported in `serve`; the default Unix signal action can terminate the process. Use the admin API to update configuration.

Only one process may own the SQLite file. The store holds an exclusive connection and rejects legacy, foreign, unsupported or corrupt databases. There is no implicit conversion or import. New database files use mode `0600` on Unix; existing Unix files with any group/other permission bits are rejected, without silently changing their permissions. Snapshots contain plaintext provider, proxy and client credentials: protect the directory, token file, database, backups and exported JSON. The admin token itself is read from its file, not stored in the configuration database.

## Read, save and publish

Every supported admin route requires exactly one `Authorization: Bearer TOKEN` header. Data-plane keys do not grant admin access; `x-api-key`, `x-goog-api-key` and query parameters are rejected. Responses use `Cache-Control: no-store`. **Authenticated `GET /admin/config` returns the full draft, including plaintext credentials**, so treat its output as a secret.

| Request | Result |
|---|---|
| `GET /admin/config` | `200`: `{ "draft": { "revision": 1, "config": CONFIG }, "published_revision": 1, "active_revision": 1, "publication": "active" }` |
| `PUT /admin/config` with `{ "expected_revision": 1, "config": CONFIG }` | `200`: `{ "draft_revision": 2 }` after saving, or `202` with `operation: "pending"` while still running; never publishes |
| `POST /admin/config/publish` with `{ "revision": 2 }` | `200` when active or `202` when pending; completed publication returns `published_revision`, `active_revision` and `publication` |

`CONFIG` above denotes the complete JSON configuration object, using the same fields as the YAML seed. Drafts must pass full configuration validation; they are not partially complete editing buffers. Request bodies are limited to 1 MiB, independently of the data-plane body limit. Unknown fields and malformed or wrongly typed JSON are rejected. Each accepted save increments the draft revision, including an equivalent configuration. `expected_revision` must match the current draft; publication also requires the current draft revision. A stale revision returns `409`.

For example, use curl and jq to fetch a private draft, edit its configuration, then submit it while preserving the fetched revision:

```sh
umask 077
export NYRO_ADMIN_TOKEN="$(cat admin.token)"
curl --fail-with-body http://127.0.0.1:19531/admin/config \
  -H "Authorization: Bearer $NYRO_ADMIN_TOKEN" > draft.json
# Edit draft.json's draft.config object before the next command.
jq '{expected_revision: .draft.revision, config: .draft.config}' draft.json > save.json
curl --fail-with-body -X PUT http://127.0.0.1:19531/admin/config \
  -H "Authorization: Bearer $NYRO_ADMIN_TOKEN" \
  -H 'Content-Type: application/json' --data-binary @save.json > saved.json
jq '{revision: .draft_revision}' saved.json > publish.json
curl --fail-with-body -X POST http://127.0.0.1:19531/admin/config/publish \
  -H "Authorization: Bearer $NYRO_ADMIN_TOKEN" \
  -H 'Content-Type: application/json' --data-binary @publish.json
unset NYRO_ADMIN_TOKEN
```

Check each response before proceeding. After a conflict, fetch again and reconcile your edit with the current draft.

## Publication and recovery

The server checks process settings and builds a runtime candidate before committing publication. Precommit rejection leaves the published restart target and active generation unchanged; a valid saved draft remains available for correction. SQLite commit is the durable publication boundary. Host activation follows it; these two steps are **not one atomic transaction**.

A successful activation returns `200` with `publication: "active"`. If activation is interrupted after the durable commit, the result is `202` with `publication: "pending"`; the published revision is the restart target even while `active_revision` remains older. Read the state with GET, then retry publication of the current revision or restart to recover that published target. This is not a background retry service. The data-plane `/readyz` reports Host lease availability, not database health or whether published and active revisions agree.

Request body reading is limited to 15 seconds (`408 request_timeout` before acceptance). After a valid request enters the owned queue, the HTTP handler waits up to 10 seconds. A still-running save or publish returns `202 { "operation": "pending" }`; it can later succeed, conflict or fail. This differs from `publication: "pending"`, which confirms durable publication already committed. Accepted work continues after client disconnect or the handler stops waiting: use GET to inspect the draft contents and draft/published/active revisions before retrying, rather than blindly resubmitting a write. A GET that cannot finish within 10 seconds returns `503 control_busy`. Operations are serialized, with at most 16 admitted operations; excess requests receive `503`. Shutdown stops admission and waits for owned work before Host cleanup; work still queued when shutdown begins can return `control_stopping`.

Publishing an already active revision, or an equivalent configuration fingerprint, does not create another runtime generation. New requests use the activated snapshot; existing leases and SSE streams retain their original generation. Shared in-memory rate, quota, subject windows, health and routing history retain their existing identity and update rules; storing configuration does not make counters durable. A process restart clears them.

Changing `server.listen` or `limit.concurrency` returns `409 restart_required` before publication. Simply restarting this database still loads the previous published values: this increment has no offline publication path for those changes. Use a separately initialized database with a revised seed when a new process configuration is needed. Active or retained rate/quota/window policy changes can likewise reject candidate construction; see the [proxy guide](rust-proxy.md) for those shared-resource constraints.

| Status | Error code / meaning |
|---|---|
| `400` | `query_not_supported`, or `invalid_request` for malformed JSON |
| `401` | `authentication_failed` |
| `408` | `request_timeout`: body read timed out before operation acceptance |
| `409` | `revision_conflict` or `restart_required` |
| `413` / `415` | `invalid_request`: body too large / unsupported content type |
| `422` | `invalid_config`, `candidate_rejected`, or `invalid_request` for a JSON shape/type mismatch |
| `500` | `control_failed` |
| `503` | `storage_failed`, `control_busy` or `control_stopping` |

Errors use `{ "error": { "code": "..." } }` without credentials or database details. A postcommit activation problem uses the successful `202` pending shape above, not a rejected-edit error.

## Local regression

```sh
cargo test -p nyro-control -p nyro --offline
cargo build -p nyro --offline
python3 tests/serve_smoke.py
```

The [process regression](../../tests/serve_smoke.py) uses temporary SQLite files and loopback mocks to exercise authentication, draft/publication separation, conflicts, rejected publication, SSE continuity and published-snapshot restart recovery. The separate [proxy reload regression](../../tests/proxy_reload_smoke.py) covers file-mode SIGHUP behavior.
