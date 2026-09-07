use std::{
    sync::{Arc, Mutex},
    time::Duration,
};

use async_trait::async_trait;
use nyro_kernel::{
    CancellationToken, Candidate, Component, Context, Host, HostOptions, Lifecycle, Phase,
};
use tokio::{
    sync::Semaphore,
    time::{Instant, timeout},
};

type Events = Arc<Mutex<Vec<String>>>;

struct Resource {
    id: String,
    events: Events,
    start_gate: Option<Arc<Semaphore>>,
    stop_gate: Option<Arc<Semaphore>>,
    fail_start: bool,
    fail_stop: bool,
    stopping: bool,
}

#[async_trait]
impl Lifecycle for Resource {
    async fn start(&mut self, _: &Context) -> Result<(), String> {
        self.events
            .lock()
            .unwrap()
            .push(format!("start:{}", self.id));
        if let Some(gate) = &self.start_gate {
            gate.acquire().await.unwrap().forget();
        }
        if self.fail_start {
            Err("start failed".into())
        } else {
            Ok(())
        }
    }

    fn begin_stop(&mut self) {
        if !self.stopping {
            self.stopping = true;
            self.events
                .lock()
                .unwrap()
                .push(format!("stop:{}", self.id));
        }
    }

    async fn wait_stopped(&mut self, _: &Context) -> Result<(), String> {
        self.events
            .lock()
            .unwrap()
            .push(format!("wait:{}", self.id));
        if let Some(gate) = &self.stop_gate {
            gate.acquire().await.unwrap().forget();
        }
        if self.fail_stop {
            Err("stop failed".into())
        } else {
            Ok(())
        }
    }
}

fn resource(id: &str, events: &Events) -> Resource {
    Resource {
        id: id.into(),
        events: events.clone(),
        start_gate: None,
        stop_gate: None,
        fail_start: false,
        fail_stop: false,
        stopping: false,
    }
}

fn component(resource: Resource, after: &[&str]) -> Component {
    Component {
        id: resource.id.clone(),
        after: after.iter().map(|id| (*id).into()).collect(),
        lifecycle: Box::new(resource),
    }
}

fn candidate(version: &str, components: Vec<Component>) -> Candidate<String> {
    Candidate {
        version: version.into(),
        fingerprint: Some(format!("fp:{version}")),
        value: version.into(),
        components,
    }
}

fn context() -> Context {
    Context {
        deadline: Instant::now() + Duration::from_secs(10),
        cancellation: CancellationToken::new(),
    }
}

async fn until(mut predicate: impl FnMut() -> bool) {
    timeout(Duration::from_secs(2), async {
        while !predicate() {
            tokio::task::yield_now().await;
        }
    })
    .await
    .expect("condition did not become true");
}

fn contains(events: &Events, event: &str) -> bool {
    events.lock().unwrap().iter().any(|item| item == event)
}

#[tokio::test]
async fn starts_in_dependency_order_and_stops_in_reverse_despite_error() {
    let events = Events::default();
    let host = Host::new(HostOptions::default());
    let mut b = resource("b", &events);
    b.fail_stop = true;
    host.activate(
        candidate(
            "one",
            vec![
                component(resource("z", &events), &["a"]),
                component(b, &[]),
                component(resource("a", &events), &[]),
            ],
        ),
        context(),
    )
    .await
    .unwrap();
    let error = host.shutdown().await.unwrap_err();
    assert_eq!(error.issues.len(), 1);
    assert_eq!(error.issues[0].component.as_deref(), Some("b"));
    assert_eq!(
        *events.lock().unwrap(),
        [
            "start:a", "start:b", "start:z", "stop:z", "wait:z", "stop:b", "wait:b", "stop:a",
            "wait:a"
        ]
    );
    assert_eq!(host.shutdown().await.unwrap_err(), error);
}

