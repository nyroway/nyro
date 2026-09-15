use super::*;
use nyro_llm::runtime::SharedResources;

fn subject_configuration(upstreams: &[&Upstream], count: u32) -> Config {
    let mut config = configuration(upstreams, count);
    config.models.get_mut("public").unwrap().rate = None;
    config.subject_limits =
        serde_json::from_value(json!({"alice":{"rpm":count,"rpd":count}})).unwrap();
    config
}

async fn call(runtime: &Runtime, model: &str, secret: Option<&str>) -> Response<Body> {
    runtime
        .handle(
            request("/v1/chat/completions", chat(model, false), secret),
            CancellationToken::new(),
        )
        .await
}

#[tokio::test]
async fn subject_crosses_models_protocols_and_workloads_while_other_callers_are_isolated() {
    let upstream = upstream(Reply::Good).await;
    let mut config = subject_configuration(&[&upstream], 5);
    config.subject_limits.insert(
        "bob".into(),
        serde_json::from_value(json!({"rpm":1})).unwrap(),
    );
    config.models.get_mut("public").unwrap().allow_anonymous = true;
    config
        .models
        .insert("alias".into(), config.models["public"].clone());
    let limit = ConcurrencyLimit::new(1).unwrap();
    let runtime = runtime(config, &limit, Options::default());
    let inputs = [
        ("/v1/chat/completions", chat("public", false)),
        (
            "/v1/messages",
            json!({"model":"alias","messages":[{"role":"user","content":"Hi"}],"max_tokens":16}),
        ),
        (
            "/v1/responses",
            json!({"model":"public","input":"Hi","max_output_tokens":16}),
        ),
        (
            "/v1beta/models/alias:generateContent",
            json!({"contents":[{"role":"user","parts":[{"text":"Hi"}]}]}),
        ),
        ("/v1/embeddings", json!({"model":"alias","input":"Hi"})),
    ];
    for (path, body) in &inputs {
        let response = runtime
            .handle(
                request(path, body.clone(), Some("alice-secret")),
                CancellationToken::new(),
            )
            .await;
        assert_eq!(
            response.status(),
            StatusCode::OK,
            "{path}: {}",
            consume(response).await
        );
    }
    for (path, body) in inputs {
        let response = runtime
            .handle(
                request(path, body, Some("alice-secret")),
                CancellationToken::new(),
            )
            .await;
        assert_eq!(response.status(), StatusCode::TOO_MANY_REQUESTS, "{path}");
        let retry: u64 = response.headers()["retry-after"]
            .to_str()
            .unwrap()
            .parse()
            .unwrap();
        assert!(
            retry > 86340 && retry <= 86400,
            "day window is the longer wait"
        );
        assert_eq!(limit.available(), 1);
        let body = consume(response).await;
        if path.ends_with("messages") {
            assert_eq!(body["error"]["type"], "rate_limit_error");
        } else if path.contains("generateContent") {
            assert_eq!(body["error"]["status"], "RESOURCE_EXHAUSTED");
        } else {
            assert_eq!(body["error"]["code"], "rate_limit_exceeded");
        }
    }
    assert_eq!(
        call(&runtime, "alias", Some("bob-secret")).await.status(),
        StatusCode::OK
    );
    assert_rate(call(&runtime, "public", Some("bob-secret")).await).await;
    for _ in 0..2 {
        assert_eq!(
            call(&runtime, "public", None).await.status(),
            StatusCode::OK
        );
    }
    assert_eq!(
        call(&runtime, "public", Some("wrong-secret"))
            .await
            .status(),
        StatusCode::UNAUTHORIZED
    );
    assert_eq!(upstream.count(), 8);
}

#[tokio::test]
async fn compound_rejection_charges_neither_subject_nor_model() {
    let upstream = upstream(Reply::Good).await;
    let mut config = configuration(&[&upstream], 1);
    config.subject_limits =
        serde_json::from_value(json!({"alice":{"rpm":1},"bob":{"rpm":1}})).unwrap();
    config
        .models
        .insert("alias".into(), config.models["public"].clone());
    let runtime = runtime(
        config,
        &ConcurrencyLimit::new(1).unwrap(),
        Options::default(),
    );
    assert_eq!(
        call(&runtime, "public", Some("alice-secret"))
            .await
            .status(),
        StatusCode::OK
    );
    assert_rate(call(&runtime, "alias", Some("alice-secret")).await).await;
    assert_rate(call(&runtime, "public", Some("bob-secret")).await).await;
    assert_eq!(
        call(&runtime, "alias", Some("bob-secret")).await.status(),
        StatusCode::OK
    );
    assert_eq!(upstream.count(), 2);
}

#[tokio::test]
async fn matching_native_chat_and_model_discovery_use_the_same_subject_identity() {
    let upstream = upstream(Reply::Good).await;
    let mut config = subject_configuration(&[&upstream], 1);
    config.subject_limits.get_mut("alice").unwrap().rpd = None;
    config.providers.get_mut("p0").unwrap().native_chat = true;
    let runtime = runtime(
        config,
        &ConcurrencyLimit::new(1).unwrap(),
        Options::default(),
    );
    for _ in 0..2 {
        let discovery = Request::builder()
            .uri("/v1/models")
            .header("authorization", "Bearer alice-secret")
            .body(Body::empty())
            .unwrap();
        assert_eq!(
            runtime
                .handle(discovery, CancellationToken::new())
                .await
                .status(),
            StatusCode::OK
        );
    }
    assert_eq!(invoke(&runtime).await.status(), StatusCode::OK);
    assert_rate(invoke(&runtime).await).await;
    assert_eq!(upstream.count(), 1);
}

