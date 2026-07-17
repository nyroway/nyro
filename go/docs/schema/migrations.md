# Schema Migrations (mysql/postgres)

How the Go storage backend's mysql/postgres schema gets changed in production,
without requiring the connecting account to have DDL rights. sqlite is
out of scope here — it stays on GORM AutoMigrate for dev/test/single-node use
(see [database.md](database.md) and the `--auto-migrate` flag below).

## Why this exists

`db.AutoMigrate(...)` at startup can't produce SQL a DBA reviews ahead of
time, behaves somewhat differently across GORM/driver versions, and won't do
"dangerous" changes (dropping a column/index) without being asked. Many
production deployments also simply don't grant the application's DB account
DDL rights at all — schema changes go through a DBA.

So mysql/postgres get **versioned migration files** generated from the GORM
models and reviewed like any other code change, instead of relying on
`AutoMigrate` to compute DDL at runtime.

## Source of truth

`go/internal/storage/model/models.go` (`model.All()`) is canonical. Every
schema change starts there.

## Day-to-day workflow

1. Change the GORM model(s).
2. `make go-migrate-diff NAME=<short_description>` — generates a new
   timestamped SQL file under `go/migrations/{mysql,postgres}/` for each
   dialect (`NAME` is required; it becomes part of the filename, e.g.
   `20260714120000_add_upstream_provider_column.sql`).
3. Review the generated SQL like any other diff.
4. Commit it.

CI enforces step 2–4 happened (see below) — a model change with no matching
committed migration fails the build.

## How generation works

`go/tools/atlasloader` is a small program (using
`ariga.io/atlas-provider-gorm`) that renders the GORM models to plain SQL for
a given dialect. `make go-migrate-diff` (and every other `go-migrate-*`
target) first runs it to produce `go/schema/{mysql,postgres}.sql`
(generated, gitignored — not the migration files themselves), then points
`go/atlas.hcl`'s `src` at that file and runs `atlas migrate diff`, which
computes the difference against `go/migrations/<dialect>/` and writes only
the new incremental SQL.

Deliberately **not** using atlas's `data "external_schema"` mechanism (which
would call `atlasloader` directly from `atlas.hcl`, skipping the
intermediate file): that data source only works on Atlas's paid-tier
standard distribution (confirmed empirically). Rendering to a plain file
first means every `go-migrate-*` target runs on the free, Apache-2.0
Atlas Community Edition docker image (`docker.io/arigaio/atlas:latest-community`)
— no account, no paid tier, no version pinning to dodge a paywall.

`go/tools/atlasloader` is deliberately **not** a `nyro` subcommand (not
`nyro tool render-schema`): it's invoked via `go run` at build/CI time only,
and `ariga.io/atlas-provider-gorm` pulls in a large, unrelated dependency
graph (spanner, mssql, azure-keyvault drivers, ...) that a shipped
production binary shouldn't carry for a capability only developers/CI ever
use.

## Local dev database

`go-migrate-diff`/`go-migrate-lint`/`go-migrate-verify` need a throwaway
mysql/postgres reachable at the `mysql_dev_url`/`postgres_dev_url` defaults
in `go/atlas.hcl` (`localhost:13306`/`:15432`). Spin them up once with:

```
docker run -d --name nyro-atlas-mysql-dev -e MYSQL_ROOT_PASSWORD=pass -p 13306:3306 mysql:8
docker run -d --name nyro-atlas-postgres-dev -e POSTGRES_PASSWORD=pass -p 15432:5432 postgres:16
# once both are up (mysqladmin ping / pg_isready):
docker exec nyro-atlas-mysql-dev mysql -ppass -e "CREATE DATABASE IF NOT EXISTS dev CHARACTER SET utf8mb4 COLLATE utf8mb4_general_ci;"
docker exec nyro-atlas-postgres-dev psql -U postgres -c "CREATE DATABASE dev;"
```

They're scratch space atlas uses to compute diffs — never a real target,
safe to leave running or throw away and recreate at any time.

