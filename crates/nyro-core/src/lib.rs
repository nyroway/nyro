pub mod admin;
pub mod auth;
pub mod config;
pub mod db;
pub mod error;
pub mod integrations;
pub mod logging;
pub mod protocol;
pub mod provider;
pub mod proxy;
pub mod router;
pub mod storage;

use std::collections::HashMap;
use std::future::Future;
use std::sync::Arc;
use std::time::{Duration, Instant};

use anyhow::Context;
use sqlx::{MySql, Pool, Postgres, SqlitePool};
use tokio::sync::mpsc;

use crate::auth::types::AuthSession;
use crate::router::health::HealthRegistry;
use config::{GatewayConfig, SqlStorageConfig, StorageBackendKind};
use logging::LogEntry;
use storage::sql::config::SqlBackendConfig;
use storage::{DynStorage, MysqlStorage, PostgresStorage, SqliteStorage};

async fn shutdown_pending() {
    std::future::pending::<()>().await;
}

#[derive(Clone, Debug)]
pub struct CapabilityCacheEntry {
    pub capabilities: Vec<String>,
    pub cached_at: Instant,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum RuntimeStorageKind {
    Memory,
    Sqlite,
    Postgres,
    Mysql,
}

#[derive(Clone)]
pub struct Gateway {
    pub config: GatewayConfig,
    pub storage: DynStorage,
    pub storage_kind: RuntimeStorageKind,
    pub http_client: reqwest::Client,
    proxy_client_cache: Arc<tokio::sync::RwLock<Option<ProxyClientCache>>>,
    pub model_cache: Arc<tokio::sync::RwLock<router::ModelCache>>,
    pub health_registry: Arc<HealthRegistry>,
    pub ollama_capability_cache: Arc<tokio::sync::RwLock<HashMap<String, CapabilityCacheEntry>>>,
    pub log_tx: mpsc::Sender<LogEntry>,
    pub(crate) auth_sessions: Arc<tokio::sync::RwLock<HashMap<String, AuthSession>>>,
    #[allow(dead_code)]
    sqlite_pool: Option<SqlitePool>,
    #[allow(dead_code)]
    postgres_pool: Option<Pool<Postgres>>,
    #[allow(dead_code)]
    mysql_pool: Option<Pool<MySql>>,
}

#[derive(Clone)]
struct ProxyClientCache {
    cache_key: String,
    client: reqwest::Client,
}

impl Gateway {
    pub async fn new(config: GatewayConfig) -> anyhow::Result<(Self, mpsc::Receiver<LogEntry>)> {
        let (storage_kind, storage, sqlite_pool, postgres_pool, mysql_pool): (
            RuntimeStorageKind,
            DynStorage,
            Option<SqlitePool>,
            Option<Pool<Postgres>>,
            Option<Pool<MySql>>,
        ) = match config.storage.backend {
            StorageBackendKind::Sqlite => {
                let sqlite_storage = if config.storage.migrate_on_start {
                    SqliteStorage::from_config(&config).await?
                } else {
                    let pool = db::init_pool(&config.data_dir).await?;
                    SqliteStorage::from_pool(pool)
                };
                let pool = sqlite_storage.pool().clone();
                (
                    RuntimeStorageKind::Sqlite,
                    Arc::new(sqlite_storage),
                    Some(pool),
                    None,
                    None,
                )
            }
            StorageBackendKind::Postgres => {
                let backend_config = to_sql_backend_config(&config.storage.postgres, "postgres")?;
                let postgres_storage = PostgresStorage::connect(backend_config).await?;
                let pool = postgres_storage.pool().clone();
                (
                    RuntimeStorageKind::Postgres,
                    Arc::new(postgres_storage),
                    None,
                    Some(pool),
                    None,
                )
            }
            StorageBackendKind::Mysql => {
                let backend_config = to_sql_backend_config(&config.storage.mysql, "mysql")?;
                let mysql_storage = MysqlStorage::connect(backend_config).await?;
                let pool = mysql_storage.pool().clone();
                (
                    RuntimeStorageKind::Mysql,
                    Arc::new(mysql_storage),
                    None,
                    None,
                    Some(pool),
                )
            }
        };

        storage.bootstrap().init().await?;
        if config.storage.migrate_on_start
            && !matches!(config.storage.backend, StorageBackendKind::Sqlite)
        {
            storage.bootstrap().migrate().await?;
        }
        let health = storage.bootstrap().health().await?;
        if !health.can_connect {
            anyhow::bail!("selected storage backend is not reachable");
        }

        Self::from_storage_with_kind(
            config,
            storage,
            storage_kind,
            sqlite_pool,
            postgres_pool,
            mysql_pool,
        )
        .await
    }

