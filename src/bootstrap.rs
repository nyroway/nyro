use std::{net::SocketAddr, sync::Arc, time::Duration};

use nyro_config::Config;
use nyro_kernel::{Candidate, Context, Host};
use nyro_limit::ConcurrencyLimit;
use nyro_llm::runtime::{Options, Runtime, SharedResources};
use nyro_security::ApiKeys;
use tokio::time::Instant;
use tokio_util::sync::CancellationToken;

pub(crate) struct Resources {
    limit: ConcurrencyLimit,
    capacity: usize,
    listen: SocketAddr,
    shared: SharedResources,
}

impl Resources {
    pub(crate) fn new(config: &Config) -> anyhow::Result<Self> {
        config.validate()?;
        Ok(Self {
            limit: ConcurrencyLimit::new(config.limit.concurrency)?,
            capacity: config.limit.concurrency,
            listen: config.server.listen,
            shared: SharedResources::default(),
        })
    }

    pub(crate) fn check_settings(&self, config: &Config) -> anyhow::Result<()> {
        anyhow::ensure!(
            config.server.listen == self.listen,
            "Changing the listen address requires a process restart"
        );
        anyhow::ensure!(
            config.limit.concurrency == self.capacity,
            "Changing concurrency capacity requires a process restart"
        );
        Ok(())
    }

    pub(crate) fn candidate(&self, config: &Config) -> anyhow::Result<Candidate<Runtime>> {
        config.validate()?;
        self.check_settings(config)?;
        let options = Options {
            request_timeout: Duration::from_millis(config.server.request_timeout_ms),
            max_body_bytes: config.server.max_body_bytes,
            max_response_bytes: config.server.max_response_bytes,
            max_frame_bytes: config.server.max_frame_bytes,
        };
        let keys = Arc::new(ApiKeys::new(config.security.api_keys.clone())?);
        Ok(Candidate {
            version: "standalone".into(),
            fingerprint: Some(config.fingerprint()?),
            value: Runtime::with_resources(
                config.llm.clone(),
                keys,
                self.limit.clone(),
                options,
                self.shared.clone(),
            )?,
            // Reqwest clients have synchronous RAII cleanup; no background component is needed.
            components: vec![],
        })
    }
}

pub(crate) async fn host(
    config: &Config,
    resources: &Resources,
) -> anyhow::Result<Arc<Host<Runtime>>> {
    let host = Arc::new(Host::new(Default::default()));
    host.activate(
        resources.candidate(config)?,
        Context {
            deadline: Instant::now() + Duration::from_secs(10),
            cancellation: CancellationToken::new(),
        },
    )
    .await?;
    Ok(host)
}
