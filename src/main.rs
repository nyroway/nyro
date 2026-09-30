mod bootstrap;
mod control;
mod gateway;
mod http;
mod serve;

use clap::{Parser, Subcommand};

#[derive(Parser)]
#[command(version, about = "Nyro AI Gateway")]
struct Cli {
    #[command(subcommand)]
    command: Command,
}

#[derive(Subcommand)]
enum Command {
    /// Run the control plane; use --enable-proxy for an embedded gateway.
    Serve(serve::Options),
    /// Run the data plane from a startup file or a remote control plane.
    Proxy(serve::ProxyOptions),
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
        Command::Proxy(options) => serve::proxy(options).await,
    }
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