#[tokio::test]
async fn invalid_graph_never_starts_and_cleans_candidate_allocations() {
    let events = Events::default();
    let host = Host::new(HostOptions::default());
    let error = host
        .activate(
            candidate("bad", vec![component(resource("a", &events), &["missing"])]),
            context(),
        )
        .await
        .unwrap_err();
    assert_eq!(error.issues[0].phase, Phase::Validate);
    assert_eq!(*events.lock().unwrap(), ["stop:a", "wait:a"]);
    assert!(!host.status().accepting);
    host.shutdown().await.unwrap();
}

#[tokio::test]
async fn failed_start_rolls_back_partial_and_unstarted_resources_preserving_active() {
    let events = Events::default();
    let host = Host::new(HostOptions::default());
    host.activate(candidate("old", vec![]), context())
        .await
        .unwrap();
    let mut b = resource("b", &events);
    b.fail_start = true;
    b.fail_stop = true;
    let error = host
        .activate(
            candidate(
                "new",
                vec![
                    component(resource("a", &events), &[]),
                    component(b, &["a"]),
                    component(resource("c", &events), &["b"]),
                ],
            ),
            context(),
        )
        .await
        .unwrap_err();
    assert_eq!(error.issues.len(), 2);
    assert_eq!(error.issues[0].phase, Phase::Start);
    assert_eq!(error.issues[1].phase, Phase::Stop);
    assert_eq!(host.acquire().unwrap().value(), "old");
    assert_eq!(host.status().last_activation_error, Some(error));
    assert_eq!(
        *events.lock().unwrap(),
        [
            "start:a", "start:b", "stop:c", "wait:c", "stop:b", "wait:b", "stop:a", "wait:a"
        ]
    );
    assert!(
        host.shutdown().await.is_err(),
        "shutdown reports earlier cleanup failure"
    );
}

#[tokio::test]
async fn each_retiring_generation_waits_for_its_last_cloned_lease() {
    let events = Events::default();
    let host = Host::new(HostOptions::default());
    host.activate(
        candidate("a", vec![component(resource("a", &events), &[])]),
        context(),
    )
    .await
    .unwrap();
    let a = host.acquire().unwrap();
    let stream = a.clone();
    host.activate(
        candidate("b", vec![component(resource("b", &events), &[])]),
        context(),
    )
    .await
    .unwrap();
    let b = host.acquire().unwrap();
    host.activate(candidate("c", vec![]), context())
        .await
        .unwrap();
    assert_eq!(host.status().retiring.len(), 2);
    drop(a);
    assert_eq!(stream.value(), "a");
    assert_eq!(stream.generation().fingerprint.as_deref(), Some("fp:a"));
    assert!(!stream.cancellation().is_cancelled());
    assert!(!contains(&events, "stop:a"));
    drop(b);
    until(|| contains(&events, "wait:b")).await;
    assert!(!contains(&events, "stop:a"));
    drop(stream);
    until(|| host.status().retiring.is_empty()).await;
    assert!(contains(&events, "wait:a"));
    host.shutdown().await.unwrap();
}

#[tokio::test]
async fn cancelled_activation_waiter_does_not_abandon_owned_transaction() {
    let events = Events::default();
    let host = Host::new(HostOptions::default());
    let gate = Arc::new(Semaphore::new(0));
    let mut a = resource("a", &events);
    a.start_gate = Some(gate.clone());
    let waiter = tokio::spawn(host.activate(candidate("a", vec![component(a, &[])]), context()));
    until(|| contains(&events, "start:a")).await;
    waiter.abort();
    gate.add_permits(1);
    until(|| host.status().accepting).await;
    assert_eq!(host.acquire().unwrap().value(), "a");
    host.shutdown().await.unwrap();
    assert!(contains(&events, "wait:a"));
}

