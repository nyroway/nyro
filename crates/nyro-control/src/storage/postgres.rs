use super::sqlite::{decode, encode};
use crate::{Error, MAX_CONFIG_BYTES, Snapshot, State};
use nyro_config::Config;
use sqlx::{
    ConnectOptions, Connection, PgConnection, Row,
    postgres::{PgConnectOptions, PgSslMode},
};
use std::{net::IpAddr, str::FromStr, time::Duration};
use tokio::time::timeout;

/// Migration source of truth, also exported for DBA reference schema generation.
pub const POSTGRES_SCHEMA: &str = "CREATE TABLE public.nyro_control_state (
    singleton BIGINT PRIMARY KEY CHECK (singleton = 1),
    schema_version BIGINT NOT NULL CHECK (schema_version = 1),
    draft_revision BIGINT NOT NULL CHECK (draft_revision > 0),
    draft_json TEXT NOT NULL CHECK (octet_length(draft_json) BETWEEN 1 AND 1048576),
    published_revision BIGINT NOT NULL CHECK (published_revision > 0 AND published_revision <= draft_revision),
    published_json TEXT NOT NULL CHECK (octet_length(published_json) BETWEEN 1 AND 1048576)
);\n";
const DEADLINE: Duration = Duration::from_secs(5);
// Session locks are scoped to the current database, never to one table or revision.
const OWNER_KEY: i64 = 0x4e59_524f_4354_524c;

pub(super) struct Store {
    connection: Option<PgConnection>,
}

enum Failure {
    Control(Error),
    Database(sqlx::Error),
}
impl From<Error> for Failure {
    fn from(value: Error) -> Self {
        Self::Control(value)
    }
}
impl From<sqlx::Error> for Failure {
    fn from(value: sqlx::Error) -> Self {
        Self::Database(value)
    }
}
type Result<T> = std::result::Result<T, Failure>;

fn options(url: &str) -> std::result::Result<PgConnectOptions, Error> {
    if !(url.starts_with("postgres://") || url.starts_with("postgresql://")) || url.contains('#') {
        return Err(Error::Storage);
    }
    let destination = url
        .split_once("://")
        .ok_or(Error::Storage)?
        .1
        .split('?')
        .next()
        .ok_or(Error::Storage)?;
    let (authority, database) = destination.split_once('/').ok_or(Error::Storage)?;
    let host = authority.rsplit('@').next().ok_or(Error::Storage)?;
    if host.is_empty() || host.starts_with(':') || database.trim_matches('/').is_empty() {
        return Err(Error::Storage);
    }
    // SQLx logs unrecognized URL query values. Reject them before its parser sees them.
    let mut explicit_tls = false;
    if let Some((_, query)) = url.split_once('?') {
        for pair in query.split('&') {
            let (key, _) = pair.split_once('=').ok_or(Error::Storage)?;
            match key {
                "sslmode" | "ssl-mode" if !explicit_tls => explicit_tls = true,
                "sslrootcert" | "ssl-root-cert" | "ssl-ca" | "sslcert" | "ssl-cert" | "sslkey"
                | "ssl-key" => {}
                _ => return Err(Error::Storage),
            }
        }
    }
    // SQLx may also log malformed pgpass records (including their contents).
    // Suppression is scoped to this synchronous parser, never process-global.
    let mut options =
        tracing::subscriber::with_default(tracing::subscriber::NoSubscriber::default(), || {
            PgConnectOptions::from_str(url)
        })
        .map_err(|_| Error::Storage)?
        .disable_statement_logging();
    if !explicit_tls {
        options = options.ssl_mode(PgSslMode::VerifyFull);
    }
    if options.get_database().is_none_or(str::is_empty) || options.get_socket().is_some() {
        return Err(Error::Storage);
    }
    match options.get_ssl_mode() {
        PgSslMode::Disable
            if options.get_host() == "localhost"
                || options
                    .get_host()
                    .trim_matches(['[', ']'])
                    .parse::<IpAddr>()
                    .is_ok_and(|ip| ip.is_loopback()) => {}
        PgSslMode::Require | PgSslMode::VerifyCa | PgSslMode::VerifyFull => {}
        _ => return Err(Error::Storage),
    }
    Ok(options.application_name("nyro-control").options([
        ("statement_timeout", "3000"),
        ("lock_timeout", "1000"),
        ("synchronous_commit", "on"),
        ("search_path", "pg_catalog"),
    ]))
}

