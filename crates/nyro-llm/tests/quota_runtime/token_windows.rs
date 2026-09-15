use super::*;

fn request(format: &str, streaming: bool) -> Request<Body> {
    let mut input = super::request(format, streaming);
    input
        .headers_mut()
        .insert("authorization", "Bearer alice-secret".parse().unwrap());
    input
}
async fn invoke(runtime: &Runtime) -> Response<Body> {
    runtime
        .handle(request("openai", false), CancellationToken::new())
        .await
}

fn with_windows(mut config: Config, tpm: u64, tpd: u64, reserve: u64) -> Config {
    config.subject_limits =
        serde_json::from_value(json!({"alice":{"tpm":tpm,"tpd":tpd,"reserve_tokens":reserve}}))
            .unwrap();
    config
}
fn window_balance(resources: &SharedResources, used: u128, reserved: u64) {
    let snapshots = resources.subject_limits.token_snapshots("alice").unwrap();
    assert_eq!(snapshots.len(), 2);
    for snapshot in snapshots {
        assert_eq!((snapshot.used, snapshot.reserved), (used, reserved));
    }
}
async fn assert_window(response: Response<Body>, pending: bool) {
    assert_eq!(response.status(), StatusCode::TOO_MANY_REQUESTS);
    if pending {
        assert!(!response.headers().contains_key("retry-after"));
    } else {
        assert!(
            response.headers()["retry-after"]
                .to_str()
                .unwrap()
                .parse::<u64>()
                .unwrap()
                > 0
        );
    }
    assert_eq!(
        consume(response).await["error"]["code"],
        "rate_limit_exceeded"
    );
}

#[tokio::test]
async fn strict_and_matching_native_json_sse_settle_both_budgets_once() {
    for format in ["openai", "anthropic", "gemini", "responses"] {
        for native_mode in [false, true] {
            let upstream = upstream(Reply::Json(native(format))).await;
            let resources = SharedResources::default();
            let mut config =
                with_windows(configuration(&[&upstream], format, 100, 7), 100, 1000, 10);
            config.providers.get_mut("p0").unwrap().native_chat = native_mode;
            let runtime = runtime(
                config,
                &ConcurrencyLimit::new(1).unwrap(),
                Options::default(),
                &resources,
            );
            let response = runtime
                .handle(request(format, false), CancellationToken::new())
                .await;
            assert_eq!(response.status(), StatusCode::OK, "{format}/{native_mode}");
            window_balance(&resources, 5, 0);
            balance(&resources, 5, 0);
            drop(response);
            *upstream.reply.lock().unwrap() = Reply::Sse(native_frames(format), false);
            let response = runtime
                .handle(request(format, true), CancellationToken::new())
                .await;
            assert_eq!(response.status(), StatusCode::OK);
            to_bytes(response.into_body(), 65536).await.unwrap();
            window_balance(&resources, 10, 0);
            balance(&resources, 10, 0);
            assert_eq!(upstream.count(), 2);
        }
    }
}

#[tokio::test]
async fn subject_scope_crosses_aliases_and_day_window_is_independent_of_model_budgets() {
    let upstream = upstream(Reply::Json(native("openai"))).await;
    let mut config = with_windows(configuration(&[&upstream], "openai", 100, 5), 100, 10, 5);
    config
        .models
        .insert("alias".into(), config.models["public"].clone());
    let resources = SharedResources::default();
    let runtime = runtime(
        config,
        &ConcurrencyLimit::new(1).unwrap(),
        Options::default(),
        &resources,
    );
    for model in ["public", "alias"] {
        let mut input = request("openai", false);
        *input.body_mut() = Body::from(
            json!({"model":model,"messages":[{"role":"user","content":"Hi"}]}).to_string(),
        );
        assert_eq!(
            runtime
                .handle(input, CancellationToken::new())
                .await
                .status(),
            StatusCode::OK
        );
    }
    window_balance(&resources, 10, 0);
    let denied = invoke(&runtime).await;
    assert!(
        denied.headers()["retry-after"]
            .to_str()
            .unwrap()
            .parse::<u64>()
            .unwrap()
            > 86340
    );
    assert_window(denied, false).await;
    balance(&resources, 5, 0);
    for secret in [Some("bob-secret"), None] {
        let mut input = request("openai", false);
        if let Some(secret) = secret {
            input
                .headers_mut()
                .insert("authorization", format!("Bearer {secret}").parse().unwrap());
        } else {
            input.headers_mut().remove("authorization");
        }
        assert_eq!(
            runtime
                .handle(input, CancellationToken::new())
                .await
                .status(),
            StatusCode::OK
        );
    }
    window_balance(&resources, 10, 0);
    assert_eq!(upstream.count(), 4);
}

