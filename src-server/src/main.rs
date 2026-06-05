use std::path::PathBuf;
use std::sync::Arc;
use std::time::Duration;

use axum::body::Body;
use axum::http::{HeaderValue, Method, StatusCode, header};
use axum::response::Response;
use clap::Parser;
use nyro_core::{
    Gateway,
    config::{
        GatewayConfig, GatewayStorageConfig, SqlStorageConfig, SqliteStorageConfig,
        StorageBackendKind,
    },
    logging,
    storage::MemoryStorage,
};
use rust_embed::RustEmbed;
use tokio::sync::broadcast;
use tower_http::cors::{AllowOrigin, CorsLayer};
use tower_http::services::{ServeDir, ServeFile};

mod admin_routes;
mod yaml_config;

#[derive(RustEmbed)]
#[folder = "../webui/dist/"]
struct WebUiAssets;

#[derive(Parser)]
#[command(name = "nyro-server", version, about = "Nyro AI Gateway — Server Mode")]
struct Args {
    // ── Server ────────────────────────────────────────────────────────────────
    #[arg(
        long,
        default_value = "127.0.0.1",
        env = "NYRO_PROXY_HOST",
        help_heading = "Server"
    )]
    proxy_host: String,

    #[arg(
        long,
        default_value_t = 19530,
        env = "NYRO_PROXY_PORT",
        help_heading = "Server"
    )]
    proxy_port: u16,

    #[arg(
        long,
        default_value = "127.0.0.1",
        env = "NYRO_ADMIN_HOST",
        help_heading = "Server"
    )]
    admin_host: String,

    #[arg(
        long,
        default_value_t = 19531,
        env = "NYRO_ADMIN_PORT",
        help_heading = "Server"
    )]
    admin_port: u16,

    #[arg(
        long,
        env = "NYRO_ADMIN_TOKEN",
        help = "Bearer token for admin API authentication",
        help_heading = "Server"
    )]
    admin_token: Option<String>,

    #[arg(
        long,
        default_value = "info",
        env = "NYRO_LOG_LEVEL",
        value_parser = ["error", "warn", "info", "debug", "trace"],
        help_heading = "Server"
    )]
    log_level: String,

    // ── Advanced (CORS) ───────────────────────────────────────────────────────
    #[arg(
        long = "admin-cors-origin",
        action = clap::ArgAction::Append,
        help = "Allowed CORS origin for admin API (repeatable, use '*' for any)",
        help_heading = "Advanced"
    )]
    admin_cors_origins: Vec<String>,

    #[arg(
        long = "proxy-cors-origin",
        action = clap::ArgAction::Append,
        help = "Allowed CORS origin for proxy API (repeatable, use '*' for any)",
        help_heading = "Advanced"
    )]
    proxy_cors_origins: Vec<String>,

    // ── Storage ───────────────────────────────────────────────────────────────
    #[arg(
        long,
        default_value = "~/.nyro",
        env = "NYRO_DATA_DIR",
        help_heading = "Storage"
    )]
    data_dir: String,

    #[arg(long, value_parser = ["sqlite", "postgres", "mysql"], default_value = "sqlite",
          env = "NYRO_STORAGE_BACKEND", help_heading = "Storage")]
    storage_backend: String,

    #[arg(
        long,
        default_value = "true",
        action = clap::ArgAction::Set,
        help = "Run migrations on startup (true/false)",
        help_heading = "Storage"
    )]
    migrate_on_start: bool,

    #[arg(
        long,
        env = "NYRO_POSTGRES_DSN",
        help = "PostgreSQL connection string (required when --storage-backend=postgres)",
        help_heading = "Storage"
    )]
    postgres_dsn: Option<String>,

    #[arg(
        long,
        default_value_t = 10,
        help = "Postgres: max connection pool size",
        help_heading = "Storage"
    )]
    postgres_max_connections: u32,

    #[arg(
        long,
        default_value_t = 1,
        help = "Postgres: min connection pool size",
        help_heading = "Storage"
    )]
    postgres_min_connections: u32,

    #[arg(
        long,
        help = "Postgres: idle connection timeout (seconds)",
        help_heading = "Storage"
    )]
    postgres_idle_timeout: Option<u64>,

    // ── MySQL ────────────────────────────────────────────────────────────────
    #[arg(
        long,
        env = "NYRO_MYSQL_DSN",
        help = "MySQL connection string (required when --storage-backend=mysql)",
        help_heading = "Storage"
    )]
    mysql_dsn: Option<String>,

    #[arg(
        long,
        default_value_t = 10,
        help = "MySQL: max connection pool size",
        help_heading = "Storage"
    )]
    mysql_max_connections: u32,

    #[arg(
        long,
        default_value_t = 1,
        help = "MySQL: min connection pool size",
        help_heading = "Storage"
    )]
    mysql_min_connections: u32,

    #[arg(
        long,
        help = "MySQL: idle connection timeout (seconds)",
        help_heading = "Storage"
    )]
    mysql_idle_timeout: Option<u64>,

    // ── Multi-replica ─────────────────────────────────────────────────────────
    #[arg(
        long,
        default_value_t = 3,
        env = "NYRO_CONFIG_POLL_INTERVAL",
        help = "Seconds between config epoch polls for multi-replica cache sync (0 = disabled)",
        help_heading = "Multi-replica"
    )]
    config_poll_interval: u64,

    #[arg(
        long,
        env = "NYRO_WEBUI_DIR",
        help = "Serve WebUI from this directory instead of the embedded assets (optional)",
        help_heading = "Multi-replica"
    )]
    webui_dir: Option<PathBuf>,

    // ── Standalone ────────────────────────────────────────────────────────────
    #[arg(
        long = "config",
        short = 'c',
        help = "Path to YAML config file for standalone mode (no DB, no admin API)",
        help_heading = "Standalone"
    )]
    config_file: Option<String>,
}