impl Store {
    pub async fn open(url: &str, seed: Option<&Config>) -> std::result::Result<Self, Error> {
        let options = options(url)?;
        let connection = timeout(DEADLINE, PgConnection::connect_with(&options))
            .await
            .map_err(|_| Error::Storage)?
            .map_err(|_| Error::Storage)?;
        let mut store = Self {
            connection: Some(connection),
        };
        let mut connection = store.connection.take().ok_or(Error::Storage)?;
        let mut committing = false;
        let result = timeout(DEADLINE, async {
            // Server-side octet_length must measure the same UTF-8 bytes as Config encoding.
            let encoding: String = sqlx::query_scalar("SHOW server_encoding").fetch_one(&mut connection).await?;
            if encoding != "UTF8" { return Err(Error::Schema.into()); }
            let locked: bool = sqlx::query_scalar("SELECT pg_catalog.pg_try_advisory_lock($1)").bind(OWNER_KEY).fetch_one(&mut connection).await?;
            if !locked { return Err(Error::Storage.into()); }
            let mut tx = connection.begin().await?;
            let initialized = check_schema(&mut tx).await?;
            match (initialized, seed) {
                (true, Some(_)) => return Err(Error::AlreadyInitialized.into()),
                (false, None) => return Err(Error::SeedRequired.into()),
                (false, Some(config)) => {
                    let json = encode(config)?;
                    sqlx::query(POSTGRES_SCHEMA).execute(&mut *tx).await?;
                    sqlx::query("INSERT INTO public.nyro_control_state (singleton, schema_version, draft_revision, draft_json, published_revision, published_json) VALUES (1, 1, 1, $1, 1, $1)").bind(json).execute(&mut *tx).await?;
                },
                (true, None) => {},
            }
            read_state(&mut tx).await?;
            committing = true;
            tx.commit().await?;
            Ok(())
        }).await;
        store.finish(connection, result, committing)?;
        Ok(store)
    }

    // Taking the connection makes cancellation fail closed as well: a dropped future
    // drops the socket, and the Store can never reconnect without reacquiring ownership.
    pub async fn state(&mut self) -> std::result::Result<State, Error> {
        let mut connection = self.connection.take().ok_or(Error::Storage)?;
        let result = timeout(DEADLINE, async {
            let mut tx = connection.begin().await?;
            sqlx::query("SET TRANSACTION ISOLATION LEVEL REPEATABLE READ, READ ONLY")
                .execute(&mut *tx)
                .await?;
            let result = async {
                if !check_schema(&mut tx).await? {
                    return Err(Error::Schema.into());
                }
                read_state(&mut tx).await
            }
            .await;
            tx.rollback().await?;
            result
        })
        .await;
        self.finish(connection, result, false)
    }

    pub async fn save(
        &mut self,
        expected: u64,
        config: &Config,
    ) -> std::result::Result<Snapshot, Error> {
        let json = encode(config)?;
        self.write(expected, Some((json, config))).await
    }
    pub async fn publish(&mut self, revision: u64) -> std::result::Result<Snapshot, Error> {
        self.write(revision, None).await
    }

