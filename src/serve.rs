//! Server composition; database entities never enter request execution or the kernel.
use crate::{
    bootstrap,
    control::{self, Control},
    http, shutdown_signal,
};
use anyhow::Context;
use clap::Args;
use nyro_config::Config;
use nyro_control::{MAX_CONFIG_BYTES, Store};
use nyro_security::{ApiKey, ApiKeys};
use std::{
    net::SocketAddr,
    path::{Path, PathBuf},
    time::Duration,
};
use tokio_util::{sync::CancellationToken, task::TaskTracker};

#[derive(Args)]
pub(crate) struct Options {
    /// Dedicated control-plane SQLite file; legacy databases are not imported.
    #[arg(long)]
    database: PathBuf,
    /// YAML seed for a new database only; omit when restarting an initialized database.
    #[arg(short, long)]
    config: Option<PathBuf>,
    /// Local admin API listener, separate from the data-plane listener in the snapshot.
    #[arg(long, default_value = "127.0.0.1:19531")]
    admin_listen: SocketAddr,
    /// File containing the admin Bearer token, separate from data-plane API keys.
    #[arg(long)]
    admin_token_file: PathBuf,
}

fn read_file(path: &Path, max: usize) -> anyhow::Result<String> {
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

pub(crate) async fn run(options: Options) -> anyhow::Result<()> {
    anyhow::ensure!(
        options.admin_listen.ip().is_loopback(),
        "The experimental admin listener must use a loopback address"
    );
    let token = read_file(&options.admin_token_file, 1024)?;
    let token = token.trim_end_matches(['\r', '\n']);
    anyhow::ensure!(
        token.len() >= 16 && token.bytes().all(|b| b.is_ascii_graphic()),
        "Admin token must contain 16 to 1024 visible ASCII characters"
    );
    let keys = ApiKeys::new(vec![ApiKey {
        id: "admin".into(),
        secret: token.into(),
        enabled: true,
        expires_at: None,
    }])?;
    let seed = options
        .config
        .as_ref()
        .map(|path| {
            Config::from_yaml(&read_file(path, MAX_CONFIG_BYTES)?)
                .map_err(|_| anyhow::anyhow!("Invalid seed configuration"))
        })
        .transpose()?;
    let mut store = Store::open(&options.database, seed.as_ref()).await?;
    let published = store.state().await?.published;
    let resources = bootstrap::Resources::new(&published.config)?;
    let host = bootstrap::host(&published.config, &resources).await?;
    let listeners = async {
        let data = tokio::net::TcpListener::bind(published.config.server.listen).await?;
        let admin = tokio::net::TcpListener::bind(options.admin_listen).await?;
        Ok::<_, std::io::Error>((data, admin))
    }
    .await;
    let (data, admin) = match listeners {
        Ok(listeners) => listeners,
        Err(_) => {
            host.shutdown().await?;
            return Err(anyhow::anyhow!("Could not bind serve listeners"));
        }
    };
    let control = Control::new(store, resources, host.clone(), published.revision, keys);
    let tracker = TaskTracker::new();
    let stop = CancellationToken::new();
    let data_stop = stop.clone();
    let admin_stop = stop.clone();
    let data_app = http::router(host.clone(), tracker.clone());
    let admin_app = control::router(control.clone());
    let mut data_server = tokio::spawn(async move {
        axum::serve(data, data_app)
            .with_graceful_shutdown(data_stop.cancelled_owned())
            .await
    });
    let mut admin_server = tokio::spawn(async move {
        axum::serve(admin, admin_app)
            .with_graceful_shutdown(admin_stop.cancelled_owned())
            .await
    });
    tracing::info!(data_listen = %published.config.server.listen, admin_listen = %options.admin_listen, revision = published.revision, "Server ready");
    drop(published);
    drop(seed);
    let (exit, data_done, admin_done) = tokio::select! {
        result = &mut data_server => (result.context("Data server task failed").and_then(|r| r.context("Data server failed")), true, false),
        result = &mut admin_server => (result.context("Admin server task failed").and_then(|r| r.context("Admin server failed")), false, true),
        result = shutdown_signal() => (result.context("Could not receive shutdown signal"), false, false),
    };
    stop.cancel();
    // Finish accepted durable writes before closing the Host, even if their callers left.
    control.shutdown().await;
    let cleanup = host.shutdown().await;
    let mut drained = Ok(());
    for (mut server, done) in [(data_server, data_done), (admin_server, admin_done)] {
        if !done {
            match tokio::time::timeout(Duration::from_secs(5), &mut server).await {
                Ok(result) => {
                    if !matches!(result, Ok(Ok(()))) {
                        drained = Err(anyhow::anyhow!("Serve listener failed"));
                    }
                }
                Err(_) => {
                    server.abort();
                    let _ = server.await;
                    drained = Err(anyhow::anyhow!("Serve connections did not drain"));
                }
            }
        }
    }
    tracker.close();
    let tracked = tokio::time::timeout(Duration::from_secs(5), tracker.wait()).await;
    drop(control);
    exit?;
    cleanup?;
    drained?;
    tracked.context("Response cleanup tasks did not stop")?;
    Ok(())
}