#[tokio::test(start_paused = true)]
async fn activation_deadline_and_cancellation_roll_back_and_keep_current() {
    for cancel in [false, true] {
        let events = Events::default();
        let host = Host::new(HostOptions::default());
        host.activate(candidate("old", vec![]), context())
            .await
            .unwrap();
        let mut a = resource("a", &events);
        a.start_gate = Some(Arc::new(Semaphore::new(0)));
        let ctx = context();
        let token = ctx.cancellation.clone();
        let waiter = tokio::spawn(host.activate(candidate("new", vec![component(a, &[])]), ctx));
        until(|| contains(&events, "start:a")).await;
        if cancel {
            token.cancel();
        } else {
            tokio::time::advance(Duration::from_secs(10)).await;
        }
        let error = waiter.await.unwrap().unwrap_err();
        assert_eq!(error.issues[0].phase, Phase::Start);
        assert_eq!(host.acquire().unwrap().value(), "old");
        assert!(contains(&events, "wait:a"));
        host.shutdown().await.unwrap();
    }
}

#[tokio::test(start_paused = true)]
async fn shutdown_has_separate_drain_and_cleanup_budgets_and_rejects_immediately() {
    let events = Events::default();
    let host = Host::new(HostOptions {
        shutdown_grace: Duration::from_secs(30),
        cleanup_timeout: Duration::from_secs(5),
    });
    let mut b = resource("b", &events);
    b.stop_gate = Some(Arc::new(Semaphore::new(0)));
    host.activate(
        candidate(
            "one",
            vec![component(resource("a", &events), &[]), component(b, &["a"])],
        ),
        context(),
    )
    .await
    .unwrap();
    let stream = host.acquire().unwrap();
    let shutdown = host.shutdown();
    assert!(host.acquire().is_err());
    let rejected = host.activate(candidate("late", vec![]), context());
    assert!(rejected.await.is_err());
    let waiter = tokio::spawn(shutdown);
    tokio::time::advance(Duration::from_secs(29)).await;
    assert!(!stream.cancellation().is_cancelled());
    assert!(!contains(&events, "stop:b"));
    tokio::time::advance(Duration::from_secs(1)).await;
    until(|| contains(&events, "wait:b")).await;
    assert!(stream.cancellation().is_cancelled());
    assert!(!waiter.is_finished());
    tokio::time::advance(Duration::from_secs(5)).await;
    let error = waiter.await.unwrap().unwrap_err();
    assert!(error.issues.iter().any(|issue| issue.phase == Phase::Drain));
    assert!(error.issues.iter().any(|issue| issue.phase == Phase::Stop));
    assert!(
        contains(&events, "stop:a"),
        "budget exhaustion must not skip begin_stop"
    );
    assert_eq!(host.shutdown().await.unwrap_err(), error);
    drop(stream);
}