    async fn write(
        &mut self,
        expected: u64,
        draft: Option<(String, &Config)>,
    ) -> std::result::Result<Snapshot, Error> {
        let mut connection = self.connection.take().ok_or(Error::Storage)?;
        let mut committing = false;
        let result = timeout(DEADLINE, async {
            let mut tx = connection.begin().await?;
            let result = async {
                // Serialize against external SQL writers as well as checking the CAS in SQL.
                sqlx::query("LOCK TABLE public.nyro_control_state IN EXCLUSIVE MODE").execute(&mut *tx).await?;
                if !check_schema(&mut tx).await? { return Err(Error::Schema.into()); }
                let state = read_state(&mut tx).await?;
                if state.draft.revision != expected { return Err(Error::Conflict.into()); }
                let snapshot = if let Some((json, config)) = draft {
                    let revision = i64::try_from(expected).ok().and_then(|v| v.checked_add(1)).ok_or(Error::Conflict)?;
                    let changed = sqlx::query("UPDATE public.nyro_control_state SET draft_revision = $1, draft_json = $2 WHERE singleton = 1 AND draft_revision = $3").bind(revision).bind(json).bind(expected as i64).execute(&mut *tx).await?;
                    if changed.rows_affected() != 1 { return Err(Error::Conflict.into()); }
                    Snapshot { revision: revision as u64, config: config.clone() }
                } else {
                    if state.published.revision != expected {
                        let changed = sqlx::query("UPDATE public.nyro_control_state SET published_revision = draft_revision, published_json = draft_json WHERE singleton = 1 AND draft_revision = $1").bind(expected as i64).execute(&mut *tx).await?;
                        if changed.rows_affected() != 1 { return Err(Error::Conflict.into()); }
                    }
                    state.draft
                };
                Ok(snapshot)
            }.await;
            match result {
                Ok(snapshot) => { committing = true; tx.commit().await?; Ok(snapshot) },
                Err(error) => { tx.rollback().await?; Err(error) },
            }
        }).await;
        self.finish(connection, result, committing)
    }

    fn finish<T>(
        &mut self,
        connection: PgConnection,
        result: std::result::Result<Result<T>, tokio::time::error::Elapsed>,
        committing: bool,
    ) -> std::result::Result<T, Error> {
        match result {
            Ok(Ok(value)) => {
                self.connection = Some(connection);
                Ok(value)
            }
            Ok(Err(Failure::Control(error))) => {
                self.connection = Some(connection);
                Err(error)
            }
            Ok(Err(Failure::Database(ref error))) if reusable(error) => {
                self.connection = Some(connection);
                Err(Error::Storage)
            }
            _ => {
                drop(connection);
                Err(if committing {
                    Error::OutcomeUnknown
                } else {
                    Error::Storage
                })
            }
        }
    }
    pub async fn close(mut self) -> std::result::Result<(), Error> {
        if let Some(connection) = self.connection.take() {
            timeout(DEADLINE, connection.close())
                .await
                .map_err(|_| Error::Storage)?
                .map_err(|_| Error::Storage)?;
        }
        Ok(())
    }
}

fn reusable(error: &sqlx::Error) -> bool {
    match error {
        sqlx::Error::Database(error) => error.code().is_some_and(|code| {
            !code.starts_with("08")
                && !["55P03", "57014", "57P01", "57P02", "57P03", "25P03"].contains(&code.as_ref())
        }),
        _ => false,
    }
}

