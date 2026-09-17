# Experimental Rust configuration server

[中文](rust-serve_CN.md)

The source-built root `nyro serve` adds a SQLite or PostgreSQL configuration control plane to the [experimental Rust proxy](rust-proxy.md). It uses the same LLM runtime and configuration format. G10 currently includes whole-configuration drafts, Provider/Model/API Key CRUD and explicit publication on either backend. WebUI, OAuth management, legacy data import, persistent usage budgets and multiple server processes remain outside this increment. Released `nyro-server` and desktop entries continue to use their existing databases.

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

Choose exactly one of `--database PATH` (SQLite) or `--postgres-url-file PATH` (PostgreSQL); they are mutually exclusive. `--admin-token-file` is required with either backend. The admin token must have 16–1024 visible ASCII characters; trailing CR/LF is stripped and the entire file is limited to 1024 bytes. It is separate from `security.api_keys`, which authenticate data-plane clients. The admin listener defaults to `127.0.0.1:19531`; `--admin-listen` accepts only a loopback IP address. The data listener comes from `server.listen` in the published configuration (default `127.0.0.1:19530`); use distinct addresses.

`--config` seeds an empty dedicated database with both draft and published revision `1`. An initialized database rejects `--config`; omit it on restart:

```sh
cargo run -p nyro -- serve \
  --database control.sqlite \
  --admin-token-file admin.token
```

Startup validates and loads the durable **published** snapshot. An unpublished draft survives restart but does not become active. Editing the seed YAML does not reimport it. SIGHUP reload is unsupported in `serve`; the default Unix signal action can terminate the process. Use the admin API to update configuration.

Only one process may own the SQLite file. The store holds an exclusive connection and rejects legacy, foreign, unsupported or corrupt databases. There is no implicit conversion or import. New database files use mode `0600` on Unix; existing Unix files with any group/other permission bits are rejected, without silently changing their permissions. Snapshots contain plaintext provider, proxy and client credentials: protect the directory, token file, database, backups and exported JSON. The admin token itself is read from its file, not stored in the configuration database.

## PostgreSQL setup

Provision a dedicated empty PostgreSQL database with UTF8 encoding and a role that can create and use its control table. Save the connection URL in a private regular file such as `postgres.url`; pass its path, not the URL, on the command line. The file must contain one UTF-8 URL, is limited to 16 KiB including trailing CR/LF, and has those trailing line endings stripped. Protect this file like the admin token; it can contain a database password.

```sh
cargo run -p nyro -- serve \
  --postgres-url-file postgres.url \
  --config nyro.yaml \
  --admin-token-file admin.token
```

TLS defaults to `verify-full`. An explicit `sslmode` may select `require`, `verify-ca` or `verify-full`; `require` does not provide the same certificate/hostname verification as `verify-full`. `sslmode=disable` is accepted only for `localhost` or a literal loopback IP, for local testing. Opportunistic `prefer`/`allow` modes are rejected. The URL must name a database and use TCP; Unix sockets and non-TLS query options are rejected. Use TLS certificate options (`sslrootcert`, `sslcert`, `sslkey`) when needed.

On restart, use the same command without `--config`. The draft, publication, entity CRUD and redaction contracts below are the same for SQLite and PostgreSQL. PostgreSQL stores the snapshots in `public.nyro_control_state`; this is a dedicated database, not a schema to add to an existing application or legacy Nyro database. There is no automatic SQLite/PostgreSQL conversion. See the [schema reference](../database/schema.md) and generated [control-postgres.sql](../../deploy/schema/control-postgres.sql), distinct from the legacy `postgres.sql`. The generated SQL is a reference, not an initialization step: do not preload it. Nyro creates the table and seed atomically in an empty database; a precreated empty table is rejected.

The store retains one connection and a session advisory lock for its entire lifetime. A second server using the same database is rejected. Connect directly to PostgreSQL; transaction/statement pooling, automatic reconnect and multiple replicas are unsupported. Connection establishment and each storage operation have a 5-second deadline; PostgreSQL statements have a 3-second timeout and lock waits a 1-second timeout. Losing the connection or exceeding a storage operation deadline disables further control-store operations until restart. The active data-plane generation continues serving; `/readyz` still reflects Host availability, not database health.

