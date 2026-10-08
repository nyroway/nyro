//! Dedicated resource databases. Full resource replacement is one transaction.
use super::{Kind, validate};
use crate::Error;
use nyro_config::{
    compile::{Identities, Snapshot},
    resources::Resources,
};
use serde_json::{Map, Value};
use sqlx::{Connection, PgConnection, Row, SqliteConnection};
use std::{path::Path, time::Duration};
pub const SQLITE_SCHEMA: &str = r#"CREATE TABLE upstreams (
    uid TEXT PRIMARY KEY,
    id TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL,
    kind TEXT NOT NULL,
    balance TEXT NOT NULL,
    targets TEXT NOT NULL
) STRICT;
CREATE TABLE models (
    uid TEXT PRIMARY KEY,
    id TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL,
    capability TEXT NOT NULL,
    upstream_uid TEXT NOT NULL REFERENCES upstreams(uid),
    access TEXT NOT NULL,
    execution TEXT NOT NULL,
    limits TEXT NOT NULL
) STRICT;
CREATE TABLE mcps (
    uid TEXT PRIMARY KEY,
    id TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL,
    upstream_uid TEXT NOT NULL REFERENCES upstreams(uid),
    allowed_tools TEXT NOT NULL,
    access TEXT NOT NULL,
    execution TEXT NOT NULL,
    limits TEXT NOT NULL
) STRICT;
CREATE TABLE consumers (
    uid TEXT PRIMARY KEY,
    id TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL,
    credentials TEXT NOT NULL,
    grants TEXT NOT NULL,
    limits TEXT NOT NULL
) STRICT;
PRAGMA user_version = 2;
"#;
pub const POSTGRES_SCHEMA: &str = r#"CREATE TABLE public.upstreams (
    uid TEXT PRIMARY KEY,
    id TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL,
    kind TEXT NOT NULL,
    balance TEXT NOT NULL,
    targets JSONB NOT NULL
);
CREATE TABLE public.models (
    uid TEXT PRIMARY KEY,
    id TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL,
    capability TEXT NOT NULL,
    upstream_uid TEXT NOT NULL REFERENCES public.upstreams(uid),
    access JSONB NOT NULL,
    execution JSONB NOT NULL,
    limits JSONB NOT NULL
);
CREATE TABLE public.mcps (
    uid TEXT PRIMARY KEY,
    id TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL,
    upstream_uid TEXT NOT NULL REFERENCES public.upstreams(uid),
    allowed_tools JSONB NOT NULL,
    access JSONB NOT NULL,
    execution JSONB NOT NULL,
    limits JSONB NOT NULL
);
CREATE TABLE public.consumers (
    uid TEXT PRIMARY KEY,
    id TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL,
    credentials JSONB NOT NULL,
    grants JSONB NOT NULL,
    limits JSONB NOT NULL
);
CREATE TABLE public.nyro_schema (version INTEGER PRIMARY KEY CHECK (version = 2));
INSERT INTO public.nyro_schema VALUES (2);
"#;
const KINDS: [Kind; 4] = [Kind::Upstreams, Kind::Models, Kind::Mcps, Kind::Consumers];
fn columns(kind: Kind) -> &'static [(&'static str, bool)] {
    match kind {
        Kind::Upstreams => &[
            ("uid", false),
            ("id", false),
            ("name", false),
            ("kind", false),
            ("balance", false),
            ("targets", true),
        ],
        Kind::Models => &[
            ("uid", false),
            ("id", false),
            ("name", false),
            ("capability", false),
            ("upstream_uid", false),
            ("access", true),
            ("execution", true),
            ("limits", true),
        ],
        Kind::Mcps => &[
            ("uid", false),
            ("id", false),
            ("name", false),
            ("upstream_uid", false),
            ("allowed_tools", true),
            ("access", true),
            ("execution", true),
            ("limits", true),
        ],
        Kind::Consumers => &[
            ("uid", false),
            ("id", false),
            ("name", false),
            ("credentials", true),
            ("grants", true),
            ("limits", true),
        ],
    }
}
pub struct Store {
    connection: Option<Backend>,
}
enum Backend {
    Sqlite(SqliteConnection),
    Postgres(PgConnection),
}
impl Store {
    pub async fn open(path: &Path) -> Result<Self, Error> {
        let mut options = std::fs::OpenOptions::new();
        options.write(true).create_new(true);
        #[cfg(unix)]
        {
            use std::os::unix::fs::OpenOptionsExt;
            options.mode(0o600);
        }
        match options.open(path) {
            Ok(_) => {}
            Err(e) if e.kind() == std::io::ErrorKind::AlreadyExists => {}
            Err(_) => return Err(Error::Storage),
        }
        let metadata = std::fs::metadata(path).map_err(|_| Error::Storage)?;
        if !metadata.is_file() {
            return Err(Error::Storage);
        }
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            if metadata.permissions().mode() & 0o077 != 0 {
                return Err(Error::Storage);
            }
        }
        let mut db = SqliteConnection::connect_with(
            &sqlx::sqlite::SqliteConnectOptions::new()
                .filename(path)
                .busy_timeout(Duration::from_millis(100))
                .foreign_keys(true),
        )
        .await
        .map_err(|_| Error::Storage)?;
        let tables: Vec<String> = sqlx::query_scalar("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name").fetch_all(&mut db).await.map_err(|_| Error::Schema)?;
        if !tables.is_empty() && tables != ["consumers", "mcps", "models", "upstreams"] {
            return Err(Error::Schema);
        }
        for sql in [
            "PRAGMA locking_mode = EXCLUSIVE",
            "PRAGMA journal_mode = DELETE",
            "PRAGMA synchronous = FULL",
        ] {
            sqlx::query(sql)
                .execute(&mut db)
                .await
                .map_err(|_| Error::Storage)?;
        }
        let mut tx = db
            .begin_with("BEGIN EXCLUSIVE")
            .await
            .map_err(|_| Error::Storage)?;
        if tables.is_empty() {
            sqlx::raw_sql(SQLITE_SCHEMA)
                .execute(&mut *tx)
                .await
                .map_err(|_| Error::Schema)?;
        }
        let version: i64 = sqlx::query_scalar("PRAGMA user_version")
            .fetch_one(&mut *tx)
            .await
            .map_err(|_| Error::Schema)?;
        if version != 2 {
            return Err(Error::Schema);
        }
        tx.commit().await.map_err(|_| Error::Storage)?;
        let mut store = Self {
            connection: Some(Backend::Sqlite(db)),
        };
        store.snapshot().await?;
        Ok(store)
    }
    pub async fn open_postgres(url: &str) -> Result<Self, Error> {
        tokio::time::timeout(Duration::from_secs(10), Self::connect_postgres(url))
            .await
            .map_err(|_| Error::Storage)?
    }
    async fn connect_postgres(url: &str) -> Result<Self, Error> {
        let options = super::postgres::options(url)?;
        let mut db =
            tokio::time::timeout(Duration::from_secs(5), PgConnection::connect_with(&options))
                .await
                .map_err(|_| Error::Storage)?
                .map_err(|_| Error::Storage)?;
        let encoding: String = sqlx::query_scalar("SHOW server_encoding")
            .fetch_one(&mut db)
            .await
            .map_err(|_| Error::Storage)?;
        if encoding != "UTF8" {
            return Err(Error::Schema);
        }
        let locked: bool =
            sqlx::query_scalar("SELECT pg_catalog.pg_try_advisory_lock(5645636632944079436)")
                .fetch_one(&mut db)
                .await
                .map_err(|_| Error::Storage)?;
        if !locked {
            return Err(Error::Storage);
        }
        let tables: Vec<String> = sqlx::query_scalar("SELECT tablename::text FROM pg_catalog.pg_tables WHERE schemaname='public' ORDER BY tablename").fetch_all(&mut db).await.map_err(|_| Error::Schema)?;
        if !tables.is_empty()
            && tables != ["consumers", "mcps", "models", "nyro_schema", "upstreams"]
        {
            return Err(Error::Schema);
        }
        let mut tx = db.begin().await.map_err(|_| Error::Storage)?;
        if tables.is_empty() {
            sqlx::raw_sql(POSTGRES_SCHEMA)
                .execute(&mut *tx)
                .await
                .map_err(|_| Error::Schema)?;
        }
        let versions: Vec<i32> = sqlx::query_scalar("SELECT version FROM public.nyro_schema")
            .fetch_all(&mut *tx)
            .await
            .map_err(|_| Error::Schema)?;
        if versions != [2] {
            return Err(Error::Schema);
        }
        tx.commit().await.map_err(|_| Error::Storage)?;
        let mut store = Self {
            connection: Some(Backend::Postgres(db)),
        };
        store.snapshot().await?;
        Ok(store)
    }
    pub async fn snapshot(&mut self) -> Result<Snapshot, Error> {
        let mut connection = self.connection.take().ok_or(Error::Storage)?;
        let result = tokio::time::timeout(Duration::from_secs(5), async {
            match &mut connection {
                Backend::Sqlite(db) => read_sqlite(db).await,
                Backend::Postgres(db) => {
                    let mut tx = db.begin().await.map_err(|_| Error::Storage)?;
                    sqlx::query("SET TRANSACTION ISOLATION LEVEL REPEATABLE READ, READ ONLY")
                        .execute(&mut *tx)
                        .await
                        .map_err(|_| Error::Storage)?;
                    let snapshot = read_postgres(&mut tx).await?;
                    tx.commit().await.map_err(|_| Error::Storage)?;
                    Ok(snapshot)
                }
            }
        })
        .await
        .unwrap_or(Err(Error::Storage));
        if result.is_ok() {
            self.connection = Some(connection);
        }
        result
    }
    pub async fn save(&mut self, snapshot: &Snapshot) -> Result<(), Error> {
        validate(snapshot)?;
        let mut connection = self.connection.take().ok_or(Error::Storage)?;
        let rows = encode(snapshot)?;
        let result = tokio::time::timeout(Duration::from_secs(5), async {
            match &mut connection {
                Backend::Sqlite(db) => save_sqlite(db, &rows).await,
                Backend::Postgres(db) => save_postgres(db, &rows).await,
            }
        })
        .await
        .unwrap_or(Err(Error::OutcomeUnknown));
        // Dropped/failed durable writes fail closed. Reopen to reconcile an uncertain commit.
        if result.is_ok() {
            self.connection = Some(connection);
        }
        result
    }
    pub async fn close(mut self) -> Result<(), Error> {
        match self.connection.take() {
            Some(Backend::Sqlite(db)) => db.close().await,
            Some(Backend::Postgres(db)) => db.close().await,
            None => return Ok(()),
        }
        .map_err(|_| Error::Storage)
    }
}
type Rows = Vec<(Kind, Vec<Map<String, Value>>)>;
fn encode(snapshot: &Snapshot) -> Result<Rows, Error> {
    let mut state = snapshot.clone();
    let resources = serde_json::to_value(&state.resources).map_err(|_| Error::Invalid)?;
    let mut result = Vec::new();
    for kind in KINDS {
        let mut rows = Vec::new();
        for item in resources[kind.name()].as_array().ok_or(Error::Invalid)? {
            let mut item = item.as_object().ok_or(Error::Invalid)?.clone();
            let id = item["id"].as_str().ok_or(Error::Invalid)?;
            item.insert(
                "uid".into(),
                Value::String(kind.identities(&mut state)[id].clone()),
            );
            if let Some(upstream) = item.remove("upstream") {
                item.insert(
                    "upstream_uid".into(),
                    Value::String(
                        state.identities.upstreams[upstream.as_str().ok_or(Error::Invalid)?]
                            .clone(),
                    ),
                );
            }
            if kind == Kind::Consumers {
                for (field, ids) in [
                    ("models", &state.identities.models),
                    ("mcps", &state.identities.mcps),
                ] {
                    for grant in item.get_mut("grants").unwrap()[field]
                        .as_array_mut()
                        .unwrap()
                    {
                        *grant = Value::String(ids[grant.as_str().ok_or(Error::Invalid)?].clone());
                    }
                }
            }
            rows.push(item);
        }
        result.push((kind, rows));
    }
    Ok(result)
}
fn decode(rows: Rows) -> Result<Snapshot, Error> {
    let mut state = Snapshot {
        resources: Resources::default(),
        identities: Identities::default(),
    };
    for (kind, items) in &rows {
        for item in items {
            kind.identities(&mut state).insert(
                item["id"].as_str().ok_or(Error::Schema)?.into(),
                item["uid"].as_str().ok_or(Error::Schema)?.into(),
            );
        }
    }
    let public = |kind, uid: &Value| -> Result<Value, Error> {
        let ids = match kind {
            Kind::Upstreams => &state.identities.upstreams,
            Kind::Models => &state.identities.models,
            Kind::Mcps => &state.identities.mcps,
            Kind::Consumers => &state.identities.consumers,
        };
        ids.iter()
            .find(|(_, v)| uid.as_str() == Some(v.as_str()))
            .map(|(k, _)| Value::String(k.clone()))
            .ok_or(Error::Schema)
    };
    let mut resources = serde_json::json!({"version":1});
    for (kind, mut items) in rows {
        for item in &mut items {
            item.remove("uid");
            if let Some(uid) = item.remove("upstream_uid") {
                item.insert("upstream".into(), public(Kind::Upstreams, &uid)?);
            }
            if kind == Kind::Consumers {
                for (field, target) in [("models", Kind::Models), ("mcps", Kind::Mcps)] {
                    for grant in item.get_mut("grants").ok_or(Error::Schema)?[field]
                        .as_array_mut()
                        .ok_or(Error::Schema)?
                    {
                        *grant = public(target, grant)?;
                    }
                }
            }
        }
        resources[kind.name()] = Value::Array(items.into_iter().map(Value::Object).collect());
    }
    state.resources = serde_json::from_value(resources).map_err(|_| Error::Schema)?;
    validate(&state).map_err(|_| Error::Schema)?;
    Ok(state)
}
async fn read_sqlite(db: &mut SqliteConnection) -> Result<Snapshot, Error> {
    let mut all = Vec::new();
    for kind in KINDS {
        let fields: Vec<_> = columns(kind)
            .iter()
            .map(|(name, _)| format!("CAST({name} AS TEXT) AS {name}"))
            .collect();
        let query = format!(
            "SELECT {} FROM {} ORDER BY id LIMIT 10001",
            fields.join(","),
            kind.name()
        );
        let rows = sqlx::query(&query)
            .fetch_all(&mut *db)
            .await
            .map_err(|_| Error::Schema)?;
        if rows.len() > 10000 {
            return Err(Error::Schema);
        }
        let mut items = Vec::new();
        for row in rows {
            let mut item = Map::new();
            for (name, json) in columns(kind) {
                let value: String = row.try_get(*name).map_err(|_| Error::Schema)?;
                if value.len() > crate::MAX_CONFIG_BYTES {
                    return Err(Error::Schema);
                }
                item.insert(
                    (*name).into(),
                    if *json {
                        serde_json::from_str(&value).map_err(|_| Error::Schema)?
                    } else {
                        Value::String(value)
                    },
                );
            }
            items.push(item);
        }
        all.push((kind, items));
    }
    decode(all)
}
async fn save_sqlite(db: &mut SqliteConnection, rows: &Rows) -> Result<(), Error> {
    let mut tx = db.begin().await.map_err(|_| Error::Storage)?;
    for table in ["consumers", "models", "mcps", "upstreams"] {
        sqlx::query(&format!("DELETE FROM {table}"))
            .execute(&mut *tx)
            .await
            .map_err(|_| Error::Storage)?;
    }
    for (kind, items) in rows {
        let columns = columns(*kind);
        let names: Vec<_> = columns.iter().map(|(name, _)| *name).collect();
        let parameters: Vec<_> = columns
            .iter()
            .enumerate()
            .map(|(i, json)| {
                let parameter = format!("${}", i + 1);
                let _ = json;
                parameter
            })
            .collect();
        let sql = format!(
            "INSERT INTO {} ({}) VALUES ({})",
            kind.name(),
            names.join(","),
            parameters.join(",")
        );
        for item in items {
            let mut query = sqlx::query(&sql);
            for (name, json) in columns {
                let value = &item[*name];
                query = query.bind(if *json {
                    value.to_string()
                } else {
                    value.as_str().ok_or(Error::Invalid)?.to_owned()
                });
            }
            query.execute(&mut *tx).await.map_err(|_| Error::Storage)?;
        }
    }
    tx.commit().await.map_err(|_| Error::OutcomeUnknown)
}
async fn read_postgres(db: &mut PgConnection) -> Result<Snapshot, Error> {
    let mut all = Vec::new();
    for kind in KINDS {
        let fields: Vec<_> = columns(kind)
            .iter()
            .map(|(name, _)| format!("CAST({name} AS TEXT) AS {name}"))
            .collect();
        let query = format!(
            "SELECT {} FROM public.{} ORDER BY id LIMIT 10001",
            fields.join(","),
            kind.name()
        );
        let rows = sqlx::query(&query)
            .fetch_all(&mut *db)
            .await
            .map_err(|_| Error::Schema)?;
        if rows.len() > 10000 {
            return Err(Error::Schema);
        }
        let mut items = Vec::new();
        for row in rows {
            let mut item = Map::new();
            for (name, json) in columns(kind) {
                let value: String = row.try_get(*name).map_err(|_| Error::Schema)?;
                if value.len() > crate::MAX_CONFIG_BYTES {
                    return Err(Error::Schema);
                }
                item.insert(
                    (*name).into(),
                    if *json {
                        serde_json::from_str(&value).map_err(|_| Error::Schema)?
                    } else {
                        Value::String(value)
                    },
                );
            }
            items.push(item);
        }
        all.push((kind, items));
    }
    decode(all)
}
async fn save_postgres(db: &mut PgConnection, rows: &Rows) -> Result<(), Error> {
    let mut tx = db.begin().await.map_err(|_| Error::Storage)?;
    for table in ["consumers", "models", "mcps", "upstreams"] {
        sqlx::query(&format!("DELETE FROM public.{table}"))
            .execute(&mut *tx)
            .await
            .map_err(|_| Error::Storage)?;
    }
    for (kind, items) in rows {
        let columns = columns(*kind);
        let names: Vec<_> = columns.iter().map(|(name, _)| *name).collect();
        let parameters: Vec<_> = columns
            .iter()
            .enumerate()
            .map(|(i, json)| {
                let parameter = format!("${}", i + 1);
                if json.1 {
                    format!("CAST({parameter} AS JSONB)")
                } else {
                    parameter
                }
            })
            .collect();
        let sql = format!(
            "INSERT INTO public.{} ({}) VALUES ({})",
            kind.name(),
            names.join(","),
            parameters.join(",")
        );
        for item in items {
            let mut query = sqlx::query(&sql);
            for (name, json) in columns {
                let value = &item[*name];
                query = query.bind(if *json {
                    value.to_string()
                } else {
                    value.as_str().ok_or(Error::Invalid)?.to_owned()
                });
            }
            query.execute(&mut *tx).await.map_err(|_| Error::Storage)?;
        }
    }
    tx.commit().await.map_err(|_| Error::OutcomeUnknown)
}