#[tokio::test]
async fn pending_windows_and_model_quota_denials_leave_other_token_reservations_unchanged() {
    for (model_total, window_total) in [(100, 20), (14, 100)] {
        let upstream = upstream(Reply::Sse(chat_frames(&[], false), true)).await;
        let resources = SharedResources::default();
        let limit = ConcurrencyLimit::new(3).unwrap();
        let runtime = runtime(
            with_windows(
                configuration(&[&upstream], "openai", model_total, 7),
                window_total,
                1000,
                10,
            ),
            &limit,
            Options::default(),
            &resources,
        );
        let (a, b) = tokio::join!(
            runtime.handle(request("openai", true), CancellationToken::new()),
            runtime.handle(request("openai", true), CancellationToken::new())
        );
        assert_eq!(a.status(), StatusCode::OK);
        assert_eq!(b.status(), StatusCode::OK);
        window_balance(&resources, 0, 20);
        balance(&resources, 0, 14);
        let denied = invoke(&runtime).await;
        assert_eq!(limit.available(), 1);
        if window_total == 20 {
            assert_window(denied, true).await;
        } else {
            assert_quota(denied).await;
        }
        window_balance(&resources, 0, 20);
        balance(&resources, 0, 14);
        drop(a);
        drop(b);
        window_balance(&resources, 20, 0);
        balance(&resources, 14, 0);
        assert_eq!(upstream.count(), 2);
    }
}

