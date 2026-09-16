use super::*;
use nyro_llm::{config::Strategy, runtime::SharedResources};

#[test]
fn optional_routing_strategies_are_strict_and_default_to_weighted() {
    let base = json!({
        "providers":{"p":{"kind":"openai","base_url":"http://127.0.0.1:1/v1"}},
        "models":{"public":{"provider":"p","upstream_model":"private","workloads":["chat"]}}
    });
    let default: Config = serde_json::from_value(base.clone()).unwrap();
    assert_eq!(
        serde_json::to_value(default).unwrap()["models"]["public"]["strategy"],
        "weighted"
    );
    for strategy in [json!("weighted"), json!("least_recent"), json!("latency")] {
        let mut input = base.clone();
        input["models"]["public"]["strategy"] = strategy.clone();
        let config: Config = serde_json::from_value(input).unwrap();
        config.validate().unwrap();
        assert_eq!(
            serde_json::to_value(config).unwrap()["models"]["public"]["strategy"],
            strategy
        );
    }
    for invalid in [
        json!(null),
        json!("cooldown"),
        json!("priority"),
        json!("Weighted"),
        json!(true),
    ] {
        let mut input = base.clone();
        input["models"]["public"]["strategy"] = invalid;
        assert!(serde_json::from_value::<Config>(input).is_err());
    }
}

#[tokio::test]
async fn least_recent_distributes_pending_attempts_before_any_response() {
    let first = upstream(Reply::Good).await;
    let second = upstream(Reply::Good).await;
    first.delay(Duration::from_secs(30));
    second.delay(Duration::from_secs(30));
    let mut value = serde_json::to_value(configuration(&[&first, &second], 1, None)).unwrap();
    value["models"]["public"]["strategy"] = json!("least_recent");
    value["models"]["public"]["backends"][1]["priority"] = json!(0);
    value["models"]["public"]["backends"][0]["weight"] = json!(u32::MAX);
    value["models"]["public"]["backends"][1]["weight"] = json!(1);
    let config = serde_json::from_value(value).unwrap();
    let limit = ConcurrencyLimit::new(12).unwrap();
    let runtime = Arc::new(runtime(config, &limit, Options::default(), &Arc::default()));
    let mut tasks = Vec::new();
    for _ in 0..12 {
        let runtime = runtime.clone();
        tasks.push(tokio::spawn(async move { invoke(&runtime, false).await }));
    }
    first.wait_for(6).await;
    second.wait_for(6).await;
    assert_eq!((first.count(), second.count()), (6, 6));
    for task in &tasks {
        task.abort();
    }
    for task in tasks {
        assert!(task.await.unwrap_err().is_cancelled());
    }
    assert_eq!(limit.available(), 12);
}

#[tokio::test]
async fn latency_samples_unknown_backends_then_prefers_faster_headers_over_weight() {
    let slow = upstream(Reply::Good).await;
    let fast = upstream(Reply::Good).await;
    slow.delay(Duration::from_millis(150));
    let mut config = configuration(&[&slow, &fast], 1, None);
    let model = config.models.get_mut("public").unwrap();
    model.strategy = nyro_llm::config::Strategy::Latency;
    model.backends[1].priority = 0;
    model.backends[0].weight = u32::MAX;
    model.backends[1].weight = 1;
    let limit = ConcurrencyLimit::new(1).unwrap();
    let runtime = runtime(config, &limit, Options::default(), &Arc::default());
    for _ in 0..2 {
        assert!(invoke(&runtime, false).await.status().is_success());
    }
    assert_eq!((slow.count(), fast.count()), (1, 1));
    for _ in 0..20 {
        assert!(invoke(&runtime, false).await.status().is_success());
    }
    // 5% exploration can revisit slow; disabled latency selection always sends to its huge weight.
    assert!(
        fast.count() >= 12,
        "slow={}, fast={}",
        slow.count(),
        fast.count()
    );
}