Snapshots and database backups contain plaintext credentials on both backends. Restrict database access and protect backups as secrets. The advisory lock coordinates Nyro instances; it does not prevent another database client from editing tables. Do not modify the table while the server is running.

## Read, save and publish

Every supported admin route requires exactly one `Authorization: Bearer TOKEN` header. Data-plane keys do not grant admin access; `x-api-key`, `x-goog-api-key` and query parameters are rejected. Responses use `Cache-Control: no-store`. `GET /admin/config` and entity reads return credential-free views. **Authenticated `GET /admin/config/export` returns the full draft, including plaintext credentials**; it uses the same admin authentication and `no-store` policy. Protect its output as a secret.

| Request | Result |
|---|---|
| `GET /admin/config` | `200`: `{ "draft": { "revision": 1, "config": VIEW }, "published_revision": 1, "active_revision": 1, "publication": "active" }` |
| `GET /admin/config/export` | Same envelope, with the complete raw `CONFIG` in `draft.config` |
| `PUT /admin/config` with `{ "expected_revision": 1, "config": CONFIG }` | `200`: `{ "draft_revision": 2 }` after saving, or `202` with `operation: "pending"` while still running; never publishes |
| `POST /admin/config/publish` with `{ "revision": 2 }` | `200` when active or `202` when pending; completed publication returns `published_revision`, `active_revision` and `publication` |

`VIEW` replaces provider `api_key`, the entire `transport.proxy_url`, and client `secret` with `has_api_key`, `has_proxy_url`, and `has_secret` booleans. It contains no masked secret strings. This read projection is not a write payload: the `has_*` fields are rejected as unknown fields. Frontends must construct entity write DTOs explicitly, or use the sensitive export for complete-configuration editing.

`CONFIG` above denotes the complete JSON configuration object, using the same fields as the YAML seed. Drafts must pass full configuration validation; they are not partially complete editing buffers. Request bodies are limited to 1 MiB, independently of the data-plane body limit. Unknown fields and malformed or wrongly typed JSON are rejected. Each accepted save increments the draft revision, including an equivalent configuration. `expected_revision` must match the current draft; publication also requires the current draft revision. A stale revision returns `409`.

For example, use curl and jq to fetch a private draft, edit its configuration, then submit it while preserving the fetched revision:

```sh
umask 077
export NYRO_ADMIN_TOKEN="$(cat admin.token)"
curl --fail-with-body http://127.0.0.1:19531/admin/config/export \
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

## Entity drafts

The collections are `/admin/providers`, `/admin/models` and `/admin/api-keys`. Each uses the same contract; replace `COLLECTION` and URL-encode the complete ID as one path segment, including `/` as `%2F` (for example, `team/model` becomes `team%2Fmodel`). IDs are stable; there is no rename operation.

| Request | Completed result |
|---|---|
| `GET /admin/COLLECTION` | `200`: `{ "draft_revision": 2, "items": [{ "id": "example", "value": VIEW }] }`, sorted by ID |
| `GET /admin/COLLECTION/ID` | `200`: `{ "draft_revision": 2, "item": { "id": "example", "value": VIEW } }` |
| `POST /admin/COLLECTION` with `{ "expected_revision": 2, "id": "example", "value": INPUT }` | `201`: `{ "draft_revision": 3 }` |
| `PUT /admin/COLLECTION/ID` with `{ "expected_revision": 2, "value": INPUT }` | `200`: `{ "draft_revision": 3 }` |
| `DELETE /admin/COLLECTION/ID` with `{ "expected_revision": 2 }` | `200`: `{ "draft_revision": 3 }` |

All edits share the same global draft revision with full-configuration PUT, validate the resulting complete configuration and save without publishing. Validation and revision-conflict rejections preserve the draft and published state; a PostgreSQL storage failure can have an unknown write outcome, as described below. Entity operations reuse the existing authentication, body limits, owned queue and timeout behavior; a write still running after 10 seconds returns `202 { "operation": "pending" }`. Publish the final draft explicitly with `/admin/config/publish`.

`INPUT` replaces all metadata, applying defaults to omitted metadata; it is not a partial patch. Only credentials preserve their previous values when omitted. Credential fields accept these tagged objects:

| Credential input | Meaning |
|---|---|
| Omitted, or `{ "action": "keep" }` | Preserve the existing credential |
| `{ "action": "set", "value": "new-secret" }` | Replace the credential |
| `{ "action": "clear" }` | Remove an optional provider credential |
| `null`, a bare string, or an unknown action/field | Reject with `422 invalid_config` |

Provider input requires `kind` and `base_url`; `native_chat` and `transport.http1_only` default to `false`, and `api` defaults to no explicit selection. Its `api_key` and `transport.proxy_url` each use the credential contract and may be cleared; keeping them on a new provider leaves them absent. Omitting `transport` preserves a previous proxy URL but resets `http1_only` to `false`. Ordinary reads expose only `has_api_key` and `transport.has_proxy_url`, never the proxy URL, even when it has no password.

API Key input accepts `secret`, `enabled` (default `true`) and `expires_at` (Unix seconds; omitted or `null` means no expiry). Creating a key requires `secret: { "action": "set", "value": "..." }`; replacing a key may omit `secret` to preserve it. Clearing a client secret is invalid. Ordinary reads expose `has_secret: true`. Limits are not fields of this DTO: edit `llm.subject_limits` through export/edit/full-configuration PUT. Model input is the existing complete model configuration described in the [proxy guide](rust-proxy.md); omitted optional model settings use their configuration defaults.

For example, add an unused local Provider to the draft without exporting credentials:

```sh
umask 077
export NYRO_ADMIN_TOKEN="$(cat admin.token)"
curl --fail-with-body http://127.0.0.1:19531/admin/providers \
  -H "Authorization: Bearer $NYRO_ADMIN_TOKEN" > providers.json
jq '{expected_revision: .draft_revision, id: "local-example", value: {
  kind: "openai", base_url: "http://127.0.0.1:8000/v1"
}}' providers.json > create.json
curl --fail-with-body -X POST http://127.0.0.1:19531/admin/providers \
  -H "Authorization: Bearer $NYRO_ADMIN_TOKEN" \
  -H 'Content-Type: application/json' --data-binary @create.json