#[tokio::test]
async fn json_zero_invalid_missing_and_overage_use_independent_reservation_fallbacks() {
    for (usage, status, model_used, window_used) in [
        (
            json!({"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}),
            200,
            0,
            0,
        ),
        (Value::Null, 200, 7, 10),
        (
            json!({"prompt_tokens":3,"completion_tokens":2,"total_tokens":4}),
            502,
            7,
            10,
        ),
        (
            json!({"prompt_tokens":11,"completion_tokens":12,"total_tokens":23}),
            200,
            23,
            23,
        ),
    ] {
        let mut value = native("openai");
        if usage.is_null() {
            value.as_object_mut().unwrap().remove("usage");
        } else {
            value["usage"] = usage;
        }
        let upstream = upstream(Reply::Json(value)).await;
        let resources = SharedResources::default();
        let runtime = runtime(
            with_windows(configuration(&[&upstream], "openai", 20, 7), 20, 100, 10),
            &ConcurrencyLimit::new(1).unwrap(),
            Options::default(),
            &resources,
        );
        let response = invoke(&runtime).await;
        assert_eq!(response.status().as_u16(), status);
        window_balance(&resources, window_used, 0);
        balance(&resources, model_used, 0);
        drop(response);
        if window_used > 20 {
            assert_window(invoke(&runtime).await, false).await;
            assert_eq!(upstream.count(), 1);
        }
    }
}

#[tokio::test]
async fn retries_reserve_tokens_per_attempt_and_known_connect_errors_release_both() {
    for (total, connect) in [(10, false), (20, false), (10, true)] {
        let first = upstream(Reply::Status(503)).await;
        let backup = upstream(Reply::Json(native("openai"))).await;
        let mut config = with_windows(
            configuration(&[&first, &backup], "openai", 100, 7),
            total,
            100,
            10,
        );
        if connect {
            let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
            config.providers.get_mut("p0").unwrap().base_url =
                format!("http://{}/v1", listener.local_addr().unwrap());
            drop(listener);
        }
        let resources = SharedResources::default();
        let runtime = runtime(
            config,
            &ConcurrencyLimit::new(1).unwrap(),
            Options::default(),
            &resources,
        );
        let response = invoke(&runtime).await;
        if total == 10 && !connect {
            assert_window(response, false).await;
            window_balance(&resources, 10, 0);
            balance(&resources, 7, 0);
            assert_eq!(backup.count(), 0);
        } else {
            assert_eq!(response.status(), StatusCode::OK);
            drop(response);
            window_balance(&resources, if connect { 5 } else { 15 }, 0);
            balance(&resources, if connect { 5 } else { 12 }, 0);
            assert_eq!(backup.count(), 1);
        }
    }
}

#[tokio::test]
async fn generations_and_removed_subjects_retain_settled_and_pending_token_state() {
    let upstream = upstream(Reply::Sse(chat_frames(&[], false), true)).await;
    let config = with_windows(configuration(&[&upstream], "openai", 100, 7), 10, 100, 10);
    let resources = SharedResources::default();
    let limit = ConcurrencyLimit::new(2).unwrap();
    let old = runtime(config.clone(), &limit, Options::default(), &resources);
    let response = old
        .handle(request("openai", true), CancellationToken::new())
        .await;
    assert_eq!(response.status(), StatusCode::OK);
    window_balance(&resources, 0, 10);
    let new = runtime(config.clone(), &limit, Options::default(), &resources);
    assert_window(invoke(&new).await, true).await;
    drop(old);
    drop(new);
    let mut removed = config.clone();
    removed.subject_limits.clear();
    drop(runtime(removed, &limit, Options::default(), &resources));
    let readded = runtime(config, &limit, Options::default(), &resources);
    assert_window(invoke(&readded).await, true).await;
    drop(response);
    window_balance(&resources, 10, 0);
    assert_window(invoke(&readded).await, false).await;
    assert_eq!(upstream.count(), 1);
}

#[tokio::test]
async fn token_only_embedding_charges_input_and_rejects_without_a_model_quota() {
    let upstream=upstream(Reply::Json(json!({"object":"list","model":"private","data":[{"object":"embedding","index":0,"embedding":[0.1]}],"usage":{"prompt_tokens":3,"total_tokens":3}}))).await;
    let mut config = with_windows(configuration(&[&upstream], "openai", 100, 7), 5, 100, 2);
    config.models.get_mut("public").unwrap().quota = None;
    let resources = SharedResources::default();
    let runtime = runtime(
        config,
        &ConcurrencyLimit::new(1).unwrap(),
        Options::default(),
        &resources,
    );
    for _ in 0..2 {
        let response = runtime
            .handle(request("embedding", false), CancellationToken::new())
            .await;
        assert_eq!(response.status(), StatusCode::OK);
        drop(response);
    }
    window_balance(&resources, 6, 0);
    assert!(resources.quotas.snapshot("public").is_none());
    assert_window(
        runtime
            .handle(request("embedding", false), CancellationToken::new())
            .await,
        false,
    )
    .await;
    assert_eq!(upstream.count(), 2);
}

#[tokio::test]
async fn stream_completion_and_interruption_settle_independent_token_fallbacks() {
    for (ending, observed) in [
        ("complete", 0),
        ("complete", 23),
        ("truncate", 3),
        ("drop", 3),
        ("cancel", 23),
        ("timeout", 3),
        ("decrease", 23),
    ] {
        let sequence = if ending == "decrease" {
            vec![observed, 3]
        } else {
            vec![observed]
        };
        let hanging = matches!(ending, "drop" | "cancel" | "timeout");
        let upstream = upstream(Reply::Sse(
            chat_frames(&sequence, ending == "complete"),
            hanging,
        ))
        .await;
        let resources = SharedResources::default();
        let limit = ConcurrencyLimit::new(1).unwrap();
        let runtime = runtime(
            with_windows(configuration(&[&upstream], "openai", 100, 7), 100, 1000, 10),
            &limit,
            Options {
                request_timeout: Duration::from_millis(if ending == "timeout" {
                    500
                } else {
                    5000
                }),
                ..Options::default()
            },
            &resources,
        );
        let token = CancellationToken::new();
        let mut input = request("openai", true);
        *input.body_mut()=Body::from(json!({"model":"public","messages":[{"role":"user","content":"Hi"}],"stream":true,"stream_options":{"include_usage":true}}).to_string());
        let response = runtime.handle(input, token.clone()).await;
        assert_eq!(response.status(), StatusCode::OK);
        let mut stream = response.into_body().into_data_stream();
        let mut text = String::new();
        while !text.contains("\"total_tokens\"") {
            text.push_str(&String::from_utf8_lossy(
                &stream.next().await.unwrap().unwrap(),
            ));
        }
        if ending == "drop" {
            drop(stream);
        } else {
            if ending == "cancel" {
                token.cancel();
            }
            let mut failed = false;
            while let Some(chunk) = stream.next().await {
                match chunk {
                    Ok(bytes) => text.push_str(&String::from_utf8_lossy(&bytes)),
                    Err(_) => {
                        failed = true;
                        break;
                    }
                }
            }
            assert_eq!(failed, ending != "complete", "{ending}");
            if ending == "complete" {
                assert!(text.contains("[DONE]"));
            }
            drop(stream);
        }
        window_balance(
            &resources,
            u128::from(if ending == "complete" {
                observed
            } else {
                observed.max(10)
            }),
            0,
        );
        balance(
            &resources,
            u128::from(if ending == "complete" {
                observed
            } else {
                observed.max(7)
            }),
            0,
        );
        assert_eq!(limit.available(), 1);
        assert_eq!(upstream.count(), 1);
    }
}

#[tokio::test]
async fn pre_dispatch_rejection_does_not_reserve_and_post_dispatch_future_drop_charges() {
    let upstream = upstream(Reply::Slow).await;
    let resources = SharedResources::default();
    let limit = ConcurrencyLimit::new(1).unwrap();
    let mut config = with_windows(configuration(&[&upstream], "openai", 100, 7), 10, 100, 10);
    config.models.get_mut("public").unwrap().allow_anonymous = false;
    let runtime = runtime(config, &limit, Options::default(), &resources);
    let mut invalid = request("openai", false);
    invalid
        .headers_mut()
        .insert("authorization", "Bearer wrong-secret".parse().unwrap());
    assert_eq!(
        runtime
            .handle(invalid, CancellationToken::new())
            .await
            .status(),
        StatusCode::UNAUTHORIZED
    );
    let cancelled = CancellationToken::new();
    cancelled.cancel();
    drop(runtime.handle(request("openai", false), cancelled).await);
    window_balance(&resources, 0, 0);
    balance(&resources, 0, 0);
    assert_eq!(upstream.count(), 0);
    let mut pending = Box::pin(runtime.handle(request("openai", false), CancellationToken::new()));
    tokio::select! {_ = &mut pending=>panic!("upstream should wait"), _=upstream.wait_for_call()=>{}}
    window_balance(&resources, 0, 10);
    balance(&resources, 0, 7);
    drop(pending);
    window_balance(&resources, 10, 0);
    balance(&resources, 7, 0);
    assert_eq!(limit.available(), 1);
    assert_window(invoke(&runtime).await, false).await;
}

#[tokio::test]
async fn token_rejection_keeps_its_logical_request_charge_in_combined_subject_policy() {
    let upstream = upstream(Reply::Sse(chat_frames(&[], false), true)).await;
    let mut config = with_windows(configuration(&[&upstream], "openai", 100, 7), 10, 100, 10);
    let policy = config.subject_limits.get_mut("alice").unwrap();
    policy.rpm = Some(2);
    policy.rpd = Some(2);
    let resources = SharedResources::default();
    let runtime = runtime(
        config,
        &ConcurrencyLimit::new(2).unwrap(),
        Options::default(),
        &resources,
    );
    let stream = runtime
        .handle(request("openai", true), CancellationToken::new())
        .await;
    assert_eq!(stream.status(), StatusCode::OK);
    // Second logical admission exhausts RPM/RPD, then pending tokens reject its attempt.
    assert_window(invoke(&runtime).await, true).await;
    window_balance(&resources, 0, 10);
    balance(&resources, 0, 7);
    // Request windows now reject first, providing a day wait instead of unknown pending time.
    let denied = invoke(&runtime).await;
    assert!(
        denied.headers()["retry-after"]
            .to_str()
            .unwrap()
            .parse::<u64>()
            .unwrap()
            > 86340
    );
    assert_window(denied, false).await;
    assert_eq!(upstream.count(), 1);
    drop(stream);
}
