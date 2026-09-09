//! Serial file reloads; the kernel owns publication and retirement.
use crate::bootstrap::Resources;
use nyro_config::Config;
use nyro_kernel::{Context, Host};
use nyro_llm::runtime::Runtime;
use std::{io, path::Path, time::Duration};
use tokio::time::Instant;
use tokio_util::sync::CancellationToken;

/// Register before readiness so an immediate SIGHUP cannot take its default action.
pub(crate) struct Trigger {
    #[cfg(unix)]
    signal: tokio::signal::unix::Signal,
}
impl Trigger {
    pub(crate) fn new() -> io::Result<Self> {
        Ok(Self {
            #[cfg(unix)]
            signal: tokio::signal::unix::signal(tokio::signal::unix::SignalKind::hangup())?,
        })
    }

    async fn recv(&mut self) -> io::Result<()> {
        #[cfg(unix)]
        {
            self.signal
                .recv()
                .await
                .ok_or_else(|| io::Error::other("Reload signal stream closed"))
        }
        #[cfg(not(unix))]
        std::future::pending().await
    }
}

#[derive(Debug, Eq, PartialEq)]
enum Outcome {
    Unchanged(u64),
    Applied(u64),
}

// Do not retain parser, file-path, credential, or candidate error details in reload logs.
#[derive(Debug, Eq, PartialEq)]
enum ReloadError {
    Read,
    Invalid,
    RestartRequired,
    Candidate,
    Activation,
    Interrupted,
}
impl std::fmt::Display for ReloadError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(match self {
            Self::Read => "read_failed",
            Self::Invalid => "invalid_config",
            Self::RestartRequired => "restart_required",
            Self::Candidate => "candidate_rejected",
            Self::Activation => "activation_failed",
            Self::Interrupted => "interrupted",
        })
    }
}

pub(crate) async fn run(
    mut trigger: Trigger,
    path: &Path,
    resources: &Resources,
    host: &Host<Runtime>,
    shutdown: CancellationToken,
) -> io::Result<()> {
    loop {
        trigger.recv().await?;
        match from_file(path, resources, host, shutdown.clone()).await {
            Ok(outcome) => {
                let (outcome, generation) = match outcome {
                    Outcome::Unchanged(id) => ("unchanged", id),
                    Outcome::Applied(id) => ("applied", id),
                };
                tracing::info!(target: "nyro::reload", outcome, generation, "Configuration reload finished");
            }
            Err(reason) => {
                tracing::warn!(target: "nyro::reload", outcome = "rejected", reason = %reason,
                    "Configuration reload rejected");
            }
        }
    }
}

async fn from_file(
    path: &Path,
    resources: &Resources,
    host: &Host<Runtime>,
    shutdown: CancellationToken,
) -> Result<Outcome, ReloadError> {
    let cancellation = shutdown.child_token();
    // Dropping a kernel activation waiter alone does not cancel its owned work.
    let _cancel_on_drop = cancellation.clone().drop_guard();
    let deadline = Instant::now() + Duration::from_secs(10);
    let work = async {
        // Tokio file reads use blocking workers; named pipes can prevent runtime shutdown.
        let metadata = tokio::fs::metadata(path)
            .await
            .map_err(|_| ReloadError::Read)?;
        if !metadata.is_file() {
            return Err(ReloadError::Read);
        }
        let yaml = tokio::fs::read_to_string(path)
            .await
            .map_err(|_| ReloadError::Read)?;
        let config = Config::from_yaml(&yaml).map_err(|_| ReloadError::Invalid)?;
        apply(
            &config,
            resources,
            host,
            Context {
                deadline,
                cancellation: cancellation.clone(),
            },
        )
        .await
    };
    tokio::select! {
        biased;
        _ = cancellation.cancelled() => Err(ReloadError::Interrupted),
        result = tokio::time::timeout_at(deadline, work) => result.unwrap_or(Err(ReloadError::Interrupted)),
    }
}

