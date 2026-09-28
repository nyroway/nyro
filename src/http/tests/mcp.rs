use super::*;
use nyro_security::ApiKey;
use std::sync::atomic::{AtomicUsize, Ordering};

fn mcp_request(key: &str) -> Request<Body> {
    Request::builder().method("POST").uri("/mcp/tools").header("host","localhost")
        .header("authorization",format!("Bearer {key}")).header("content-type","application/json")
        .header("accept","application/json, text/event-stream").header("mcp-protocol-version","2026-07-28")
        .header("mcp-method","tools/call").header("mcp-name","read")
        .body(Body::from(json!({"jsonrpc":"2.0","id":"root-call","method":"tools/call","params":{"name":"read","_meta":{
            "io.modelcontextprotocol/protocolVersion":"2026-07-28",
            "io.modelcontextprotocol/clientInfo":{"name":"test","version":"1"},
            "io.modelcontextprotocol/clientCapabilities":{}
        }}}).to_string())).unwrap()
}
#[tokio::test]
async fn dual_application_reload_and_shared_concurrency() {
    let calls = Arc::new(AtomicUsize::new(0));
    let started = Arc::new(tokio::sync::Notify::new());
    let release = Arc::new(tokio::sync::Notify::new());
    let counter = calls.clone();
    let notify = started.clone();
    let gate = release.clone();
    let upstream=Router::new().route("/mcp",post(move|axum::Json(value):axum::Json<Value>|{
        let count=counter.clone();let notify=notify.clone();let gate=gate.clone();
        async move {
            let result=match value["method"].as_str().unwrap() {
                "server/discover"=>json!({"resultType":"complete","supportedVersions":["2026-07-28"],"capabilities":{"tools":{}},"ttlMs":0,"cacheScope":"private"}),
                "tools/list"=>json!({"tools":[{"name":"read","inputSchema":{"type":"object"}}]}),
                "tools/call"=>{count.fetch_add(1,Ordering::SeqCst);notify.notify_one();gate.notified().await;json!({"resultType":"complete","content":[{"type":"text","text":"old-generation"}]})},
                _=>panic!(),
            };
            axum::Json(json!({"jsonrpc":"2.0","id":value["id"],"result":result}))
        }
    })).route("/v1/chat/completions",post(||async {axum::Json(json!({"id":"chat","object":"chat.completion","created":1,"model":"old-model","choices":[{"index":0,"message":{"role":"assistant","content":"Hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}))}));
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let url = format!("http://{}", listener.local_addr().unwrap());
    let server = tokio::spawn(async move { axum::serve(listener, upstream).await.unwrap() });
    let mut config = config(&format!("{url}/v1"));
    config.security.api_keys.push(ApiKey {
        id: "client".into(),
        secret: "old-key".into(),
        enabled: true,
        expires_at: None,
    });
    config.mcp=Some(serde_json::from_value(json!({"servers":{"tools":{"transport":"http","url":format!("{url}/mcp"),"subjects":["client"],"allowed_tools":["read"]}}})).unwrap());
    let mut empty = Config::from_yaml("{}").unwrap();
    empty.limit.concurrency = config.limit.concurrency;
    let resources = bootstrap::Resources::new(&empty).unwrap();
    let host = bootstrap::host(&empty, &resources).await.unwrap();
    let tracker = TaskTracker::new();
    let router = router(host.clone(), tracker.clone());
    for missing in [request(), mcp_request("old-key")] {
        let response = router.clone().oneshot(missing).await.unwrap();
        assert_eq!(response.status(), StatusCode::NOT_FOUND);
        to_bytes(response.into_body(), 16384).await.unwrap();
    }
    host.activate(
        resources.candidate(&config).unwrap(),
        Context {
            deadline: Instant::now() + Duration::from_secs(2),
            cancellation: CancellationToken::new(),
        },
    )
    .await
    .unwrap();
    let pending = tokio::spawn(router.clone().oneshot(mcp_request("old-key")));
    tokio::time::timeout(Duration::from_secs(3), started.notified())
        .await
        .unwrap();
    let denied = router.clone().oneshot(request()).await.unwrap();
    assert_eq!(denied.status(), StatusCode::TOO_MANY_REQUESTS);
    drop(denied);
    let mut invalid = config.clone();
    invalid
        .mcp
        .as_mut()
        .unwrap()
        .servers
        .get_mut("tools")
        .unwrap()
        .subjects = vec!["missing".into()];
    assert!(resources.candidate(&invalid).is_err());
    // Remove MCP while its request is running. Both applications publish together;
    // the original request retains the old key, target and permitted tool.
    config.mcp = None;
    host.activate(
        resources.candidate(&config).unwrap(),
        Context {
            deadline: Instant::now() + Duration::from_secs(2),
            cancellation: CancellationToken::new(),
        },
    )
    .await
    .unwrap();
    let removed = router
        .clone()
        .oneshot(mcp_request("old-key"))
        .await
        .unwrap();
    assert_eq!(removed.status(), StatusCode::NOT_FOUND);
    drop(removed);
    release.notify_one();
    let response = pending.await.unwrap().unwrap();
    assert_eq!(response.status(), StatusCode::OK);
    let bytes = to_bytes(response.into_body(), 16384).await.unwrap();
    let value: Value = serde_json::from_slice(&bytes).unwrap();
    assert_eq!(value["result"]["content"][0]["text"], "old-generation");
    let chat = router.oneshot(request()).await.unwrap();
    assert_eq!(chat.status(), StatusCode::OK);
    to_bytes(chat.into_body(), 16384).await.unwrap();
    assert_eq!(calls.load(Ordering::SeqCst), 1);
    host.shutdown().await.unwrap();
    tracker.close();
    tracker.wait().await;
    server.abort();
}
