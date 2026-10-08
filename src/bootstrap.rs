use crate::gateway::GatewayRuntime;
use nyro_kernel::{Candidate, Context, Host};
use nyro_limit::ConcurrencyLimit;
use nyro_llm::{Runtime as LlmRuntime, runtime::SharedResources};
use std::{sync::Arc, time::Duration};
use tokio::time::Instant;
use tokio_util::sync::CancellationToken;

/// Process-owned state shared across every successfully applied resource generation.
pub(crate) struct Application {
    concurrency: ConcurrencyLimit,
    llm: SharedResources,
    limits: nyro_limit::token::Registry,
    pools: nyro_mcp::runtime::Pools,
}
impl Application {
    pub(crate) fn new(concurrency: usize) -> anyhow::Result<Self> {
        Ok(Self {
            concurrency: ConcurrencyLimit::new(concurrency)?,
            llm: Default::default(),
            limits: Default::default(),
            pools: Default::default(),
        })
    }
    pub(crate) fn candidate(
        &self,
        snapshot: &nyro_config::compile::Snapshot,
    ) -> anyhow::Result<Candidate<GatewayRuntime>> {
        let compiled = snapshot.compile(&self.limits, self.pools.clone())?;
        let keys = Arc::new(compiled.keys);
        Ok(Candidate {
            version: "resources-v1".into(),
            fingerprint: Some(nyro_sync::fingerprint(snapshot)?),
            value: GatewayRuntime {
                llm: LlmRuntime::with_policies(
                    compiled.llm,
                    keys.clone(),
                    self.concurrency.clone(),
                    compiled.options,
                    self.llm.clone(),
                    compiled.llm_policies,
                )?,
                mcp: nyro_mcp::Runtime::with_policies(
                    compiled.mcp,
                    keys,
                    self.concurrency.clone(),
                    compiled.mcp_policies,
                )?,
            },
            components: vec![],
        })
    }
    pub(crate) async fn apply(
        &self,
        host: &Host<GatewayRuntime>,
        snapshot: &nyro_config::compile::Snapshot,
    ) -> Result<(), nyro_sync::ApplyError> {
        let candidate = self
            .candidate(snapshot)
            .map_err(|_| nyro_sync::ApplyError::Rejected("invalid_config"))?;
        host.activate(
            candidate,
            Context {
                deadline: Instant::now() + Duration::from_secs(10),
                cancellation: CancellationToken::new(),
            },
        )
        .await
        .map_err(|_| nyro_sync::ApplyError::Retry("activation_failed"))?;
        Ok(())
    }
}
