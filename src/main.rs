mod bootstrap;
mod control;
mod gateway;
mod http;
mod reload;
mod serve;

use std::{path::PathBuf, time::Duration};

use anyhow::Context;
use clap::{Parser, Subcommand};
use nyro_config::Config;
use tokio_util::{sync::CancellationToken, task::TaskTracker};

#[derive(Parser)]
#[command(version, about = "Nyro AI Gateway")]
struct Cli {
    #[command(subcommand)]
    command: Command,
}

#[derive(Subcommand)]
enum Command {
    /// Run the SQLite or PostgreSQL control plane and gateway data plane.
    Serve(serve::Options),
    /// Run the standalone gateway data plane from a YAML configuration file.
    /// On Unix, send SIGHUP to reload the file while in-flight requests finish.
    Proxy {
        #[arg(short, long)]
        config: PathBuf,
    },
}

#[tokio::main]
async fn main() -> anyhow::Result<()> {
    let cli = Cli::parse();
    use tracing_subscriber::prelude::*;
    tracing_subscriber::registry()
        // The SDK emits wire payloads. An independent target filter cannot be
        // overridden by more specific RUST_LOG directives (e.g. rmcp::service).
        .with(tracing_subscriber::filter::filter_fn(|metadata| {
            metadata.target() != "rmcp" && !metadata.target().starts_with("rmcp::")
        }))
        .with(
            tracing_subscriber::EnvFilter::try_from_default_env()
                .unwrap_or_else(|_| "nyro=info".into()),
        )
        .with(tracing_subscriber::fmt::layer())
        .init();
    match cli.command {
        Command::Serve(options) => serve::run(options).await,
        Command::Proxy { config } => run(config).await,
    }
}

async fn run(path: PathBuf) -> anyhow::Result<()> {
    let reload_trigger = reload::Trigger::new().context("Could not register reload signal")?;
    let config = Config::load(&path)?;
    let resources = bootstrap::Resources::new(&config)?;
    let host = bootstrap::host(&config, &resources).await?;
    let listener = match tokio::net::TcpListener::bind(config.server.listen).await {
        Ok(listener) => listener,
        Err(error) => {
            host.shutdown().await?;
            return Err(error).context("Could not bind proxy listener");
        }
    };
    tracing::info!(listen = %config.server.listen, "Proxy ready");
    drop(config);
    let tracker = TaskTracker::new();
    let app = http::router(host.clone(), tracker.clone());
    let stop = CancellationToken::new();
    let shutdown = stop.clone();
    let mut server = tokio::spawn(async move {
        axum::serve(listener, app)
            .with_graceful_shutdown(shutdown.cancelled_owned())
            .await
    });
    let (exit, finished) = tokio::select! {
        biased;
        result = &mut server => (result.context("Proxy task failed").and_then(|result| result.context("Proxy server failed")), true),
        result = shutdown_signal() => (result.context("Could not receive shutdown signal"), false),
        result = reload::run(reload_trigger, &path, &resources, &host, stop.clone()) =>
            (result.context("Configuration reload listener failed"), false),
    };
    stop.cancel();
    let cleanup = host.shutdown().await;
    let drained = if finished {
        Ok(())
    } else {
        match tokio::time::timeout(Duration::from_secs(5), &mut server).await {
            Ok(result) => result
                .context("Proxy task failed")
                .and_then(|result| result.context("Proxy server failed")),
            Err(_) => {
                server.abort();
                let _ = server.await;
                Err(anyhow::anyhow!(
                    "HTTP connections did not drain before shutdown deadline"
                ))
            }
        }
    };
    tracker.close();
    let tracked = tokio::time::timeout(Duration::from_secs(5), tracker.wait()).await;
    exit?;
    cleanup?;
    drained?;
    tracked.context("Response cleanup tasks did not stop")?;
    Ok(())
}

async fn shutdown_signal() -> std::io::Result<()> {
    #[cfg(unix)]
    {
        let mut terminate =
            tokio::signal::unix::signal(tokio::signal::unix::SignalKind::terminate())?;
        tokio::select! {
            result = tokio::signal::ctrl_c() => result,
            _ = terminate.recv() => Ok(()),
        }
    }
    #[cfg(not(unix))]
    tokio::signal::ctrl_c().await
}
