mod postgres;
mod sqlite;

use crate::{Error, Snapshot, State};
use nyro_config::Config;
pub use postgres::POSTGRES_SCHEMA;
use std::path::Path;

/// Owns one dedicated database session. Snapshots contain credentials.
pub struct Store(Backend);
enum Backend {
    Sqlite(sqlite::Store),
    Postgres(postgres::Store),
}
impl Store {
    pub async fn open(path: &Path, seed: Option<&Config>) -> Result<Self, Error> {
        Ok(Self(Backend::Sqlite(
            sqlite::Store::open(path, seed).await?,
        )))
    }
    /// Open a dedicated PostgreSQL database with exclusive session ownership.
    /// TLS defaults to verify-full; disable is limited to loopback fixtures.
    pub async fn open_postgres(url: &str, seed: Option<&Config>) -> Result<Self, Error> {
        Ok(Self(Backend::Postgres(
            postgres::Store::open(url, seed).await?,
        )))
    }
    pub async fn state(&mut self) -> Result<State, Error> {
        match &mut self.0 {
            Backend::Sqlite(s) => s.state().await,
            Backend::Postgres(s) => s.state().await,
        }
    }
    pub async fn save(
        &mut self,
        expected_revision: u64,
        config: &Config,
    ) -> Result<Snapshot, Error> {
        match &mut self.0 {
            Backend::Sqlite(s) => s.save(expected_revision, config).await,
            Backend::Postgres(s) => s.save(expected_revision, config).await,
        }
    }
    pub async fn publish(&mut self, revision: u64) -> Result<Snapshot, Error> {
        match &mut self.0 {
            Backend::Sqlite(s) => s.publish(revision).await,
            Backend::Postgres(s) => s.publish(revision).await,
        }
    }
    pub async fn close(self) -> Result<(), Error> {
        match self.0 {
            Backend::Sqlite(s) => s.close().await,
            Backend::Postgres(s) => s.close().await,
        }
    }
}
