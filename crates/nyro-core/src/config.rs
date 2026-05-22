use std::path::PathBuf;
use std::time::Duration;

#[derive(Debug, Clone, Copy, PartialEq, Eq, Default)]
pub enum StorageBackendKind {
    #[default]
    Sqlite,
    Postgres,
}

#[derive(Debug, Clone)]
pub struct SqliteStorageConfig {
    pub migrate_on_start: bool,
}

impl Default for SqliteStorageConfig {
    fn default() -> Self {
        Self {
            migrate_on_start: true,
        }
    }
}

#[derive(Debug, Clone)]
pub struct SqlStorageConfig {
    pub url: Option<String>,
    pub max_connections: u32,
    pub min_connections: u32,
    pub acquire_timeout: Duration,
    pub idle_timeout: Option<Duration>,
    pub max_lifetime: Option<Duration>,
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
            acquire_timeout: Duration::from_secs(10),
            idle_timeout: Some(Duration::from_secs(300)),
            max_lifetime: Some(Duration::from_secs(1800)),
        }
    }
}

#[derive(Debug, Clone)]
pub struct GatewayStorageConfig {
    pub backend: StorageBackendKind,
    pub sqlite: SqliteStorageConfig,
    pub postgres: SqlStorageConfig,
}

impl Default for GatewayStorageConfig {
    fn default() -> Self {
        Self {
            backend: StorageBackendKind::Sqlite,
            sqlite: SqliteStorageConfig::default(),
            postgres: SqlStorageConfig::default(),
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
        }
    }
}

fn default_data_dir() -> PathBuf {
    dirs::home_dir()
        .unwrap_or_else(|| PathBuf::from("."))
        .join(".nyro")
}
