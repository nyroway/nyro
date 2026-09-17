use crate::{Error, MAX_CONFIG_BYTES, Snapshot, State};
use nyro_config::Config;
use sqlx::{Connection, Row, SqliteConnection, sqlite::SqliteConnectOptions};
use std::{fs::OpenOptions, io::ErrorKind, path::Path, time::Duration};

const SCHEMA: &str = "CREATE TABLE nyro_control_state (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    draft_revision INTEGER NOT NULL CHECK (draft_revision > 0),
    draft_json TEXT NOT NULL CHECK (length(CAST(draft_json AS BLOB)) BETWEEN 1 AND 1048576),
    published_revision INTEGER NOT NULL CHECK (published_revision > 0 AND published_revision <= draft_revision),
    published_json TEXT NOT NULL CHECK (length(CAST(published_json AS BLOB)) BETWEEN 1 AND 1048576)
) STRICT";

/// One process owns the dedicated database until this connection closes.
/// Snapshots contain credentials; callers must protect their serialized output.
pub struct Store {
    connection: SqliteConnection,
}

impl Store {
    /// Initialize an empty database with an explicit seed, or reopen its durable snapshots.
    /// Existing control databases reject seeds; unrelated databases are never adopted.
    pub async fn open(path: &Path, seed: Option<&Config>) -> Result<Self, Error> {
        let mut file = OpenOptions::new();
        file.write(true).create_new(true);
        #[cfg(unix)]
        {
            use std::os::unix::fs::OpenOptionsExt;
            file.mode(0o600);
        }
        match file.open(path) {
            Ok(_) => {}
            Err(error) if error.kind() == ErrorKind::AlreadyExists => {}
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
        let mut connection = SqliteConnection::connect_with(
            &SqliteConnectOptions::new()
                .filename(path)
                .busy_timeout(Duration::from_millis(100)),
        )
        .await
        .map_err(|_| Error::Storage)?;

        // Check ownership before changing persistent SQLite settings.
        let initialized = check_schema(&mut connection).await?;
        let seed_json = match (initialized, seed) {
            (true, Some(_)) => return Err(Error::AlreadyInitialized),
            (false, None) => return Err(Error::SeedRequired),
            (false, Some(config)) => Some(encode(config)?),
            (true, None) => None,
        };
        sqlx::query("PRAGMA locking_mode = EXCLUSIVE")
            .execute(&mut connection)
            .await
            .map_err(|_| Error::Storage)?;
        sqlx::query("PRAGMA journal_mode = DELETE")
            .execute(&mut connection)
            .await
            .map_err(|_| Error::Storage)?;
        sqlx::query("PRAGMA synchronous = FULL")
            .execute(&mut connection)
            .await
            .map_err(|_| Error::Storage)?;
        let mut transaction = connection
            .begin_with("BEGIN EXCLUSIVE")
            .await
            .map_err(|_| Error::Storage)?;
        // Another process may have initialized the file between inspection and locking.
        if check_schema(&mut transaction).await? != initialized {
            return Err(Error::AlreadyInitialized);
        }
        if let Some(json) = seed_json {
            sqlx::query(SCHEMA)
                .execute(&mut *transaction)
                .await
                .map_err(|_| Error::Storage)?;
            sqlx::query(
                "INSERT INTO nyro_control_state \
                 (singleton, draft_revision, draft_json, published_revision, published_json) \
                 VALUES (1, 1, ?, 1, ?)",
            )
            .bind(&json)
            .bind(&json)
            .execute(&mut *transaction)
            .await
            .map_err(|_| Error::Storage)?;
            sqlx::query("PRAGMA user_version = 1")
                .execute(&mut *transaction)
                .await
                .map_err(|_| Error::Storage)?;
        }
        transaction.commit().await.map_err(|_| Error::Storage)?;
        // EXCLUSIVE locking mode retains the lock across subsequent autocommits.
        let mut store = Self { connection };
        store.state().await?;
        Ok(store)
    }

    /// Read and validate both durable snapshots, including schema and revision invariants.
    pub async fn state(&mut self) -> Result<State, Error> {
        if !check_schema(&mut self.connection).await? {
            return Err(Error::Schema);
        }
        // Bound text reads even if another tool previously bypassed CHECK constraints.
        let count: i64 = sqlx::query_scalar("SELECT COUNT(*) FROM nyro_control_state")
            .fetch_one(&mut self.connection)
            .await
            .map_err(|_| Error::Schema)?;
        if count != 1 {
            return Err(Error::Schema);
        }
        let row = sqlx::query(
            "SELECT singleton, draft_revision, published_revision, \
             length(CAST(draft_json AS BLOB)) AS draft_size, \
             length(CAST(published_json AS BLOB)) AS published_size FROM nyro_control_state",
        )
        .fetch_one(&mut self.connection)
        .await
        .map_err(|_| Error::Schema)?;
        let singleton: i64 = row.try_get("singleton").map_err(|_| Error::Schema)?;
        let draft_revision: i64 = row.try_get("draft_revision").map_err(|_| Error::Schema)?;
        let published_revision: i64 = row
            .try_get("published_revision")
            .map_err(|_| Error::Schema)?;
        let draft_size: i64 = row.try_get("draft_size").map_err(|_| Error::Schema)?;
        let published_size: i64 = row.try_get("published_size").map_err(|_| Error::Schema)?;
        if singleton != 1
            || published_revision <= 0
            || draft_revision < published_revision
            || !(1..=MAX_CONFIG_BYTES as i64).contains(&draft_size)
            || !(1..=MAX_CONFIG_BYTES as i64).contains(&published_size)
        {
            return Err(Error::Schema);
        }
        let (draft_json, published_json): (String, String) =
            sqlx::query_as("SELECT draft_json, published_json FROM nyro_control_state")
                .fetch_one(&mut self.connection)
                .await
                .map_err(|_| Error::Schema)?;
        if draft_revision == published_revision && draft_json != published_json {
            return Err(Error::Schema);
        }
        Ok(State {
            draft: Snapshot {
                revision: draft_revision as u64,
                config: decode(&draft_json)?,
            },
            published: Snapshot {
                revision: published_revision as u64,
                config: decode(&published_json)?,
            },
        })
    }

    /// Save a validated draft without changing the published restart target.
    pub async fn save(
        &mut self,
        expected_revision: u64,
        config: &Config,
    ) -> Result<Snapshot, Error> {
        let json = encode(config)?;
        let state = self.state().await?;
        if state.draft.revision != expected_revision {
            return Err(Error::Conflict);
        }
        let revision = i64::try_from(expected_revision)
            .ok()
            .and_then(|value| value.checked_add(1))
            .ok_or(Error::Conflict)?;
        sqlx::query(
            "UPDATE nyro_control_state SET draft_revision = ?, draft_json = ? WHERE singleton = 1",
        )
        .bind(revision)
        .bind(json)
        .execute(&mut self.connection)
        .await
        .map_err(|_| Error::Storage)?;
        Ok(Snapshot {
            revision: revision as u64,
            config: config.clone(),
        })
    }

    /// Durably publish the current draft. Repeating its revision is idempotent.
    pub async fn publish(&mut self, revision: u64) -> Result<Snapshot, Error> {
        let state = self.state().await?;
        if state.draft.revision != revision {
            return Err(Error::Conflict);
        }
        if state.published.revision != revision {
            sqlx::query(
                "UPDATE nyro_control_state SET published_revision = draft_revision, \
                 published_json = draft_json WHERE singleton = 1",
            )
            .execute(&mut self.connection)
            .await
            .map_err(|_| Error::Storage)?;
        }
        Ok(state.draft)
    }

    /// Close SQLite and release exclusive ownership.
    pub async fn close(self) -> Result<(), Error> {
        self.connection.close().await.map_err(|_| Error::Storage)
    }
}

async fn check_schema(connection: &mut SqliteConnection) -> Result<bool, Error> {
    let version: i64 = sqlx::query_scalar("PRAGMA user_version")
        .fetch_one(&mut *connection)
        .await
        .map_err(|_| Error::Storage)?;
    let objects: Vec<(String, String, Option<String>)> =
        sqlx::query_as("SELECT type, name, sql FROM sqlite_schema")
            .fetch_all(connection)
            .await
            .map_err(|_| Error::Storage)?;
    if objects.is_empty() && version == 0 {
        return Ok(false);
    }
    if version == 1
        && objects.len() == 1
        && objects[0].0 == "table"
        && objects[0].1 == "nyro_control_state"
        && objects[0].2.as_deref() == Some(SCHEMA)
    {
        Ok(true)
    } else {
        Err(Error::Schema)
    }
}

pub(super) fn encode(config: &Config) -> Result<String, Error> {
    config.validate().map_err(|_| Error::Invalid)?;
    let json = serde_json::to_string(config).map_err(|_| Error::Invalid)?;
    if json.len() > MAX_CONFIG_BYTES {
        return Err(Error::Invalid);
    }
    Ok(json)
}

pub(super) fn decode(json: &str) -> Result<Config, Error> {
    let config: Config = serde_json::from_str(json).map_err(|_| Error::Schema)?;
    config.validate().map_err(|_| Error::Schema)?;
    Ok(config)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[tokio::test]
    async fn sqlite_write_failures_preserve_draft_and_published_snapshots() {
        let seed = Config::from_yaml(
            r#"
llm:
  providers:
    p: {kind: openai, base_url: 'https://example.test/v1'}
  models:
    chat:
      provider: p
      upstream_model: example
      workloads: [chat]
      allow_anonymous: true
"#,
        )
        .unwrap();
        let temp = tempfile::tempdir().unwrap();
        let path = temp.path().join("control.db");
        let mut store = Store::open(&path, Some(&seed)).await.unwrap();
        let mut draft = seed.clone();
        draft.limit.concurrency = 17;
        store.save(1, &draft).await.unwrap();
        let before = serde_json::to_value(store.state().await.unwrap()).unwrap();
        sqlx::query("PRAGMA query_only = ON")
            .execute(&mut store.connection)
            .await
            .unwrap();

        draft.limit.concurrency = 23;
        assert!(matches!(store.save(2, &draft).await, Err(Error::Storage)));
        assert_eq!(
            serde_json::to_value(store.state().await.unwrap()).unwrap(),
            before
        );
        assert!(matches!(store.publish(2).await, Err(Error::Storage)));
        assert_eq!(
            serde_json::to_value(store.state().await.unwrap()).unwrap(),
            before
        );
        store.close().await.unwrap();

        let mut reopened = Store::open(&path, None).await.unwrap();
        assert_eq!(
            serde_json::to_value(reopened.state().await.unwrap()).unwrap(),
            before
        );
        reopened.close().await.unwrap();
    }
}