#[tokio::main]
async fn main() -> anyhow::Result<()> {
    let args = Args::parse();

    let filter = format!("nyro={level},tower_http={level}", level = args.log_level);
    tracing_subscriber::fmt().with_env_filter(filter).init();

    if let Some(ref config_path) = args.config_file {
        return run_standalone(config_path, &args).await;
    }

    run_full(&args).await
}

async fn run_standalone(config_path: &str, args: &Args) -> anyhow::Result<()> {
    tracing::info!("standalone mode: loading {config_path}");
    let yaml = yaml_config::YamlConfig::load(config_path)?;

    // Standalone mode: proxy address comes exclusively from YAML
    let proxy_host = yaml.server.proxy_host.clone();
    let proxy_port = yaml.server.proxy_port;

    let providers = yaml_config::build_providers(&yaml);
    let models = yaml_config::build_models(&yaml, &providers);
    let settings: Vec<(String, String)> = yaml.settings.into_iter().collect();

    tracing::info!(
        "loaded {} providers, {} models from YAML",
        providers.len(),
        models.len()
    );

    let storage: nyro_core::storage::DynStorage =
        Arc::new(MemoryStorage::new(providers, models, settings));

    let data_dir = shellexpand::tilde(&args.data_dir).to_string();
    let proxy_cors_origins = if args.proxy_cors_origins.is_empty() {
        default_local_origins(&[proxy_port])
    } else {
        args.proxy_cors_origins.clone()
    };

    let config = GatewayConfig {
        proxy_host: proxy_host.clone(),
        proxy_port,
        proxy_cors_origins,
        data_dir: PathBuf::from(data_dir),
        storage: GatewayStorageConfig::default(),
        ..Default::default()
    };

    let (gateway, log_rx) = Gateway::from_storage(config, storage).await?;
    let storage_for_logs = gateway.storage.clone();

    tokio::spawn(async move {
        logging::run_collector(log_rx, storage_for_logs).await;
    });

    tracing::info!("proxy  → http://{}:{}", proxy_host, proxy_port);
    tracing::info!("standalone mode: admin API and WebUI are disabled");

    gateway.start_proxy_with_shutdown(shutdown_signal()).await?;
    Ok(())
}