unset NYRO_ADMIN_TOKEN
```

Check the returned revision before further edits or publication. An existing ID returns `409 entity_exists`; stale revisions return `409 revision_conflict`; a missing item returns `404 not_found`. Deleting a Provider referenced by any model backend, including a zero-weight backend, returns `409 entity_referenced`. Deleting an API Key referenced by any model's `subjects` or by `llm.subject_limits` does too. There is no cascade: remove references first, or make a coordinated full-configuration edit. Deleting the last model returns `422 invalid_config` because every saved draft must remain valid.

## Publication and recovery

The server checks process settings and builds a runtime candidate before committing publication. Precommit rejection leaves the published restart target and active generation unchanged; a valid saved draft remains available for correction. Database commit is the durable publication boundary. Host activation follows it; these two steps are **not one atomic transaction**.

A successful activation returns `200` with `publication: "active"`. If activation is interrupted after the durable commit, the result is `202` with `publication: "pending"`; the published revision is the restart target even while `active_revision` remains older. Read the state with GET, then retry publication of the current revision or restart to recover that published target. This is not a background retry service. The data-plane `/readyz` reports Host lease availability, not database health or whether published and active revisions agree.

PostgreSQL transactions use `synchronous_commit = on`. If a commit acknowledgement is lost through disconnect or timeout and its outcome cannot be established, the API returns `503 storage_outcome_unknown`. The write **may already have persisted**; this is neither proof of rollback nor confirmation of durable publication. The store is disabled and does not reconnect or retry automatically. Restore database availability, restart without `--config`, then read the draft and published revisions and reconcile the intended edit before resubmitting. Restart loads the persisted published snapshot, which may be newer than the generation that was active before the failure. Reads and known storage failures, including failures before COMMIT is sent, return `503 storage_failed`.

Request body reading is limited to 15 seconds (`408 request_timeout` before acceptance). After a valid request enters the owned queue, the HTTP handler waits up to 10 seconds. A still-running save, entity edit or publish returns `202 { "operation": "pending" }`; it can later succeed, conflict or fail. This differs from `publication: "pending"`, which confirms durable publication already committed. Accepted work continues after client disconnect or the handler stops waiting: use GET to inspect the draft contents and draft/published/active revisions before retrying, rather than blindly resubmitting a write. A GET that cannot finish within 10 seconds returns `503 control_busy`. Operations are serialized, with at most 16 admitted operations; excess requests receive `503`. Shutdown stops admission and waits for owned work before Host cleanup; work still queued when shutdown begins can return `control_stopping`.

Publishing an already active revision, or an equivalent configuration fingerprint, does not create another runtime generation. New requests use the activated snapshot; existing leases and SSE streams retain their original generation. Shared in-memory rate, quota, subject windows, health and routing history retain their existing identity and update rules; storing configuration does not make counters durable. A process restart clears them.

Changing `server.listen` or `limit.concurrency` returns `409 restart_required` before publication. Simply restarting this database still loads the previous published values: this increment has no offline publication path for those changes. Use a separately initialized database with a revised seed when a new process configuration is needed. Active or retained rate/quota/window policy changes can likewise reject candidate construction; see the [proxy guide](rust-proxy.md) for those shared-resource constraints.

| Status | Error code / meaning |
|---|---|
| `400` | `invalid_path`, `query_not_supported`, or `invalid_request` for malformed JSON |
| `401` | `authentication_failed` |
| `404` | `not_found`: unknown collection or missing item |
| `408` | `request_timeout`: body read timed out before operation acceptance |
| `409` | `revision_conflict`, `entity_exists`, `entity_referenced` or `restart_required` |
| `413` / `415` | `invalid_request`: body too large / unsupported content type |
| `422` | `invalid_config`, `candidate_rejected`, or `invalid_request` for a JSON shape/type mismatch |
| `500` | `control_failed` |
| `503` | `storage_failed`, `storage_outcome_unknown`, `control_busy` or `control_stopping` |

Errors use `{ "error": { "code": "..." } }` without credentials or database details. A postcommit activation problem uses the successful `202` pending shape above, not a rejected-edit error.

## Local regression

```sh
cargo test -p nyro-control -p nyro --offline
cargo build -p nyro --offline
python3 tests/serve_smoke.py
```

The [process regression](../../tests/serve_smoke.py) uses temporary SQLite files and loopback mocks to exercise authentication, entity CRUD, redacted reads and explicit export, credential rotation, draft/publication separation, conflicts, rejected publication, SSE continuity and published-snapshot restart recovery. The separate [proxy reload regression](../../tests/proxy_reload_smoke.py) covers file-mode SIGHUP behavior.


PostgreSQL integration tests are opt-in. Provision a disposable PostgreSQL instance and save an administrative database URL in a private file; the role must be able to create and drop databases. The [store tests](../../crates/nyro-control/tests/postgres.rs) create their own isolated databases and remove those databases after successful checks:

```sh
export NYRO_TEST_POSTGRES_URL="$(cat /private/path/postgres-admin.url)"
cargo test -p nyro-control --test postgres -- --ignored
unset NYRO_TEST_POSTGRES_URL
```

For the [PostgreSQL process regression](../../tests/serve_postgres_smoke.py), separately create a fresh, empty, disposable database on a loopback PostgreSQL server. Save its URL in `/private/path/test.url` with explicit `sslmode=disable`; the local fault proxy uses this mode to drop a COMMIT acknowledgement. Build the root binary before running the driver:

```sh
cargo build -p nyro --offline
python3 tests/serve_postgres_smoke.py --postgres-url-file /private/path/test.url
```

The process regression initializes that database and leaves it intact for inspection; it does not erase an existing database. Use a new empty database for each run. It exercises the shared serve behavior plus PostgreSQL ownership, startup redaction and recovery after a lost COMMIT reply. Real-database regression coverage for this increment is PostgreSQL 16; it does not establish compatibility with every PostgreSQL version.
