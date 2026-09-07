use std::{
    collections::BTreeMap,
    future::Future,
    sync::{
        Arc, Mutex,
        atomic::{AtomicUsize, Ordering},
    },
    time::Duration,
};

use tokio::{
    runtime::Handle,
    sync::{Notify, oneshot},
    time::Instant,
};

use crate::{
    CancellationToken, Component, Context, Issue, KernelError, Phase, lifecycle::Resources,
};

/// A typed, unpublished runtime. Construction must not start background work.
pub struct Candidate<T> {
    pub version: String,
    /// Opaque metadata. Comparison and no-op reload detection belong to bootstrap.
    pub fingerprint: Option<String>,
    pub value: T,
    pub components: Vec<Component>,
}

#[derive(Clone, Copy, Debug)]
pub struct HostOptions {
    pub shutdown_grace: Duration,
    pub cleanup_timeout: Duration,
}

impl Default for HostOptions {
    fn default() -> Self {
        Self {
            shutdown_grace: Duration::from_secs(30),
            cleanup_timeout: Duration::from_secs(5),
        }
    }
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct GenerationInfo {
    /// Monotonically increasing within this Host, not a cluster-wide identifier.
    pub id: u64,
    pub version: String,
    pub fingerprint: Option<String>,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct GenerationStatus {
    pub generation: GenerationInfo,
    pub leases: usize,
}

#[derive(Clone, Debug)]
pub struct Status {
    pub accepting: bool,
    pub closing: bool,
    pub active: Option<GenerationStatus>,
    pub retiring: Vec<GenerationStatus>,
    pub pending_activations: usize,
    pub last_activation_error: Option<KernelError>,
    pub cleanup_errors: Vec<Issue>,
}

struct Generation<T> {
    info: GenerationInfo,
    value: T,
    leases: AtomicUsize,
    cancellation: CancellationToken,
    resources: Mutex<Option<Resources>>,
}

impl<T> Generation<T> {
    fn status(&self) -> GenerationStatus {
        GenerationStatus {
            generation: self.info.clone(),
            leases: self.leases.load(Ordering::Acquire),
        }
    }
}

/// Keep this through response streaming AND required finalizers, not just the handler.
pub struct Lease<T: Send + Sync + 'static> {
    generation: Arc<Generation<T>>,
    host: Arc<Inner<T>>,
}

impl<T: Send + Sync + 'static> Lease<T> {
    pub fn value(&self) -> &T {
        &self.generation.value
    }
    pub fn generation(&self) -> &GenerationInfo {
        &self.generation.info
    }
    /// A child signal: cancelling it cannot cancel other leases or the generation.
    pub fn cancellation(&self) -> CancellationToken {
        self.generation.cancellation.child_token()
    }
}

impl<T: Send + Sync + 'static> Clone for Lease<T> {
    fn clone(&self) -> Self {
        self.generation.leases.fetch_add(1, Ordering::AcqRel);
        Self {
            generation: self.generation.clone(),
            host: self.host.clone(),
        }
    }
}

impl<T: Send + Sync + 'static> Drop for Lease<T> {
    fn drop(&mut self) {
        if self.generation.leases.fetch_sub(1, Ordering::AcqRel) == 1 {
            self.host.retire_ready(false);
            self.host.changed.notify_waiters();
        }
    }
}

struct Retiring<T> {
    generation: Arc<Generation<T>>,
    cleaning: bool,
}

struct State<T> {
    active: Option<Arc<Generation<T>>>,
    retiring: BTreeMap<u64, Retiring<T>>,
    next_id: u64,
    pending_activations: usize,
    closing: bool,
    cleanup_deadline: Option<Instant>,
    shutdown_result: Option<Result<(), KernelError>>,
    last_activation_error: Option<KernelError>,
    cleanup_errors: Vec<Issue>,
}

struct Inner<T> {
    state: Mutex<State<T>>,
    activation_gate: tokio::sync::Mutex<()>,
    cancel_activation: CancellationToken,
    cleanup_expired: CancellationToken,
    changed: Notify,
    options: HostOptions,
    executor: Handle,
}

/// One owner of an isolated set of generations. Use inside a running Tokio runtime.
/// Call `shutdown().await` before tearing down the executor; Drop only starts shutdown.
pub struct Host<T: Send + Sync + 'static> {
    inner: Arc<Inner<T>>,
}