async fn apply(
    config: &Config,
    resources: &Resources,
    host: &Host<Runtime>,
    context: Context,
) -> Result<Outcome, ReloadError> {
    if context.cancellation.is_cancelled()
        || Instant::now() >= context.deadline
        || host.status().closing
    {
        return Err(ReloadError::Interrupted);
    }
    config.validate().map_err(|_| ReloadError::Invalid)?;
    // listen is intentionally excluded from the data-plane fingerprint.
    resources
        .check_settings(config)
        .map_err(|_| ReloadError::RestartRequired)?;
    let fingerprint = config.fingerprint().map_err(|_| ReloadError::Invalid)?;
    if context.cancellation.is_cancelled() || Instant::now() >= context.deadline {
        return Err(ReloadError::Interrupted);
    }
    if let Some(active) = host.status().active
        && active.generation.fingerprint.as_deref() == Some(&fingerprint)
    {
        return Ok(Outcome::Unchanged(active.generation.id));
    }
    let candidate = resources
        .candidate(config)
        .map_err(|_| ReloadError::Candidate)?;
    let generation = host
        .activate(candidate, context)
        .await
        .map_err(|_| ReloadError::Activation)?;
    Ok(Outcome::Applied(generation.id))
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::bootstrap;
    use std::time::Duration;
    use tokio::time::Instant;

    fn config() -> Config {
        Config::from_yaml(
            r#"
llm:
  providers:
    p: {kind: openai, base_url: 'http://127.0.0.1:1/v1'}
  models:
    public:
      provider: p
      upstream_model: original
      workloads: [chat, embedding]
      allow_anonymous: true
      rate: {requests: 1, period_ms: 600000}
      quota: {total_tokens: 100, reserve_tokens: 10}
"#,
        )
        .unwrap()
    }

    fn context() -> Context {
        Context {
            deadline: Instant::now() + Duration::from_secs(2),
            cancellation: CancellationToken::new(),
        }
    }

    #[tokio::test]
    async fn effective_duplicate_keeps_generation_while_change_retires_old_lease() {
        let mut config = config();
        let resources = Resources::new(&config).unwrap();
        let host = bootstrap::host(&config, &resources).await.unwrap();
        let lease = host.acquire().unwrap();
        let original = lease.generation().id;
        config
            .llm
            .models
            .get_mut("public")
            .unwrap()
            .workloads
            .reverse();
        assert_eq!(
            apply(&config, &resources, &host, context()).await.unwrap(),
            Outcome::Unchanged(original)
        );
        assert!(host.status().retiring.is_empty());
        config.llm.models.get_mut("public").unwrap().backends[0].upstream_model =
            "replacement".into();
        let Outcome::Applied(updated) = apply(&config, &resources, &host, context()).await.unwrap()
        else {
            panic!("changed route must activate")
        };
        assert!(updated > original);
        assert_eq!(host.acquire().unwrap().generation().id, updated);
        assert_eq!(lease.generation().id, original);
        assert!(!lease.cancellation().is_cancelled());
        assert_eq!(host.status().retiring[0].generation.id, original);
        drop(lease);
        tokio::time::timeout(Duration::from_secs(2), async {
            while !host.status().retiring.is_empty() {
                tokio::task::yield_now().await;
            }
        })
        .await
        .unwrap();
        host.shutdown().await.unwrap();
    }

    #[tokio::test]
    async fn immutable_settings_are_checked_before_fingerprint_deduplication() {
        let original = config();
        let resources = Resources::new(&original).unwrap();
        let host = bootstrap::host(&original, &resources).await.unwrap();
        let generation = host.acquire().unwrap().generation().id;
        let mut changed = original.clone();
        changed
            .server
            .listen
            .set_port(original.server.listen.port() + 1);
        assert_eq!(
            original.fingerprint().unwrap(),
            changed.fingerprint().unwrap()
        );
        assert_eq!(
            apply(&changed, &resources, &host, context())
                .await
                .unwrap_err(),
            ReloadError::RestartRequired
        );
        changed = original.clone();
        changed.limit.concurrency += 1;
        assert_eq!(
            apply(&changed, &resources, &host, context())
                .await
                .unwrap_err(),
            ReloadError::RestartRequired
        );
        assert_eq!(host.acquire().unwrap().generation().id, generation);
        host.shutdown().await.unwrap();
    }

    #[tokio::test]
    async fn invalid_or_conflicting_candidates_preserve_active_generation() {
        let original = config();
        let resources = Resources::new(&original).unwrap();
        let host = bootstrap::host(&original, &resources).await.unwrap();
        let generation = host.acquire().unwrap().generation().id;
        for kind in ["invalid", "rate", "quota"] {
            let mut changed = original.clone();
            let model = changed.llm.models.get_mut("public").unwrap();
            let expected = match kind {
                "invalid" => {
                    model.backends[0].provider = "unknown-secret-marker".into();
                    ReloadError::Invalid
                }
                "rate" => {
                    model.rate.as_mut().unwrap().burst += 1;
                    ReloadError::Candidate
                }
                _ => {
                    model.quota.as_mut().unwrap().total_tokens += 1;
                    ReloadError::Candidate
                }
            };
            let error = apply(&changed, &resources, &host, context())
                .await
                .unwrap_err();
            assert_eq!(error, expected);
            assert!(!error.to_string().contains("secret-marker"));
            assert_eq!(host.acquire().unwrap().generation().id, generation);
            assert!(host.status().retiring.is_empty());
        }
        assert_eq!(
            apply(&original, &resources, &host, context())
                .await
                .unwrap(),
            Outcome::Unchanged(generation)
        );
        host.shutdown().await.unwrap();
    }

    #[tokio::test]
    async fn interrupted_reload_never_publishes_even_an_unchanged_candidate() {
        let config = config();
        let resources = Resources::new(&config).unwrap();
        let host = bootstrap::host(&config, &resources).await.unwrap();
        let generation = host.acquire().unwrap().generation().id;
        let cancelled = context();
        cancelled.cancellation.cancel();
        let mut expired = context();
        expired.deadline = Instant::now();
        for context in [cancelled, expired] {
            assert_eq!(
                apply(&config, &resources, &host, context)
                    .await
                    .unwrap_err(),
                ReloadError::Interrupted
            );
        }
        assert_eq!(host.acquire().unwrap().generation().id, generation);
        host.shutdown().await.unwrap();
        assert_eq!(
            apply(&config, &resources, &host, context())
                .await
                .unwrap_err(),
            ReloadError::Interrupted
        );
    }
}