async fn run_full(args: &Args) -> anyhow::Result<()> {
    let data_dir = shellexpand::tilde(&args.data_dir).to_string();
    let admin_token = args.admin_token.clone().filter(|t| !t.trim().is_empty());

    if !is_loopback_host(&args.admin_host) && admin_token.is_none() {
        anyhow::bail!(
            "--admin-token is required when --admin-host is not loopback (localhost/127.0.0.1/::1)"
        );
    }

    let admin_cors_origins = if args.admin_cors_origins.is_empty() {
        default_local_origins(&[args.admin_port])
    } else {
        args.admin_cors_origins.clone()
    };
    let proxy_cors_origins = if args.proxy_cors_origins.is_empty() {
        default_local_origins(&[args.proxy_port, args.admin_port])
    } else {
        args.proxy_cors_origins.clone()
    };

    let config = GatewayConfig {
        proxy_host: args.proxy_host.clone(),
        proxy_port: args.proxy_port,
        proxy_cors_origins,
        data_dir: PathBuf::from(data_dir),
        storage: build_storage_config(args)?,
        config_poll_interval: Duration::from_secs(args.config_poll_interval),
        ..Default::default()
    };

    let (gateway, log_rx) = Gateway::new(config).await?;

    let gw_proxy = gateway.clone();
    let storage_for_logs = gateway.storage.clone();
    let (shutdown_tx, _) = broadcast::channel::<()>(1);
    let proxy_shutdown = shutdown_tx.subscribe();
    let admin_shutdown_tx = shutdown_tx.clone();

    let proxy_task = tokio::spawn(async move {
        if let Err(e) = gw_proxy
            .start_proxy_with_shutdown(wait_for_shutdown(proxy_shutdown))
            .await
        {
            tracing::error!("proxy server error: {e}");
        }
    });

    tokio::spawn(async move {
        logging::run_collector(log_rx, storage_for_logs).await;
    });

    let admin_router = admin_routes::create_router(gateway, admin_token.clone());

    let app = if let Some(ref webui_dir) = args.webui_dir {
        let index = webui_dir.join("index.html");
        tracing::info!("webui  serving from directory: {}", webui_dir.display());
        admin_router
            .fallback_service(ServeDir::new(webui_dir).not_found_service(ServeFile::new(index)))
            .layer(build_cors_layer(&admin_cors_origins))
    } else {
        admin_router
            .fallback(serve_webui)
            .layer(build_cors_layer(&admin_cors_origins))
    };

    let admin_addr = format!("{}:{}", args.admin_host, args.admin_port);
    let listener = tokio::net::TcpListener::bind(&admin_addr).await?;

    let proxy_bind_addr = format!("{}:{}", args.proxy_host, args.proxy_port);
    tracing::info!("proxy  → http://{proxy_bind_addr}");
    tracing::info!("webui  → http://{admin_addr}");

    if admin_token.is_none() {
        tracing::warn!("admin API auth disabled: set --admin-token for production");
    }
    let admin_result = axum::serve(listener, app)
        .with_graceful_shutdown(async move {
            shutdown_signal().await;
            let _ = admin_shutdown_tx.send(());
        })
        .await;

    let _ = shutdown_tx.send(());
    if let Err(error) = proxy_task.await {
        tracing::error!("proxy server task failed: {error}");
    }

    admin_result?;
    Ok(())
}

async fn shutdown_signal() {
    let ctrl_c = async {
        if let Err(error) = tokio::signal::ctrl_c().await {
            tracing::warn!("failed to listen for shutdown signal: {error}");
        }
    };

    #[cfg(unix)]
    let terminate = async {
        match tokio::signal::unix::signal(tokio::signal::unix::SignalKind::terminate()) {
            Ok(mut signal) => {
                signal.recv().await;
            }
            Err(error) => tracing::warn!("failed to listen for SIGTERM: {error}"),
        }
    };

    #[cfg(not(unix))]
    let terminate = std::future::pending::<()>();

    tokio::select! {
        _ = ctrl_c => {},
        _ = terminate => {},
    }

    tracing::info!("shutdown signal received");
}

async fn wait_for_shutdown(mut shutdown: broadcast::Receiver<()>) {
    let _ = shutdown.recv().await;
}

// ── WebUI (embedded) ──────────────────────────────────────────────────────────

async fn serve_webui(uri: axum::http::Uri) -> Response {
    let path = uri.path().trim_start_matches('/');
    let file_path = if path.is_empty() { "index.html" } else { path };

    match WebUiAssets::get(file_path) {
        Some(content) => {
            let mime = infer_mime(file_path);
            Response::builder()
                .header(header::CONTENT_TYPE, mime)
                .body(Body::from(content.data.into_owned()))
                .unwrap()
        }
        None => {
            // SPA fallback: serve index.html for any unmatched path
            match WebUiAssets::get("index.html") {
                Some(content) => Response::builder()
                    .header(header::CONTENT_TYPE, "text/html; charset=utf-8")
                    .body(Body::from(content.data.into_owned()))
                    .unwrap(),
                None => Response::builder()
                    .status(StatusCode::NOT_FOUND)
                    .body(Body::empty())
                    .unwrap(),
            }
        }
    }
}