async fn read_state(connection: &mut PgConnection) -> Result<State> {
    // CASE bounds materialized text even when constraints were previously bypassed.
    let rows = sqlx::query("SELECT singleton, schema_version, draft_revision, published_revision, CASE WHEN octet_length(draft_json) BETWEEN 1 AND $1 THEN draft_json END AS draft_json, CASE WHEN octet_length(published_json) BETWEEN 1 AND $1 THEN published_json END AS published_json FROM public.nyro_control_state LIMIT 2").bind(MAX_CONFIG_BYTES as i32).fetch_all(connection).await?;
    if rows.len() != 1 {
        return Err(Error::Schema.into());
    }
    let row = &rows[0];
    let singleton: i64 = row.try_get("singleton").map_err(|_| Error::Schema)?;
    let version: i64 = row.try_get("schema_version").map_err(|_| Error::Schema)?;
    let draft: i64 = row.try_get("draft_revision").map_err(|_| Error::Schema)?;
    let published: i64 = row
        .try_get("published_revision")
        .map_err(|_| Error::Schema)?;
    let draft_json: String = row.try_get("draft_json").map_err(|_| Error::Schema)?;
    let published_json: String = row.try_get("published_json").map_err(|_| Error::Schema)?;
    if singleton != 1
        || version != 1
        || published <= 0
        || draft < published
        || (draft == published && draft_json != published_json)
    {
        return Err(Error::Schema.into());
    }
    Ok(State {
        draft: Snapshot {
            revision: draft as u64,
            config: decode(&draft_json)?,
        },
        published: Snapshot {
            revision: published as u64,
            config: decode(&published_json)?,
        },
    })
}

