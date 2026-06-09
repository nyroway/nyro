use std::path::PathBuf;
use std::time::Duration;

#[derive(Debug, Clone, Copy, PartialEq, Eq, Default)]
pub enum StorageBackendKind {
    #[default]
    Sqlite,
    Postgres,
    Mysql,
}

#[derive(Debug, Clone)]
pub struct SqlStorageConfig {
    pub url: Option<String>,
    pub max_connections: u32,
    pub min_connections: u32,
    pub idle_timeout: Option<Duration>,
}

impl SqlStorageConfig {
    pub fn configured_url(&self) -> Option<String> {
        self.url
            .as_deref()
            .map(str::trim)
            .filter(|value| !value.is_empty())
            .map(ToString::to_string)
    }
}

impl Default for SqlStorageConfig {
    fn default() -> Self {
        Self {
            url: None,
            max_connections: 10,
            min_connections: 1,
            idle_timeout: Some(Duration::from_secs(300)),
        }
    }
}

#[derive(Debug, Clone)]
pub struct GatewayStorageConfig {
    pub backend: StorageBackendKind,
    /// Whether to run schema init + migrations on startup.
    /// Defaults to `true` (all backends). Set to `false` to skip DDL at startup
    /// and run migrations separately via `--migrate-only` (e.g. K8S init Job).
    pub migrate_on_start: bool,
    pub postgres: SqlStorageConfig,
    pub mysql: SqlStorageConfig,
}

impl Default for GatewayStorageConfig {
    fn default() -> Self {
        Self {
            backend: StorageBackendKind::Sqlite,
            migrate_on_start: true,
            postgres: SqlStorageConfig::default(),
            mysql: SqlStorageConfig::default(),
        }
    }
}

#[derive(Debug, Clone)]
pub struct GatewayConfig {
    pub proxy_host: String,
    pub proxy_port: u16,
    pub proxy_cors_origins: Vec<String>,
    pub data_dir: PathBuf,
    pub auth_key: Option<String>,
    pub storage: GatewayStorageConfig,
    /// How often to poll the shared DB for a config epoch change and reload
    /// `model_cache` when a change is detected. Set to `Duration::ZERO` to
    /// disable (default for desktop / single-process deployments).
    pub config_poll_interval: Duration,
}

impl Default for GatewayConfig {
    fn default() -> Self {
        Self {
            proxy_host: "127.0.0.1".to_string(),
            proxy_port: 19530,
            proxy_cors_origins: Vec::new(),
            data_dir: default_data_dir(),
            auth_key: None,
            storage: GatewayStorageConfig::default(),
            config_poll_interval: Duration::ZERO,
        }
    }
}

fn default_data_dir() -> PathBuf {
    dirs::home_dir()
        .unwrap_or_else(|| PathBuf::from("."))
        .join(".nyro")
}