impl<T: Send + Sync + 'static> Host<T> {
    pub fn new(options: HostOptions) -> Self {
        Self {
            inner: Arc::new(Inner {
                state: Mutex::new(State {
                    active: None,
                    retiring: BTreeMap::new(),
                    next_id: 1,
                    pending_activations: 0,
                    closing: false,
                    cleanup_deadline: None,
                    shutdown_result: None,
                    last_activation_error: None,
                    cleanup_errors: Vec::new(),
                }),
                activation_gate: tokio::sync::Mutex::new(()),
                cancel_activation: CancellationToken::new(),
                cleanup_expired: CancellationToken::new(),
                changed: Notify::new(),
                options,
                executor: Handle::current(),
            }),
        }
    }

    /// Transfers ownership immediately, even if the returned waiter is never polled.
    /// Dropping the waiter does not cancel activation; use Context.cancellation for that.
    pub fn activate(
        &self,
        candidate: Candidate<T>,
        context: Context,
    ) -> impl Future<Output = Result<GenerationInfo, KernelError>> + Send + use<T> {
        let Candidate {
            version,
            fingerprint,
            value,
            components,
        } = candidate;
        let resources = Resources::new(components);
        let (sender, receiver) = oneshot::channel();
        let mut state = self.inner.state.lock().unwrap();
        if state.closing {
            drop(state);
            // Rejected candidates never start; their inactive allocations must be RAII-owned.
            drop(resources);
            let _ = sender.send(Err(Issue::new(Phase::Host, None, "Host is closing").into()));
        } else {
            state.pending_activations += 1;
            drop(state);
            let inner = self.inner.clone();
            self.inner.executor.spawn(async move {
                let result = inner
                    .activate_owned(version, fingerprint, value, resources, context)
                    .await;
                {
                    let mut state = inner.state.lock().unwrap();
                    state.pending_activations -= 1;
                    state.last_activation_error = result.as_ref().err().cloned();
                }
                inner.changed.notify_waiters();
                let _ = sender.send(result);
            });
        }
        async move {
            receiver.await.unwrap_or_else(|_| {
                Err(Issue::new(
                    Phase::Host,
                    None,
                    "Activation task lost; executor or lifecycle failed",
                )
                .into())
            })
        }
    }

    pub fn acquire(&self) -> Result<Lease<T>, KernelError> {
        let state = self.inner.state.lock().unwrap();
        let generation = state
            .active
            .as_ref()
            .filter(|_| !state.closing)
            .ok_or_else(|| {
                KernelError::from(Issue::new(Phase::Host, None, "No active generation"))
            })?;
        generation.leases.fetch_add(1, Ordering::AcqRel);
        Ok(Lease {
            generation: generation.clone(),
            host: self.inner.clone(),
        })
    }

    pub fn status(&self) -> Status {
        let state = self.inner.state.lock().unwrap();
        Status {
            accepting: !state.closing && state.active.is_some(),
            closing: state.closing,
            active: state.active.as_ref().map(|generation| generation.status()),
            retiring: state
                .retiring
                .values()
                .map(|retiring| retiring.generation.status())
                .collect(),
            pending_activations: state.pending_activations,
            last_activation_error: state.last_activation_error.clone(),
            cleanup_errors: state.cleanup_errors.clone(),
        }
    }

    /// Stops admission immediately. All waiters observe the same completed report.
    pub fn shutdown(&self) -> impl Future<Output = Result<(), KernelError>> + Send + use<T> {
        self.inner.begin_shutdown();
        let inner = self.inner.clone();
        async move {
            loop {
                let changed = inner.changed.notified();
                if let Some(result) = inner.state.lock().unwrap().shutdown_result.clone() {
                    return result;
                }
                changed.await;
            }
        }
    }
}

impl<T: Send + Sync + 'static> Drop for Host<T> {
    fn drop(&mut self) {
        self.inner.begin_shutdown();
    }
}