async fn check_schema(connection: &mut PgConnection) -> Result<bool> {
    let foreign: bool = sqlx::query_scalar("SELECT
        EXISTS (SELECT 1 FROM pg_namespace WHERE nspname <> 'public' AND nspname <> 'information_schema' AND left(nspname, 3) <> 'pg_') OR
        EXISTS (SELECT 1 FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace WHERE n.nspname = 'public') OR
        EXISTS (SELECT 1 FROM pg_extension WHERE extname <> 'plpgsql') OR
        EXISTS (SELECT 1 FROM pg_event_trigger) OR
        EXISTS (SELECT 1 FROM pg_foreign_server) OR
        EXISTS (SELECT 1 FROM pg_foreign_data_wrapper) OR
        EXISTS (SELECT 1 FROM pg_publication) OR
        EXISTS (SELECT 1 FROM pg_largeobject_metadata) OR
        EXISTS (SELECT 1 FROM pg_collation c JOIN pg_namespace n ON n.oid = c.collnamespace WHERE n.nspname = 'public') OR
        EXISTS (SELECT 1 FROM pg_operator o JOIN pg_namespace n ON n.oid = o.oprnamespace WHERE n.nspname = 'public') OR
        EXISTS (SELECT 1 FROM pg_conversion c JOIN pg_namespace n ON n.oid = c.connamespace WHERE n.nspname = 'public') OR
        EXISTS (SELECT 1 FROM pg_ts_config c JOIN pg_namespace n ON n.oid = c.cfgnamespace WHERE n.nspname = 'public') OR
        EXISTS (SELECT 1 FROM pg_ts_dict d JOIN pg_namespace n ON n.oid = d.dictnamespace WHERE n.nspname = 'public')")
        .fetch_one(&mut *connection).await?;
    if foreign {
        return Err(Error::Schema.into());
    }
    let objects: Vec<(String, String, bool)> = sqlx::query_as("SELECT c.relname, c.relkind::text, (c.relpersistence = 'p' AND NOT c.relispartition AND NOT c.relrowsecurity AND NOT c.relforcerowsecurity AND NOT c.relhasrules AND NOT c.relhastriggers) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = 'public' ORDER BY c.relname LIMIT 3").fetch_all(&mut *connection).await?;
    let types: Vec<(String, String)> = sqlx::query_as("SELECT t.typname, t.typtype::text FROM pg_type t JOIN pg_namespace n ON n.oid = t.typnamespace WHERE n.nspname = 'public' ORDER BY t.typname LIMIT 3").fetch_all(&mut *connection).await?;
    if objects.is_empty() && types.is_empty() {
        return Ok(false);
    }
    if objects
        != [
            ("nyro_control_state".into(), "r".into(), true),
            ("nyro_control_state_pkey".into(), "i".into(), true),
        ]
        || types
            != [
                ("_nyro_control_state".into(), "b".into()),
                ("nyro_control_state".into(), "c".into()),
            ]
    {
        return Err(Error::Schema.into());
    }
    let columns: Vec<(String, String, bool, bool, String, String)> = sqlx::query_as("SELECT a.attname, format_type(a.atttypid, a.atttypmod), a.attnotnull, a.atthasdef, a.attidentity::text, a.attgenerated::text FROM pg_attribute a WHERE a.attrelid = 'public.nyro_control_state'::regclass AND a.attnum > 0 ORDER BY a.attnum LIMIT 7").fetch_all(&mut *connection).await?;
    let expected = [
        ("singleton", "bigint"),
        ("schema_version", "bigint"),
        ("draft_revision", "bigint"),
        ("draft_json", "text"),
        ("published_revision", "bigint"),
        ("published_json", "text"),
    ];
    if columns.len() != expected.len()
        || columns.iter().zip(expected).any(|(actual, (name, ty))| {
            actual
                != &(
                    name.into(),
                    ty.into(),
                    true,
                    false,
                    String::new(),
                    String::new(),
                )
        })
    {
        return Err(Error::Schema.into());
    }
    let constraints: Vec<(String, String, bool, bool, bool)> = sqlx::query_as("SELECT conname, pg_get_constraintdef(oid), convalidated AND COALESCE((to_jsonb(c)->>'conenforced')::boolean, true), condeferrable, condeferred FROM pg_constraint c WHERE contype <> 'n' AND conrelid = 'public.nyro_control_state'::regclass ORDER BY conname LIMIT 8").fetch_all(&mut *connection).await?;
    let expected = [
        (
            "nyro_control_state_check",
            "CHECK (((published_revision > 0) AND (published_revision <= draft_revision)))",
        ),
        (
            "nyro_control_state_draft_json_check",
            "CHECK (((octet_length(draft_json) >= 1) AND (octet_length(draft_json) <= 1048576)))",
        ),
        (
            "nyro_control_state_draft_revision_check",
            "CHECK ((draft_revision > 0))",
        ),
        ("nyro_control_state_pkey", "PRIMARY KEY (singleton)"),
        (
            "nyro_control_state_published_json_check",
            "CHECK (((octet_length(published_json) >= 1) AND (octet_length(published_json) <= 1048576)))",
        ),
        (
            "nyro_control_state_schema_version_check",
            "CHECK ((schema_version = 1))",
        ),
        (
            "nyro_control_state_singleton_check",
            "CHECK ((singleton = 1))",
        ),
    ];
    if constraints.len() != expected.len()
        || constraints
            .iter()
            .zip(expected)
            .any(|(actual, (name, definition))| {
                actual != &(name.into(), definition.into(), true, false, false)
            })
    {
        return Err(Error::Schema.into());
    }
    let valid_index: bool = sqlx::query_scalar("SELECT indisvalid AND indisready AND indisunique AND indisprimary AND indpred IS NULL AND indexprs IS NULL FROM pg_index WHERE indexrelid = 'public.nyro_control_state_pkey'::regclass AND indrelid = 'public.nyro_control_state'::regclass").fetch_one(&mut *connection).await?;
    if !valid_index {
        return Err(Error::Schema.into());
    }
    Ok(true)
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn tls_defaults_and_literal_loopback_modes() {
        assert!(matches!(
            options("postgres://u:p@example.test/control")
                .unwrap()
                .get_ssl_mode(),
            PgSslMode::VerifyFull
        ));
        for mode in ["require", "verify-ca", "verify-full"] {
            assert!(
                options(&format!(
                    "postgres://u:p@example.test/control?sslmode={mode}"
                ))
                .is_ok()
            );
        }
        for host in ["localhost", "127.0.0.1", "[::1]"] {
            assert!(options(&format!("postgres://u:p@{host}/control?sslmode=disable")).is_ok());
        }
        for url in [
            "postgres:///control",
            "postgres://host/",
            "postgres://host/control?unexpected=secret",
            "postgres://host/control?%73slmode=disable",
            "postgres://host/control?sslmode=disable&sslmode=require",
        ] {
            assert!(options(url).is_err());
        }
    }
}