fn generation(
    config: Config,
    resources: &SharedResources,
    secret: &str,
    enabled: bool,
    expires_at: Option<u64>,
) -> Result<Runtime, nyro_llm::runtime::BuildError> {
    Runtime::with_resources(
        config,
        Arc::new(
            ApiKeys::new(vec![ApiKey {
                id: "alice".into(),
                secret: secret.into(),
                enabled,
                expires_at,
            }])
            .unwrap(),
        ),
        ConcurrencyLimit::new(1).unwrap(),
        Options::default(),
        resources.clone(),
    )
}

#[tokio::test]
async fn generations_rotation_disable_expiry_and_removal_preserve_subject_history() {
    let upstream = upstream(Reply::Good).await;
    let mut config = subject_configuration(&[&upstream], 2);
    config.subject_limits.get_mut("alice").unwrap().rpd = None;
    let resources = SharedResources::default();
    let first = generation(config.clone(), &resources, "old", true, None).unwrap();
    assert_eq!(
        call(&first, "public", Some("old")).await.status(),
        StatusCode::OK
    );
    let second = generation(config.clone(), &resources, "new", true, None).unwrap();
    assert_eq!(
        call(&second, "public", Some("old")).await.status(),
        StatusCode::UNAUTHORIZED
    );
    assert_eq!(
        call(&second, "public", Some("new")).await.status(),
        StatusCode::OK
    );
    assert_rate(call(&first, "public", Some("old")).await).await;
    let mut changed = config.clone();
    changed.subject_limits.get_mut("alice").unwrap().rpm = Some(3);
    assert!(generation(changed.clone(), &resources, "new", true, None).is_err());
    for (enabled, expires) in [(false, None), (true, Some(0))] {
        let disabled = generation(config.clone(), &resources, "new", enabled, expires).unwrap();
        assert_eq!(
            call(&disabled, "public", Some("new")).await.status(),
            StatusCode::UNAUTHORIZED
        );
    }
    let reenabled = generation(config.clone(), &resources, "new", true, None).unwrap();
    assert_rate(call(&reenabled, "public", Some("new")).await).await;
    drop(first);
    drop(second);
    drop(reenabled);
    let mut removed = config.clone();
    removed.subject_limits.clear();
    drop(generation(removed, &resources, "new", false, None).unwrap());
    assert!(generation(changed, &resources, "new", true, None).is_err());
    let readded = generation(config.clone(), &resources, "new", true, None).unwrap();
    assert_rate(call(&readded, "public", Some("new")).await).await;
    // An explicit new process/resource owner starts empty.
    let restarted = generation(config, &SharedResources::default(), "new", true, None).unwrap();
    assert_eq!(
        call(&restarted, "public", Some("new")).await.status(),
        StatusCode::OK
    );
    assert_eq!(upstream.count(), 3);
}

#[tokio::test]
async fn partially_built_candidate_does_not_pin_unused_policy_or_reset_active_history() {
    let upstream = upstream(Reply::Good).await;
    let mut config = subject_configuration(&[&upstream], 1);
    config.subject_limits.get_mut("alice").unwrap().rpd = None;
    let resources = SharedResources::default();
    let active = generation(config.clone(), &resources, "alice-secret", true, None).unwrap();
    assert_eq!(invoke(&active).await.status(), StatusCode::OK);
    let mut failed = config.clone();
    // BTreeMap binds this fresh identity before reaching the conflicting alice rule.
    failed.subject_limits.insert(
        "aaa-new".into(),
        serde_json::from_value(json!({"rpm":1})).unwrap(),
    );
    failed.subject_limits.get_mut("alice").unwrap().rpm = Some(2);
    assert!(generation(failed.clone(), &resources, "alice-secret", true, None).is_err());
    assert_rate(invoke(&active).await).await;
    failed.subject_limits.get_mut("alice").unwrap().rpm = Some(1);
    failed.subject_limits.get_mut("aaa-new").unwrap().rpm = Some(2);
    let recovered = generation(failed, &resources, "alice-secret", true, None).unwrap();
    assert_rate(invoke(&recovered).await).await;
    assert_eq!(upstream.count(), 1);
}

#[tokio::test]
async fn later_token_quota_rejection_still_counts_as_an_admitted_request() {
    let upstream = upstream(Reply::Good).await;
    let mut config = subject_configuration(&[&upstream], 2);
    config.subject_limits.get_mut("alice").unwrap().rpd = None;
    config.models.get_mut("public").unwrap().quota = Some(nyro_llm::config::QuotaConfig {
        total_tokens: 2,
        reserve_tokens: 2,
    });
    let runtime = runtime(
        config,
        &ConcurrencyLimit::new(1).unwrap(),
        Options::default(),
    );
    assert_eq!(invoke(&runtime).await.status(), StatusCode::OK);
    let quota = invoke(&runtime).await;
    assert_eq!(quota.status(), StatusCode::TOO_MANY_REQUESTS);
    assert!(!quota.headers().contains_key("retry-after"));
    assert_eq!(consume(quota).await["error"]["code"], "quota_exceeded");
    assert_rate(invoke(&runtime).await).await;
    assert_eq!(upstream.count(), 1);
}