fn infer_mime(path: &str) -> &'static str {
    if path.ends_with(".html") {
        "text/html; charset=utf-8"
    } else if path.ends_with(".js") || path.ends_with(".mjs") {
        "application/javascript"
    } else if path.ends_with(".css") {
        "text/css"
    } else if path.ends_with(".svg") {
        "image/svg+xml"
    } else if path.ends_with(".png") {
        "image/png"
    } else if path.ends_with(".ico") {
        "image/x-icon"
    } else if path.ends_with(".woff2") {
        "font/woff2"
    } else if path.ends_with(".woff") {
        "font/woff"
    } else if path.ends_with(".json") {
        "application/json"
    } else if path.ends_with(".map") {
        "application/json"
    } else {
        "application/octet-stream"
    }
}

// ── Storage ───────────────────────────────────────────────────────────────────

fn build_storage_config(args: &Args) -> anyhow::Result<GatewayStorageConfig> {
    let backend = parse_storage_backend(&args.storage_backend)?;

    let postgres_url = if matches!(backend, StorageBackendKind::Postgres) {
        let dsn = args
            .postgres_dsn
            .as_deref()
            .map(str::trim)
            .filter(|s| !s.is_empty())
            .ok_or_else(|| {
                anyhow::anyhow!(
                    "--postgres-dsn (or env NYRO_POSTGRES_DSN) is required \
                     when --storage-backend=postgres"
                )
            })?;
        Some(dsn.to_string())
    } else {
        None
    };

    let postgres = SqlStorageConfig {
        url: postgres_url,
        max_connections: args.postgres_max_connections,
        min_connections: args.postgres_min_connections,
        idle_timeout: args.postgres_idle_timeout.map(Duration::from_secs),
    };

    let mysql_url = if matches!(backend, StorageBackendKind::Mysql) {
        let dsn = args
            .mysql_dsn
            .as_deref()
            .map(str::trim)
            .filter(|s| !s.is_empty())
            .ok_or_else(|| {
                anyhow::anyhow!(
                    "--mysql-dsn (or env NYRO_MYSQL_DSN) is required \
                     when --storage-backend=mysql"
                )
            })?;
        Some(dsn.to_string())
    } else {
        None
    };

    let mysql = SqlStorageConfig {
        url: mysql_url,
        max_connections: args.mysql_max_connections,
        min_connections: args.mysql_min_connections,
        idle_timeout: args.mysql_idle_timeout.map(Duration::from_secs),
    };

    Ok(GatewayStorageConfig {
        backend,
        sqlite: SqliteStorageConfig {
            migrate_on_start: args.migrate_on_start,
        },
        postgres,
        mysql,
    })
}

fn parse_storage_backend(value: &str) -> anyhow::Result<StorageBackendKind> {
    match value.trim().to_ascii_lowercase().as_str() {
        "sqlite" => Ok(StorageBackendKind::Sqlite),
        "postgres" => Ok(StorageBackendKind::Postgres),
        "mysql" => Ok(StorageBackendKind::Mysql),
        other => anyhow::bail!("unsupported storage backend: {other}"),
    }
}

// ── Helpers ───────────────────────────────────────────────────────────────────

fn is_loopback_host(host: &str) -> bool {
    matches!(host, "127.0.0.1" | "localhost" | "::1")
}

fn default_local_origins(ports: &[u16]) -> Vec<String> {
    let mut origins = vec![
        "tauri://localhost".to_string(),
        "http://tauri.localhost".to_string(),
    ];
    for port in ports {
        origins.push(format!("http://127.0.0.1:{port}"));
        origins.push(format!("http://localhost:{port}"));
    }
    origins
}

fn parse_allow_origin(origins: &[String]) -> AllowOrigin {
    if origins.iter().any(|o| o.trim() == "*") {
        return AllowOrigin::any();
    }

    let values = origins
        .iter()
        .filter_map(|o| HeaderValue::from_str(o.trim()).ok())
        .collect::<Vec<_>>();

    if values.is_empty() {
        AllowOrigin::any()
    } else {
        AllowOrigin::list(values)
    }
}

fn build_cors_layer(origins: &[String]) -> CorsLayer {
    CorsLayer::new()
        .allow_origin(parse_allow_origin(origins))
        .allow_methods([
            Method::GET,
            Method::POST,
            Method::PUT,
            Method::DELETE,
            Method::OPTIONS,
        ])
        .allow_headers([
            header::AUTHORIZATION,
            header::CONTENT_TYPE,
            header::ACCEPT,
            header::HeaderName::from_static("x-api-key"),
            header::HeaderName::from_static("anthropic-version"),
        ])
}