fn shared(config: Config, resources: &SharedResources, limit: &ConcurrencyLimit) -> Runtime {
    Runtime::with_resources(
        config,
        Arc::new(
            ApiKeys::new(vec![ApiKey {
                id: "alice".into(),
                secret: "client-secret".into(),
                enabled: true,
                expires_at: None,
            }])
            .unwrap(),
        ),
        limit.clone(),
        Options::default(),
        resources.clone(),
    )
    .unwrap()
}

#[tokio::test]
async fn strategies_share_filtering_health_failover_and_disabled_backend_contracts() {
    for strategy in [Strategy::LeastRecent, Strategy::Latency] {
        let disabled = upstream(Reply::Good).await;
        let first = upstream(Reply::Status(503)).await;
        let second = upstream(Reply::Status(504)).await;
        let fallback = upstream(Reply::Good).await;
        let mut config = configuration(
            &[&disabled, &first, &second, &fallback],
            3,
            Some((1, 30_000)),
        );
        let model = config.models.get_mut("public").unwrap();
        model.strategy = strategy;
        model.backends[0].weight = 0;
        model.backends[1].priority = 0;
        model.backends[2].priority = 0;
        let limit = ConcurrencyLimit::new(1).unwrap();
        let runtime = runtime(config, &limit, Options::default(), &Arc::default());
        for stream in [false, true] {
            let response = invoke(&runtime, stream).await;
            assert!(response.status().is_success());
            assert!(consume(response).await.contains("From selected backend"));
        }
        assert_eq!(
            (
                disabled.count(),
                first.count(),
                second.count(),
                fallback.count()
            ),
            (0, 1, 1, 2)
        );
        assert_eq!(limit.available(), 1);
    }
}

#[tokio::test]
async fn rejected_quota_keeps_recent_history_and_reload_retains_unchanged_bindings() {
    let first = upstream(Reply::Good).await;
    let second = upstream(Reply::Good).await;
    let mut config = configuration(&[&first, &second], 1, None);
    let model = config.models.get_mut("public").unwrap();
    model.strategy = Strategy::LeastRecent;
    model.quota = Some(nyro_llm::config::QuotaConfig {
        total_tokens: 3,
        reserve_tokens: 3,
    });
    let resources = SharedResources::default();
    let limit = ConcurrencyLimit::new(1).unwrap();
    let old = shared(config.clone(), &resources, &limit);
    assert!(
        consume(invoke(&old, false).await)
            .await
            .contains("From selected backend")
    );
    assert_eq!((first.count(), second.count()), (1, 0));
    config.models.get_mut("public").unwrap().backends[1].priority = 0;
    let candidate = shared(config.clone(), &resources, &limit);
    assert_eq!(
        invoke(&candidate, false).await.status(),
        StatusCode::TOO_MANY_REQUESTS
    );
    assert_eq!((first.count(), second.count()), (1, 0));
    config.models.get_mut("public").unwrap().quota = None;
    let published = shared(config.clone(), &resources, &limit);
    assert!(invoke(&published, false).await.status().is_success());
    assert_eq!((first.count(), second.count()), (1, 1));
    // Reordered and reweighted config still uses existing recent history.
    config.models.get_mut("public").unwrap().backends.reverse();
    config.models.get_mut("public").unwrap().backends[0].weight = u32::MAX;
    let reordered = shared(config.clone(), &resources, &limit);
    assert!(invoke(&reordered, true).await.status().is_success());
    assert_eq!((first.count(), second.count()), (2, 1));
    // Credential rotation creates a fresh b1 binding, despite identical public/backend names.
    config.providers.get_mut("p1").unwrap().api_key = Some("rotated-secret".into());
    let rotated = shared(config, &resources, &limit);
    assert!(invoke(&rotated, false).await.status().is_success());
    assert_eq!((first.count(), second.count()), (2, 2));
    assert_eq!(limit.available(), 1);
}