#[tokio::test]
async fn shutdown_cancels_blocked_activation_and_survives_dropped_waiter() {
    let events = Events::default();
    let host = Host::new(HostOptions::default());
    host.activate(
        candidate("old", vec![component(resource("old", &events), &[])]),
        context(),
    )
    .await
    .unwrap();
    let mut a = resource("a", &events);
    a.start_gate = Some(Arc::new(Semaphore::new(0)));
    let activation =
        tokio::spawn(host.activate(candidate("new", vec![component(a, &[])]), context()));
    until(|| contains(&events, "start:a")).await;
    drop(host.shutdown());
    host.shutdown().await.unwrap();
    assert!(activation.await.unwrap().is_err());
    assert!(!host.status().accepting);
    assert!(contains(&events, "wait:a"));
    assert!(contains(&events, "wait:old"));
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn acquire_and_publish_never_mix_runtime_and_metadata_and_hosts_are_isolated() {
    let host = Arc::new(Host::new(HostOptions::default()));
    let other = Host::new(HostOptions::default());
    host.activate(candidate("0", vec![]), context())
        .await
        .unwrap();
    other
        .activate(candidate("isolated", vec![]), context())
        .await
        .unwrap();
    let reader = {
        let host = host.clone();
        tokio::spawn(async move {
            for _ in 0..2000 {
                let lease = host.acquire().unwrap();
                assert_eq!(lease.value(), &lease.generation().version);
                tokio::task::yield_now().await;
            }
        })
    };
    for version in 1..100 {
        host.activate(candidate(&version.to_string(), vec![]), context())
            .await
            .unwrap();
    }
    reader.await.unwrap();
    host.shutdown().await.unwrap();
    assert_eq!(other.acquire().unwrap().value(), "isolated");
    other.shutdown().await.unwrap();
}

#[tokio::test]
async fn host_drop_initiates_owned_cleanup() {
    let events = Events::default();
    let host = Host::new(HostOptions::default());
    host.activate(
        candidate("a", vec![component(resource("a", &events), &[])]),
        context(),
    )
    .await
    .unwrap();
    drop(host);
    until(|| contains(&events, "wait:a")).await;
}

#[tokio::test(start_paused = true)]
async fn queued_activation_obeys_its_deadline_without_starting() {
    let events = Events::default();
    let host = Host::new(HostOptions::default());
    let gate = Arc::new(Semaphore::new(0));
    let mut a = resource("a", &events);
    a.start_gate = Some(gate.clone());
    let first = host.activate(candidate("a", vec![component(a, &[])]), context());
    until(|| contains(&events, "start:a")).await;
    let mut ctx = context();
    ctx.deadline = Instant::now() + Duration::from_secs(1);
    let queued = host.activate(
        candidate("b", vec![component(resource("b", &events), &[])]),
        ctx,
    );
    tokio::time::advance(Duration::from_secs(1)).await;
    assert!(queued.await.is_err());
    assert!(!contains(&events, "start:b"));
    assert!(contains(&events, "wait:b"));
    gate.add_permits(1);
    first.await.unwrap();
    assert_eq!(host.acquire().unwrap().value(), "a");
    host.shutdown().await.unwrap();
}

#[tokio::test(start_paused = true)]
async fn normal_reload_never_forces_a_stream_and_its_finalizer() {
    let events = Events::default();
    let host = Host::new(HostOptions::default());
    host.activate(
        candidate("old", vec![component(resource("old", &events), &[])]),
        context(),
    )
    .await
    .unwrap();
    let lease = host.acquire().unwrap();
    let stream_end = CancellationToken::new();
    let finalizer = Arc::new(Semaphore::new(0));
    let request = {
        let end = stream_end.clone();
        let finalizer = finalizer.clone();
        tokio::spawn(async move {
            let cancelled = lease.cancellation();
            tokio::select! { _ = end.cancelled() => {}, _ = cancelled.cancelled() => panic!("reload cancelled stream") }
            finalizer.acquire().await.unwrap().forget();
            assert_eq!(lease.value(), "old");
        })
    };
    host.activate(candidate("new", vec![]), context())
        .await
        .unwrap();
    tokio::time::advance(Duration::from_secs(3600)).await;
    assert!(!contains(&events, "stop:old"));
    stream_end.cancel();
    tokio::task::yield_now().await;
    assert!(!contains(&events, "stop:old"), "finalizer still owns lease");
    finalizer.add_permits(1);
    request.await.unwrap();
    until(|| host.status().retiring.is_empty()).await;
    assert!(contains(&events, "wait:old"));
    host.shutdown().await.unwrap();
}

#[tokio::test(start_paused = true)]
async fn zero_cleanup_budget_still_signals_every_owner_and_reports_incomplete() {
    let events = Events::default();
    let host = Host::new(HostOptions {
        shutdown_grace: Duration::ZERO,
        cleanup_timeout: Duration::ZERO,
    });
    host.activate(
        candidate(
            "one",
            vec![
                component(resource("a", &events), &[]),
                component(resource("b", &events), &["a"]),
            ],
        ),
        context(),
    )
    .await
    .unwrap();
    let error = host.shutdown().await.unwrap_err();
    assert_eq!(error.issues.len(), 2);
    assert_eq!(
        *events.lock().unwrap(),
        ["start:a", "start:b", "stop:b", "stop:a"]
    );
}

#[tokio::test(start_paused = true)]
async fn shutdown_joins_an_already_running_retirement_without_resetting_its_budget() {
    let events = Events::default();
    let host = Host::new(HostOptions::default());
    let mut old = resource("old", &events);
    old.stop_gate = Some(Arc::new(Semaphore::new(0)));
    host.activate(candidate("old", vec![component(old, &[])]), context())
        .await
        .unwrap();
    host.activate(candidate("new", vec![]), context())
        .await
        .unwrap();
    until(|| contains(&events, "wait:old")).await;
    tokio::time::advance(Duration::from_secs(4)).await;
    let closing = host.shutdown();
    tokio::time::advance(Duration::from_secs(1)).await;
    let error = closing.await.unwrap_err();
    assert_eq!(error.issues.len(), 1);
    assert_eq!(error.issues[0].component.as_deref(), Some("old"));
    assert_eq!(
        events
            .lock()
            .unwrap()
            .iter()
            .filter(|e| *e == "stop:old")
            .count(),
        1
    );
}

#[tokio::test]
async fn cancellation_is_scoped_and_completed_activation_ignores_caller_cancellation() {
    let host = Host::new(HostOptions::default());
    let ctx = context();
    let caller = ctx.cancellation.clone();
    host.activate(candidate("one", vec![]), ctx).await.unwrap();
    let lease = host.acquire().unwrap();
    let isolated = lease.cancellation();
    isolated.cancel();
    caller.cancel();
    assert!(!lease.cancellation().is_cancelled());
    drop(lease);
    host.shutdown().await.unwrap();
}

#[tokio::test]
async fn dependencies_remain_running_until_dependents_finish_cleanup() {
    struct OrderedResource {
        signal: Arc<Mutex<Option<CancellationToken>>>,
        dependency: Option<Arc<Mutex<Option<CancellationToken>>>>,
        fail_start: bool,
    }

    #[async_trait]
    impl Lifecycle for OrderedResource {
        async fn start(&mut self, context: &Context) -> Result<(), String> {
            *self.signal.lock().unwrap() = Some(context.cancellation.clone());
            if self.fail_start {
                Err("partial startup".into())
            } else {
                Ok(())
            }
        }
        fn begin_stop(&mut self) {}
        async fn wait_stopped(&mut self, _: &Context) -> Result<(), String> {
            if let Some(dependency) = &self.dependency
                && dependency.lock().unwrap().as_ref().unwrap().is_cancelled()
            {
                return Err("Dependency cancelled before dependent finished cleanup".into());
            }
            if !self.signal.lock().unwrap().as_ref().unwrap().is_cancelled() {
                return Err("Own cancellation signal was not set before stopping".into());
            }
            Ok(())
        }
    }

    for rollback in [false, true] {
        let a = Arc::new(Mutex::new(None));
        let b = Arc::new(Mutex::new(None));
        let host = Host::new(HostOptions::default());
        let result = host
            .activate(
                candidate(
                    "one",
                    vec![
                        Component {
                            id: "a".into(),
                            after: vec![],
                            lifecycle: Box::new(OrderedResource {
                                signal: a.clone(),
                                dependency: None,
                                fail_start: false,
                            }),
                        },
                        Component {
                            id: "b".into(),
                            after: vec!["a".into()],
                            lifecycle: Box::new(OrderedResource {
                                signal: b.clone(),
                                dependency: Some(a.clone()),
                                fail_start: rollback,
                            }),
                        },
                    ],
                ),
                context(),
            )
            .await;
        if rollback {
            assert_eq!(
                result.unwrap_err().issues.len(),
                1,
                "rollback must not stop dependency early"
            );
        } else {
            result.unwrap();
            host.activate(candidate("two", vec![]), context())
                .await
                .unwrap();
            until(|| host.status().retiring.is_empty()).await;
        }
        host.shutdown().await.unwrap();
        assert!(a.lock().unwrap().as_ref().unwrap().is_cancelled());
        assert!(b.lock().unwrap().as_ref().unwrap().is_cancelled());
    }
}