The `dev` mysql database's charset/collation must be set explicitly to
`utf8mb4`/`utf8mb4_general_ci` (matching CI's setup step): tables with no
explicit charset inherit whatever the connected database defaults to, and
Atlas bakes that into the generated `CREATE TABLE` statements. Left
unset, MySQL 8 defaults to `utf8mb4_0900_ai_ci`, a collation unsupported
by MySQL 5.7 and most MariaDB versions — the broadly-compatible
`utf8mb4_general_ci` needs to come from the dev database, not the
generator.

## Makefile targets

All require `docker`; `go-migrate-diff`/`go-migrate-lint`/`go-migrate-verify`
also need a throwaway "dev" database per dialect reachable at the url
configured in `go/atlas.hcl` (`mysql_dev_url`/`postgres_dev_url` variables,
defaulting to `localhost:13306`/`:15432` — matching both a local
docker/podman container a dev spins up and CI's service containers;
override with `--var` if yours run on different ports).

- `go-migrate-diff NAME=...` — generate a new migration (see above).
- `go-migrate-lint [LATEST=1]` — static analysis of the most recent N
  committed migration(s): destructive/unsafe changes, locking risks, naming
  conventions. **Does not** compare against the live GORM models — a model
  changed with no matching migration file passes lint silently (confirmed
  empirically). That's what `go-migrate-verify` is for.
- `go-migrate-verify` — runs the real generator (`go-migrate-diff
  NAME=verify`) and fails if it produced anything not already committed
  under `go/migrations/`. The same "generate, then check for uncommitted
  changes" idiom used for any other checked-in codegen. Only meaningful
  against an already-committed `go/migrations/` (i.e. in CI, or on a clean
  working tree).
- `go-migrate-status DIALECT=mysql|postgres DSN=...` — read-only: which
  migrations are applied/pending against a live database. Thin wrapper over
  `atlas migrate status`, no nyro-specific logic. (`DIALECT` maps to Atlas's
  own `--env`/HCL `env` block naming, which we can't rename — but it means
  mysql vs postgres here, not a deployment environment.)

CI (`go-check` job in `.github/workflows/ci.yml`) runs `go-migrate-lint` and
`go-migrate-verify` on every PR, with mysql/postgres service containers as
the throwaway dev databases.

## Applying to a real database (DBA workflow)

A DBA reviews the plain SQL under `go/migrations/{mysql,postgres}/`, then
applies it with:

```
atlas migrate apply --env mysql --config file://atlas.hcl --url "$DSN"
```

(same free Community Edition docker image, still no account needed). Use
`atlas migrate apply`, not a raw `mysql`/`psql` import of the same file —
`apply` also creates/updates the `atlas_schema_revisions` bookkeeping table
that nyro's own startup check reads (`Backend.CheckSchemaVersion`); a plain
client import would leave the schema correct but that bookkeeping table
missing, so nyro would still treat the database as unmigrated. Note: on
postgres, `atlas_schema_revisions` lives in its own schema of the same name,
not `public`.

## Runtime behavior: `--auto-migrate`

`nyro admin`/`nyro gateway` take `--auto-migrate` (default `false`,
regardless of backend — whether the connecting account has DDL rights is a
deployment decision, not something inferred from the database engine):

- `--auto-migrate` (or `=true`): runs `GORM AutoMigrate` at startup, same as
  before this whole migrations system existed. Useful for local sqlite dev,
  or a mysql/postgres account known to have DDL rights.
- unset (default): no DDL. Instead, a read-only startup check
  (`Backend.CheckSchemaVersion`) — for mysql/postgres, compares the latest
  version embedded in the binary (from `go/migrations/<dialect>/`, see the
  `migrations` package) against the version recorded in
  `atlas_schema_revisions`; for sqlite, just confirms the canonical tables
  exist. Either way, a mismatch fails fast with a message pointing at what
  to do next (apply pending migrations, or pass `--auto-migrate`).

## Filename convention

Atlas's own default: `<timestamp>_<name>.sql` (e.g.
`20260713135351_baseline.sql`). The timestamp keeps files in creation order
and avoids collisions between migrations authored on different branches —
sequential integers (`0001`, `0002`, ...) don't have that property. `NAME`
is required on `go-migrate-diff` specifically so the description part stays
meaningful (no repo full of `unnamed.sql`/`verify.sql` files — the latter is
`go-migrate-verify`'s own internal probe name and never actually committed).

## Out of scope

Root `deploy/schema/{mysql.sql,postgres.sql}` belongs to the Rust storage
layer (`crates/nyro-core/src/storage/*.rs`) — a different schema
(providers/model_backends/... vs. this package's upstreams/routes/...) that
predates this Go migrations system. Not touched by anything on this page.