#[tokio::test]
async fn latency_uses_headers_before_json_or_first_stream_frame() {
    for stream in [false, true] {
        let slow_headers = upstream(Reply::Good).await;
        let slow_body = upstream(Reply::SlowBody).await;
        slow_headers.delay(Duration::from_millis(120));
        let mut config = configuration(&[&slow_headers, &slow_body], 1, None);
        config.models.get_mut("public").unwrap().strategy = Strategy::Latency;
        config.models.get_mut("public").unwrap().backends[1].priority = 0;
        let limit = ConcurrencyLimit::new(1).unwrap();
        let runtime = runtime(config, &limit, Options::default(), &Arc::default());
        for _ in 0..2 {
            assert!(
                consume(invoke(&runtime, stream).await)
                    .await
                    .contains("From selected backend")
            );
        }
        assert_eq!((slow_headers.count(), slow_body.count()), (1, 1));
        for _ in 0..20 {
            assert!(
                consume(invoke(&runtime, stream).await)
                    .await
                    .contains("From selected backend")
            );
        }
        assert!(
            slow_body.count() >= 12,
            "stream={stream}: headers={}, body={}",
            slow_headers.count(),
            slow_body.count()
        );
        assert_eq!(limit.available(), 1);
    }
}

#[tokio::test]
async fn latency_sample_excludes_previous_failed_attempt_and_survives_reload() {
    let failed = upstream(Reply::Status(503)).await;
    let fast = upstream(Reply::Good).await;
    let slow = upstream(Reply::Good).await;
    failed.delay(Duration::from_millis(250));
    slow.delay(Duration::from_millis(100));
    let mut config = configuration(&[&failed, &fast, &slow], 2, None);
    config.models.get_mut("public").unwrap().strategy = Strategy::Latency;
    let resources = SharedResources::default();
    let limit = ConcurrencyLimit::new(1).unwrap();
    let old = shared(config.clone(), &resources, &limit);
    assert!(invoke(&old, false).await.status().is_success());
    assert_eq!((failed.count(), fast.count(), slow.count()), (1, 1, 0));
    let model = config.models.get_mut("public").unwrap();
    model.backends[0].weight = 0;
    model.backends[1].priority = 0;
    model.backends[2].priority = 0;
    let new = shared(config, &resources, &limit);
    assert!(invoke(&new, false).await.status().is_success());
    assert_eq!((failed.count(), fast.count(), slow.count()), (1, 1, 1));
    for _ in 0..20 {
        assert!(invoke(&new, false).await.status().is_success());
    }
    assert!(
        fast.count() >= 12,
        "fast={}, slow={}",
        fast.count(),
        slow.count()
    );
    assert_eq!(failed.count(), 1);
}

#[tokio::test]
async fn strategies_filter_incompatible_protocols_before_priority_and_sampling() {
    for strategy in [Strategy::LeastRecent, Strategy::Latency] {
        let compatible = upstream(Reply::Good).await;
        let incompatible = upstream(Reply::Good).await;
        let disabled = upstream(Reply::Good).await;
        let mut config = configuration(&[&compatible, &incompatible, &disabled], 1, None);
        let model = config.models.get_mut("public").unwrap();
        model.strategy = strategy;
        model.backends[0].priority = 10;
        model.backends[1].priority = 0;
        model.backends[2].priority = 0;
        model.backends[2].weight = 0;
        config.providers.get_mut("p1").unwrap().kind = nyro_llm::config::ProviderKind::Anthropic;
        let limit = ConcurrencyLimit::new(1).unwrap();
        let runtime = runtime(config, &limit, Options::default(), &Arc::default());
        // The input has no token limit and cannot be encoded for Anthropic.
        for stream in [false, true, false] {
            assert!(
                consume(invoke(&runtime, stream).await)
                    .await
                    .contains("From selected backend")
            );
        }
        assert_eq!(
            (compatible.count(), incompatible.count(), disabled.count()),
            (3, 0, 0)
        );
    }
}