impl<T: Send + Sync + 'static> Inner<T> {
    fn cleanup_deadline(&self) -> Instant {
        let local = Instant::now() + self.options.cleanup_timeout;
        self.state
            .lock()
            .unwrap()
            .cleanup_deadline
            .map_or(local, |deadline| deadline.min(local))
    }

    async fn activate_owned(
        self: &Arc<Self>,
        version: String,
        fingerprint: Option<String>,
        value: T,
        resources: Resources,
        context: Context,
    ) -> Result<GenerationInfo, KernelError> {
        let mut owned = Some((value, resources));
        let outcome = async {
            let (_, resources) = owned.as_mut().unwrap();
            resources.sort()?;
            let _gate = tokio::select! {
                biased;
                _ = self.cancel_activation.cancelled() => return Err(Issue::new(Phase::Start, None, "Host is closing").into()),
                _ = context.cancellation.cancelled() => return Err(Issue::new(Phase::Start, None, "Activation cancelled").into()),
                _ = tokio::time::sleep_until(context.deadline) => return Err(Issue::new(Phase::Start, None, "Startup deadline exceeded").into()),
                gate = self.activation_gate.lock() => gate,
            };
            resources.start(&context, &self.cancel_activation).await?;
            let mut state = self.state.lock().unwrap();
            // Publication and root-lease acquisition share the same lock.
            if state.closing || context.cancellation.is_cancelled() || Instant::now() >= context.deadline {
                return Err(Issue::new(Phase::Start, None, "Activation cancelled, expired, or host closing before publication").into());
            }
            let info = GenerationInfo { id: state.next_id, version, fingerprint };
            state.next_id += 1;
            let (value, resources) = owned.take().unwrap();
            let generation = Arc::new(Generation { info: info.clone(), value, leases: AtomicUsize::new(0),
                cancellation: resources.cancellation.clone(), resources: Mutex::new(Some(resources)) });
            if let Some(old) = state.active.replace(generation) {
                state.retiring.insert(old.info.id, Retiring { generation: old, cleaning: false });
            }
            Ok(info)
        }.await;
        if let Some((_, mut resources)) = owned {
            let cleanup = resources
                .stop(self.cleanup_deadline(), &self.cleanup_expired)
                .await;
            self.state
                .lock()
                .unwrap()
                .cleanup_errors
                .extend(cleanup.clone());
            let mut error: KernelError = outcome.unwrap_err();
            error.issues.extend(cleanup);
            return Err(error);
        }
        self.retire_ready(false);
        self.changed.notify_waiters();
        outcome
    }

    fn retire_ready(self: &Arc<Self>, force: bool) {
        let ready: Vec<_> = {
            let mut state = self.state.lock().unwrap();
            state
                .retiring
                .values_mut()
                .filter_map(|retiring| {
                    if !retiring.cleaning
                        && (force || retiring.generation.leases.load(Ordering::Acquire) == 0)
                    {
                        retiring.cleaning = true;
                        Some(retiring.generation.clone())
                    } else {
                        None
                    }
                })
                .collect()
        };
        for generation in ready {
            let inner = self.clone();
            self.executor.spawn(async move {
                let mut resources = generation.resources.lock().unwrap().take().unwrap();
                let issues = resources
                    .stop(inner.cleanup_deadline(), &inner.cleanup_expired)
                    .await;
                {
                    let mut state = inner.state.lock().unwrap();
                    state.cleanup_errors.extend(issues);
                    state.retiring.remove(&generation.info.id);
                }
                inner.changed.notify_waiters();
            });
        }
    }

    fn begin_shutdown(self: &Arc<Self>) {
        let drain_deadline = Instant::now() + self.options.shutdown_grace;
        {
            let mut state = self.state.lock().unwrap();
            if state.closing {
                return;
            }
            state.closing = true;
            state.cleanup_deadline = Some(drain_deadline + self.options.cleanup_timeout);
            if let Some(active) = state.active.take() {
                state.retiring.insert(
                    active.info.id,
                    Retiring {
                        generation: active,
                        cleaning: false,
                    },
                );
            }
        }
        self.cancel_activation.cancel();
        self.retire_ready(false);
        let inner = self.clone();
        self.executor.spawn(async move {
            inner.finish_shutdown(drain_deadline).await;
        });
    }

    async fn finish_shutdown(self: Arc<Self>, drain_deadline: Instant) {
        loop {
            let changed = self.changed.notified();
            let leases: usize = self
                .state
                .lock()
                .unwrap()
                .retiring
                .values()
                .map(|retiring| retiring.generation.leases.load(Ordering::Acquire))
                .sum();
            if leases == 0 {
                break;
            }
            if Instant::now() >= drain_deadline {
                self.state.lock().unwrap().cleanup_errors.push(Issue::new(
                    Phase::Drain,
                    None,
                    format!("Shutdown drain deadline exceeded with {leases} leases"),
                ));
                break;
            }
            tokio::select! { _ = changed => {}, _ = tokio::time::sleep_until(drain_deadline) => {} }
        }
        let deadline = self.cleanup_deadline();
        self.state.lock().unwrap().cleanup_deadline = Some(deadline);
        // Normal reload never takes this force path. Shutdown cancels remaining uses.
        self.retire_ready(true);
        loop {
            let changed = self.changed.notified();
            {
                let mut state = self.state.lock().unwrap();
                if state.pending_activations == 0 && state.retiring.is_empty() {
                    state.shutdown_result = Some(if state.cleanup_errors.is_empty() {
                        Ok(())
                    } else {
                        Err(KernelError {
                            issues: state.cleanup_errors.clone(),
                        })
                    });
                    drop(state);
                    self.changed.notify_waiters();
                    return;
                }
            }
            if self.cleanup_expired.is_cancelled() {
                // Each owned task still runs every synchronous begin_stop before reporting.
                changed.await;
            } else {
                tokio::select! {
                    _ = changed => {},
                    _ = tokio::time::sleep_until(deadline) => self.cleanup_expired.cancel(),
                }
            }
        }
    }
}