    pub async fn from_storage(
        config: GatewayConfig,
        storage: DynStorage,
    ) -> anyhow::Result<(Self, mpsc::Receiver<LogEntry>)> {
        Self::from_storage_with_kind(
            config,
            storage,
            RuntimeStorageKind::Memory,
            None,
            None,
            None,
        )
        .await
    }

    async fn from_storage_with_kind(
        config: GatewayConfig,
        storage: DynStorage,
        storage_kind: RuntimeStorageKind,
        sqlite_pool: Option<SqlitePool>,
        postgres_pool: Option<Pool<Postgres>>,
        mysql_pool: Option<Pool<MySql>>,
    ) -> anyhow::Result<(Self, mpsc::Receiver<LogEntry>)> {
        let http_client = reqwest::Client::builder()
            .timeout(std::time::Duration::from_secs(300))
            .build()?;

        let model_cache = Arc::new(tokio::sync::RwLock::new(
            router::ModelCache::load(storage.snapshots()).await?,
        ));
        let health_registry = Arc::new(HealthRegistry::new());
        let ollama_capability_cache = Arc::new(tokio::sync::RwLock::new(HashMap::new()));

        let (log_tx, log_rx) = mpsc::channel(1024);

        let gw = Self {
            config,
            storage,
            storage_kind,
            http_client,
            proxy_client_cache: Arc::new(tokio::sync::RwLock::new(None)),
            model_cache,
            health_registry,
            ollama_capability_cache,
            log_tx,
            auth_sessions: Arc::new(tokio::sync::RwLock::new(HashMap::new())),
            sqlite_pool,
            postgres_pool,
            mysql_pool,
        };

        {
            let data_dir = gw.config.data_dir.clone();
            let http_client = gw.http_client.clone();
            tokio::spawn(async move {
                admin::refresh_models_dev_runtime_cache_on_startup(data_dir, http_client).await;
            });
        }

        {
            let gw_refresh = gw.clone();
            tokio::spawn(async move {
                let mut interval = tokio::time::interval(Duration::from_secs(120));
                loop {
                    interval.tick().await;
                    if let Err(error) = gw_refresh.admin().refresh_oauth_providers().await {
                        tracing::warn!("background oauth refresh skipped: {error}");
                    }
                    if let Err(error) = gw_refresh.admin().cleanup_auth_sessions().await {
                        tracing::warn!("auth session cleanup skipped: {error}");
                    }
                }
            });
        }

        if !gw.config.config_poll_interval.is_zero() {
            let gw_poll = gw.clone();
            let poll_interval = gw.config.config_poll_interval;
            tokio::spawn(async move {
                let mut known_epoch: i64 = gw_poll
                    .storage
                    .settings()
                    .get(admin::settings::CONFIG_EPOCH_KEY)
                    .await
                    .ok()
                    .flatten()
                    .as_deref()
                    .and_then(|v| v.parse().ok())
                    .unwrap_or(0);

                let mut interval = tokio::time::interval(poll_interval);
                interval.tick().await; // consume the immediate first tick
                loop {
                    interval.tick().await;
                    let current: i64 = match gw_poll
                        .storage
                        .settings()
                        .get(admin::settings::CONFIG_EPOCH_KEY)
                        .await
                    {
                        Ok(val) => val.as_deref().and_then(|v| v.parse().ok()).unwrap_or(0),
                        Err(error) => {
                            tracing::warn!("config epoch poll failed: {error}");
                            continue;
                        }
                    };

                    if current > known_epoch {
                        known_epoch = current;
                        if let Err(error) = gw_poll
                            .model_cache
                            .write()
                            .await
                            .reload(gw_poll.storage.snapshots())
                            .await
                        {
                            tracing::warn!("config epoch reload failed: {error}");
                        } else {
                            tracing::debug!("model_cache reloaded (epoch={current})");
                        }
                    }
                }
            });
        }

        Ok((gw, log_rx))
    }

    pub async fn start_proxy(&self) -> anyhow::Result<()> {
        self.start_proxy_with_shutdown(shutdown_pending()).await
    }

    pub async fn start_proxy_with_shutdown(
        &self,
        shutdown: impl Future<Output = ()> + Send + 'static,
    ) -> anyhow::Result<()> {
        let router = proxy::server::create_router(self.clone());
        let addr = format!("{}:{}", self.config.proxy_host, self.config.proxy_port);
        let listener = tokio::net::TcpListener::bind(&addr).await?;
        tracing::info!("proxy listening on {}", addr);
        axum::serve(listener, router)
            .with_graceful_shutdown(shutdown)
            .await?;
        Ok(())
    }

