use std::time::Duration;

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum SqlBackendKind {
    Sqlite,
    Postgres,
    Mysql,
}

#[derive(Debug, Clone)]
pub struct SqlBackendConfig {
    pub url: String,
    pub max_connections: u32,
    pub min_connections: u32,
    pub acquire_timeout: Duration,
    pub idle_timeout: Option<Duration>,
}

impl SqlBackendConfig {
    pub fn with_url(url: impl Into<String>) -> Self {
        Self {
            url: url.into(),
            ..Self::default()
        }
    }
}

impl Default for SqlBackendConfig {
    fn default() -> Self {
        Self {
            url: String::new(),
            max_connections: 10,
            min_connections: 1,
            acquire_timeout: Duration::from_secs(10),
            idle_timeout: Some(Duration::from_secs(300)),
        }
    }
}
