# Schema Migrations (mysql/postgres)

How the Go storage backend's mysql/postgres schema gets changed in production.
The canonical schema is the GORM models (`go/internal/storage/model`,
`model.All()`); everything here is about turning those into DDL. This system
depends on **GORM only** — no Atlas, no external migration framework. sqlite is
covered too (it just uses AutoMigrate directly; see [database.md](database.md)).

## Two ways to migrate

### 1. Automatic — `--auto-migrate` (GORM AutoMigrate)

`nyro admin`/`nyro gateway --auto-migrate` runs `GORM AutoMigrate` at startup,
creating/altering tables to match the models. Requires the connecting account
to have DDL rights. Off by default (whether the account has DDL rights is a
deployment decision, not inferred from the engine). Good for local sqlite dev
and for mysql/postgres accounts that are allowed to run DDL.

AutoMigrate is **additive**: it creates missing tables/columns/indexes but
never drops a column or table, and it can misread a column *rename* as a new
column (leaving the old one). Destructive or rename changes need a hand-written
one-off SQL statement.

### 2. Manual — `nyro migrate dump` / `diff`

For deployments where the application account has **no DDL rights** (schema
changes go through a DBA who applies reviewed SQL by hand, often importing a
`.sql` through a company Web platform), the binary prints the SQL for you:

```
nyro migrate dump  --dsn <dialect-dsn, read-only>  [--output f.sql]
    # Full CREATE DDL for a fresh database, in --dsn's dialect.

nyro migrate diff  --shadow-dsn <ddl-dsn>  ( --target-dsn <ro-dsn> | --target-file <schema.sql> )  [--output f.sql]
    # Incremental DDL to bring an existing schema up to the models.
```

The DBA reviews the printed SQL and applies it. No CLI or framework is needed
on the target server — just the ability to run the reviewed SQL.

Startup then uses a read-only check (no `--auto-migrate`): `CheckSchema`
confirms every canonical table exists and fails fast if the schema was never
applied.

## `nyro migrate dump`

Renders `model.All()` to `CREATE TABLE`/index DDL in the target dialect. It runs
in a GORM **DryRun** session — nothing is executed — so `--dsn` may point at a
**read-only** account; it exists only to select the dialect (GORM generates
mysql-vs-postgres DDL through a live dialector). Omit `--dsn` for sqlite
(in-memory).

Because rendering needs a live dialect connection, a fresh mysql/postgres
deployment points `--dsn` at the (empty) target server — nothing is written to
it.

## `nyro migrate diff`

Prints the incremental DDL to evolve a **current** schema to the models. It
works by loading the current schema onto a throwaway **shadow** database
(`--shadow-dsn`, needs DDL rights, must differ from the target), running
`AutoMigrate` there for real, and capturing the statements it issues. A real
run on a throwaway is used deliberately — it's exactly what a real apply does.

The "current schema" comes from exactly one source:

- `--target-file <schema.sql>` — an exact schema dump (e.g. a previous `dump`
  output). **Precise, recommended.**
- `--target-dsn <ro-dsn>` — a read-only live database, introspected via GORM.
  **Lossy**: it reconstructs tables/columns/primary keys but not secondary
  indexes/constraints, so the diff may re-suggest indexes that already exist on
  the target (review and skip those). The target is only read.

`--shadow-dsn` must be the same dialect as the target and point at a dedicated
scratch database (its model tables are dropped before each run). Omit it for
sqlite (in-memory).

A typical upgrade workflow keeps a schema snapshot per release and diffs
against it — no live database needed:

```
# release N: snapshot the schema (store it anywhere; it need not be committed)
nyro migrate dump --dsn mysql://ro@host/anydb > schema_vN.sql
# release N+1: incremental DDL from N to the new models
nyro migrate diff --target-file schema_vN.sql --shadow-dsn mysql://ddl@host/scratch
```

## How it works (GORM only)

Both commands capture the SQL GORM's migrator builds via a logger, then keep
only DDL statements (`CREATE`/`ALTER`/`DROP`). `dump` captures a DryRun
`CreateTable` (no execution); `diff` captures a real `AutoMigrate` on the
shadow. All of this lives in `go/internal/schemadump` and uses nothing beyond
`gorm` and the models — so `nyro migrate` ships in the binary without pulling
in any schema-tool dependency.

## Runtime behavior

`nyro admin`/`nyro gateway`:

- `--auto-migrate`: run AutoMigrate at startup (see above).
- unset (default): no DDL. A read-only `CheckSchema` confirms every canonical
  table (`model.All()`) exists; a missing table fails fast with a message
  pointing at `--auto-migrate` or `nyro migrate`. It does not check
  column-level drift — keeping the schema current is the operator's job.

## Accepted trade-offs

- **AutoMigrate ceiling**: no drops, rename-as-add, cross-dialect type-change
  quirks — handle rare destructive changes with a hand-written one-off SQL.
- **`dump` needs a live dialect connection** (read-only): pure GORM can't render
  mysql/postgres DDL fully offline. sqlite uses an in-memory connection.
- **`diff --target-dsn` is lossy** (indexes/constraints) — prefer
  `--target-file` when precision matters.

## Out of scope

Root `deploy/schema/{mysql.sql,postgres.sql}` belongs to the Rust storage layer
(`crates/nyro-core/src/storage/*.rs`) — a different schema that predates this
Go system. Not touched here.
