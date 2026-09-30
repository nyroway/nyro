//! Process composition. Only the control plane opens databases.
use crate::{
    bootstrap::Application,
    control::{self, Control},
    gateway::GatewayRuntime,
    http, shutdown_signal,
};
use anyhow::Context;
use clap::Args;
use nyro_config::compile::Snapshot;
use nyro_kernel::Host;
use std::{
    net::SocketAddr,
    path::{Path, PathBuf},
    sync::Arc,
    time::Duration,
};
use tokio_util::{sync::CancellationToken, task::TaskTracker};

#[derive(Args, Clone)]
pub(crate) struct DataOptions {
    #[arg(long, env = "NYRO_LISTEN", default_value = "127.0.0.1:19530")]
    pub(crate) listen: SocketAddr,
    #[arg(long, env = "NYRO_CONCURRENCY", default_value_t = 64)]
    pub(crate) concurrency: usize,
}
#[derive(Args)]
pub(crate) struct ProxyOptions {
    /// Read runtime resources once at startup.
    #[arg(
        short,
        long,
        env = "NYRO_CONFIG",
        required_unless_present = "server",
        conflicts_with = "server"
    )]
    config: Option<PathBuf>,
    /// HTTPS control-plane origin (HTTP is permitted only for loopback testing).
    #[arg(long, env = "NYRO_SERVER", requires_all = ["sync_token_file", "node_id"])]
    server: Option<String>,
    #[arg(long, env = "NYRO_SYNC_TOKEN_FILE", requires = "server")]
    sync_token_file: Option<PathBuf>,
    #[arg(long, env = "NYRO_NODE_ID", requires = "server")]
    node_id: Option<String>,
    #[command(flatten)]
    data: DataOptions,
}
#[derive(Args)]
pub(crate) struct Options {
    #[arg(
        long,
        env = "NYRO_DATABASE",
        required_unless_present = "postgres_url_file",
        conflicts_with = "postgres_url_file"
    )]
    database: Option<PathBuf>,
    #[arg(long, env = "NYRO_POSTGRES_URL_FILE")]
    postgres_url_file: Option<PathBuf>,
    #[arg(long, env = "NYRO_ADMIN_LISTEN", default_value = "127.0.0.1:19531")]
    admin_listen: SocketAddr,
    #[arg(long, env = "NYRO_ADMIN_TOKEN_FILE")]
    admin_token_file: PathBuf,
    /// Run an embedded gateway using the same snapshot/application contract as remote nodes.
    #[arg(long, env = "NYRO_ENABLE_PROXY", default_value_t = false)]
    enable_proxy: bool,
    /// Dedicated loopback listener. Expose remotely through an HTTPS reverse proxy.
    #[arg(long, env = "NYRO_SYNC_LISTEN", requires = "sync_token_file")]
    sync_listen: Option<SocketAddr>,
    #[arg(long, env = "NYRO_SYNC_TOKEN_FILE", requires = "sync_listen")]
    sync_token_file: Option<PathBuf>,
    #[command(flatten)]
    data: DataOptions,
}
pub(crate) fn read_file(path: &Path, max: usize) -> anyhow::Result<String> {
    use std::io::Read;
    anyhow::ensure!(
        std::fs::metadata(path)
            .map_err(|_| anyhow::anyhow!("Could not read startup file"))?
            .is_file(),
        "Startup input must be a regular file"
    );
    let file =
        std::fs::File::open(path).map_err(|_| anyhow::anyhow!("Could not read startup file"))?;
    anyhow::ensure!(
        file.metadata()?.is_file(),
        "Startup input must be a regular file"
    );
    let mut value = String::new();
    file.take(max as u64 + 1)
        .read_to_string(&mut value)
        .map_err(|_| anyhow::anyhow!("Could not read startup file"))?;
    anyhow::ensure!(value.len() <= max, "Startup file exceeds size limit");
    Ok(value)
}
fn token(path: &Path) -> anyhow::Result<String> {
    let token = read_file(path, 1024)?
        .trim_end_matches(['\r', '\n'])
        .to_owned();
    anyhow::ensure!(
        token.len() >= 16 && token.bytes().all(|b| b.is_ascii_graphic()),
        "Token must contain 16 to 1024 visible ASCII characters"
    );
    Ok(token)
}
fn keys(token: String) -> anyhow::Result<nyro_authn::KeyAuth> {
    Ok(nyro_authn::KeyAuth::new(vec![nyro_authn::KeyCredential {
        id: "admin".into(),
        secret: token,
        enabled: true,
        expires_at: None,
    }])?)
}
fn follow<S: nyro_sync::Source<Snapshot> + 'static>(
    source: S,
    node: String,
    application: Arc<Application>,
    host: Arc<Host<GatewayRuntime>>,
    stop: CancellationToken,
) -> tokio::task::JoinHandle<Result<(), nyro_sync::Error>> {
    tokio::spawn(async move {
        nyro_sync::run(&source, &node, stop, |snapshot| {
            let application = application.clone();
            let host = host.clone();
            async move { application.apply(&host, &snapshot.config).await }
        })
        .await
    })
}
fn listener(
    tasks: &mut tokio::task::JoinSet<anyhow::Result<()>>,
    tcp: tokio::net::TcpListener,
    app: axum::Router,
    stop: CancellationToken,
) {
    tasks.spawn(async move {
        axum::serve(tcp, app)
            .with_graceful_shutdown(stop.cancelled_owned())
            .await
            .context("Listener failed")
    });
}
async fn finish(
    mut tasks: tokio::task::JoinSet<anyhow::Result<()>>,
    mut sync: Option<tokio::task::JoinHandle<Result<(), nyro_sync::Error>>>,
    stop: CancellationToken,
    host: Option<Arc<Host<GatewayRuntime>>>,
    control: Option<Arc<Control>>,
    tracker: TaskTracker,
) -> anyhow::Result<()> {
    let outcome = tokio::select! {
        result = shutdown_signal() => result.context("Shutdown signal failed"),
        result = tasks.join_next() => match result { Some(Ok(result)) => result, _ => Err(anyhow::anyhow!("Listener task failed")) },
        result = async { match sync.as_mut() { Some(task) => task.await.map_err(anyhow::Error::from)?.map_err(anyhow::Error::from), None => std::future::pending().await } } => result,
    };
    stop.cancel();
    if let Some(control) = &control {
        control.shutdown().await;
    }
    // Cancellation wakes long polls; an application transition is owned by the kernel.
    if let Some(task) = sync.as_mut()
        && !task.is_finished()
    {
        let _ = task.await;
    }
    let cleanup = if let Some(host) = &host {
        host.shutdown().await.map_err(anyhow::Error::from)
    } else {
        Ok(())
    };
    let drained = tokio::time::timeout(Duration::from_secs(5), async {
        while let Some(task) = tasks.join_next().await {
            task??;
        }
        Ok::<_, anyhow::Error>(())
    })
    .await;
    tasks.abort_all();
    tracker.close();
    let tracked = tokio::time::timeout(Duration::from_secs(5), tracker.wait()).await;
    outcome?;
    cleanup?;
    drained.context("HTTP connections did not drain")??;
    tracked.context("Response cleanup did not finish")?;
    Ok(())
}
pub(crate) async fn proxy(options: ProxyOptions) -> anyhow::Result<()> {
    let application = Arc::new(Application::new(options.data.concurrency)?);
    let host = Arc::new(Host::new(Default::default()));
    let stop = CancellationToken::new();
    let tracker = TaskTracker::new();
    let tcp = tokio::net::TcpListener::bind(options.data.listen)
        .await
        .context("Could not bind proxy listener")?;
    let sync = match (&options.config, &options.server) {
        (Some(path), None) => {
            let resources = nyro_config::resources::Resources::from_yaml(&read_file(
                path,
                nyro_control::MAX_CONFIG_BYTES,
            )?)?;
            application
                .apply(&host, &Snapshot::file(resources))
                .await
                .map_err(|_| anyhow::anyhow!("Could not activate resource configuration"))?;
            None
        }
        (None, Some(server)) => {
            let node = options.node_id.as_deref().unwrap_or("");
            anyhow::ensure!(
                (1..=128).contains(&node.len())
                    && node
                        .bytes()
                        .all(|b| b.is_ascii_alphanumeric() || b"-_.".contains(&b)),
                "Invalid node ID"
            );
            let client = nyro_sync::HttpClient::<Snapshot>::new(
                server,
                &token(
                    options
                        .sync_token_file
                        .as_deref()
                        .context("Sync token required")?,
                )?,
            )?;
            Some(follow(
                client,
                node.into(),
                application,
                host.clone(),
                stop.clone(),
            ))
        }
        _ => anyhow::bail!("Exactly one configuration source is required"),
    };
    let mut tasks = tokio::task::JoinSet::new();
    listener(
        &mut tasks,
        tcp,
        http::router(host.clone(), tracker.clone()),
        stop.clone(),
    );
    tracing::info!(listen = %options.data.listen, "Proxy listener started; /readyz reports active configuration");
    finish(tasks, sync, stop, Some(host), None, tracker).await
}
pub(crate) async fn run(options: Options) -> anyhow::Result<()> {
    anyhow::ensure!(
        options.admin_listen.ip().is_loopback(),
        "Admin listener must use loopback"
    );
    if let Some(listen) = options.sync_listen {
        anyhow::ensure!(
            listen.ip().is_loopback(),
            "Sync listener must use loopback behind an HTTPS reverse proxy"
        );
    }
    let admin_token = token(&options.admin_token_file)?;
    let sync_token = options.sync_token_file.as_deref().map(token).transpose()?;
    anyhow::ensure!(
        sync_token.as_ref() != Some(&admin_token),
        "Admin and sync tokens must be different"
    );
    let mut store = match (&options.database, &options.postgres_url_file) {
        (Some(path), None) => nyro_control::resource::Store::open(path).await?,
        (None, Some(path)) => {
            nyro_control::resource::Store::open_postgres(
                read_file(path, 16384)?.trim_end_matches(['\r', '\n']),
            )
            .await?
        }
        _ => anyhow::bail!("Exactly one database source is required"),
    };
    let snapshot = store.snapshot().await?;
    let control = Control::new(store, snapshot, keys(admin_token)?)?;
    let admin = tokio::net::TcpListener::bind(options.admin_listen)
        .await
        .context("Could not bind admin listener")?;
    let sync_tcp = match options.sync_listen {
        Some(listen) => Some(
            tokio::net::TcpListener::bind(listen)
                .await
                .context("Could not bind sync listener")?,
        ),
        None => None,
    };
    let data_tcp = if options.enable_proxy {
        Some(
            tokio::net::TcpListener::bind(options.data.listen)
                .await
                .context("Could not bind data listener")?,
        )
    } else {
        None
    };
    let stop = CancellationToken::new();
    let tracker = TaskTracker::new();
    let mut tasks = tokio::task::JoinSet::new();
    listener(
        &mut tasks,
        admin,
        control::router(control.clone()),
        stop.clone(),
    );
    if let Some(tcp) = sync_tcp {
        listener(
            &mut tasks,
            tcp,
            nyro_sync::http_router(control.hub.clone(), sync_token.as_deref().unwrap())?,
            stop.clone(),
        );
    }
    let (host, sync) = if let Some(tcp) = data_tcp {
        let application = Arc::new(Application::new(options.data.concurrency)?);
        let host = Arc::new(Host::new(Default::default()));
        let sync = follow(
            control.hub.clone(),
            "embedded".into(),
            application,
            host.clone(),
            stop.clone(),
        );
        listener(
            &mut tasks,
            tcp,
            http::router(host.clone(), tracker.clone()),
            stop.clone(),
        );
        (Some(host), Some(sync))
    } else {
        (None, None)
    };
    tracing::info!(admin_listen = %options.admin_listen, proxy = options.enable_proxy, "Control plane ready");
    finish(tasks, sync, stop, host, Some(control), tracker).await
}
#[cfg(test)]
mod tests {
    use clap::Parser;
    #[test]
    fn serve_is_control_only_by_default_and_proxy_requires_one_source() {
        let cli = crate::Cli::try_parse_from([
            "nyro",
            "serve",
            "--database",
            "control.db",
            "--admin-token-file",
            "token",
        ])
        .unwrap();
        let crate::Command::Serve(options) = cli.command else {
            panic!()
        };
        assert!(!options.enable_proxy);
        assert!(crate::Cli::try_parse_from(["nyro", "proxy"]).is_err());
        assert!(
            crate::Cli::try_parse_from(["nyro", "proxy", "--config", "resources.yaml"]).is_ok()
        );
        assert!(
            crate::Cli::try_parse_from(["nyro", "proxy", "--server", "https://example.test"])
                .is_err()
        );
        assert!(
            crate::Cli::try_parse_from([
                "nyro",
                "proxy",
                "--config",
                "resources.yaml",
                "--server",
                "https://example.test"
            ])
            .is_err()
        );
    }
}