    pub fn admin(&self) -> admin::AdminService {
        admin::AdminService::new(self.clone())
    }

    pub async fn http_client_for_provider(
        &self,
        use_proxy: bool,
    ) -> anyhow::Result<reqwest::Client> {
        if !use_proxy {
            return Ok(self.http_client.clone());
        }

        let enabled = self
            .storage
            .settings()
            .get("proxy_enabled")
            .await?
            .as_deref()
            .map(parse_bool_setting)
            .unwrap_or(false);
        if !enabled {
            return Ok(self.http_client.clone());
        }

        let proxy_url = self
            .storage
            .settings()
            .get("proxy_url")
            .await?
            .unwrap_or_default()
            .trim()
            .to_string();
        if proxy_url.is_empty() {
            anyhow::bail!("proxy_url is empty");
        }

        let force_http1 = self
            .storage
            .settings()
            .get("proxy_force_http1")
            .await?
            .as_deref()
            .map(parse_bool_setting)
            .unwrap_or(false);

        let cache_key = format!("{proxy_url}|{force_http1}");
        if let Some(cached) = self.proxy_client_cache.read().await.clone()
            && cached.cache_key == cache_key
        {
            return Ok(cached.client);
        }

        let mut builder = reqwest::Client::builder().timeout(std::time::Duration::from_secs(300));
        if force_http1 {
            builder = builder.http1_only();
        }
        let client = builder.proxy(reqwest::Proxy::all(&proxy_url)?).build()?;

        *self.proxy_client_cache.write().await = Some(ProxyClientCache {
            cache_key,
            client: client.clone(),
        });
        Ok(client)
    }

    pub async fn get_ollama_capabilities_cached(
        &self,
        provider_id: &str,
        model: &str,
        ttl: Duration,
    ) -> Option<Vec<String>> {
        let key = format!("{provider_id}:{model}");
        let cache = self.ollama_capability_cache.read().await;
        cache.get(&key).and_then(|entry| {
            if entry.cached_at.elapsed() < ttl {
                Some(entry.capabilities.clone())
            } else {
                None
            }
        })
    }

    pub async fn set_ollama_capabilities_cache(
        &self,
        provider_id: &str,
        model: &str,
        capabilities: Vec<String>,
    ) {
        let key = format!("{provider_id}:{model}");
        let mut cache = self.ollama_capability_cache.write().await;
        cache.insert(
            key,
            CapabilityCacheEntry {
                capabilities,
                cached_at: Instant::now(),
            },
        );
    }

    pub async fn clear_ollama_capability_cache_for_provider(&self, provider_id: &str) {
        let prefix = format!("{provider_id}:");
        let mut cache = self.ollama_capability_cache.write().await;
        cache.retain(|k, _| !k.starts_with(&prefix));
    }
}

fn parse_bool_setting(value: &str) -> bool {
    matches!(
        value.trim().to_ascii_lowercase().as_str(),
        "1" | "true" | "yes" | "on"
    )
}

fn to_sql_backend_config(
    config: &SqlStorageConfig,
    backend: &str,
) -> anyhow::Result<SqlBackendConfig> {
    let url = config
        .configured_url()
        .with_context(|| format!("{backend} backend selected but storage url is empty"))?;
    Ok(SqlBackendConfig {
        url,
        max_connections: config.max_connections,
        min_connections: config.min_connections,
        idle_timeout: config.idle_timeout,
        ..Default::default()
    })
}

#[cfg(test)]
mod tests {
    use super::*;
    use tokio::sync::oneshot;

    #[tokio::test]
    async fn proxy_server_stops_when_shutdown_future_resolves() {
        let config = GatewayConfig {
            proxy_host: "127.0.0.1".to_string(),
            proxy_port: 0,
            storage: config::GatewayStorageConfig::default(),
            ..Default::default()
        };
        let storage: DynStorage = Arc::new(storage::MemoryStorage::new(vec![], vec![], vec![]));
        let (gateway, _log_rx) = Gateway::from_storage(config, storage).await.unwrap();
        let (shutdown_tx, shutdown_rx) = oneshot::channel::<()>();

        let server = tokio::spawn(async move {
            gateway
                .start_proxy_with_shutdown(async move {
                    let _ = shutdown_rx.await;
                })
                .await
        });

        shutdown_tx.send(()).unwrap();

        tokio::time::timeout(Duration::from_secs(1), server)
            .await
            .expect("proxy server should stop after shutdown")
            .expect("proxy server task should complete")
            .expect("proxy server should exit cleanly");
    }
}
