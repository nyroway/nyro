use super::*;
use crate::bootstrap::Application;
use axum::{
    Json,
    body::{Body, to_bytes},
    http::Request,
    routing::post,
};
use nyro_config::{compile::Snapshot, resources::Resources};
use serde_json::{Value, json};
use tokio_util::sync::CancellationToken;
use tower::ServiceExt;

fn snapshot(url: &str) -> Snapshot {
    let resources: Resources = serde_json::from_value(json!({"version":1,
        "upstreams":[{"id":"pool","kind":"llm","targets":[{"id":"a","protocol":"openai/chat-completions","base_url":url,"model":"a","weight":3},{"id":"b","protocol":"openai/chat-completions","base_url":url,"model":"b","weight":1}]}],
        "models":[{"id":"chat","capability":"chat","upstream":"pool","access":{"mode":"restricted"},"limits":{"token":[{"limit":10,"window":"1m"}]}}],
        "consumers":[{"id":"app","credentials":[{"id":"old","type":"key-auth","secret":"old-key"},{"id":"new","type":"key-auth","secret":"new-key"}],"grants":{"models":["chat"]},"limits":{"llm":{"request":[{"limit":2,"window":"1m"}]}}}]
    })).unwrap();
    Snapshot::file(resources)
}
fn request(model: &str, key: &str) -> Request<Body> {
    Request::builder()
        .method("POST")
        .uri("/v1/chat/completions")
        .header("content-type", "application/json")
        .header("authorization", format!("Bearer {key}"))
        .body(Body::from(
            json!({"model":model,"messages":[{"role":"user","content":"Hello"}]}).to_string(),
        ))
        .unwrap()
}
async fn read(response: Response) -> Value {
    serde_json::from_slice(&to_bytes(response.into_body(), 65536).await.unwrap()).unwrap()
}
async fn upstream() -> (String, tokio::task::JoinHandle<()>) {
    let app = Router::new().route("/v1/chat/completions", post(|Json(v): Json<Value>| async move {
        Json(json!({"id":v["model"],"object":"chat.completion","created":1,"model":v["model"],"choices":[{"index":0,"message":{"role":"assistant","content":"Hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}))
    }));
    let tcp = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let url = format!("http://{}/v1", tcp.local_addr().unwrap());
    (
        url,
        tokio::spawn(async move {
            axum::serve(tcp, app).await.unwrap();
        }),
    )
}
#[tokio::test]
async fn consumer_rotation_model_rename_and_failed_candidate_keep_actual_usage() {
    let (url, task) = upstream().await;
    let snapshot = snapshot(&url);
    let application = Application::new(8).unwrap();
    let host = Arc::new(Host::new(Default::default()));
    let tracker = TaskTracker::new();
    let app = router(host.clone(), tracker.clone());
    assert_eq!(
        app.clone()
            .oneshot(
                Request::builder()
                    .uri("/readyz")
                    .body(Body::empty())
                    .unwrap()
            )
            .await
            .unwrap()
            .status(),
        StatusCode::SERVICE_UNAVAILABLE
    );
    assert!(application.apply(&host, &snapshot).await.is_ok());
    let first = app
        .clone()
        .oneshot(request("chat", "old-key"))
        .await
        .unwrap();
    assert_eq!(first.status(), StatusCode::OK);
    read(first).await;
    let mut invalid = snapshot.clone();
    invalid.resources.models[0].upstream = "missing".into();
    assert!(application.apply(&host, &invalid).await.is_err());
    let renamed = nyro_control::resource::edit(&snapshot, nyro_control::resource::Kind::Models, Some("chat"), Some(json!({"id":"renamed","capability":"chat","upstream":"pool","limits":{"token":[{"limit":10,"window":"1m"}]}}))).unwrap();
    assert!(application.apply(&host, &renamed).await.is_ok());
    let second = app
        .clone()
        .oneshot(request("renamed", "new-key"))
        .await
        .unwrap();
    assert_eq!(second.status(), StatusCode::OK);
    read(second).await;
    assert_eq!(
        app.clone()
            .oneshot(request("renamed", "old-key"))
            .await
            .unwrap()
            .status(),
        StatusCode::TOO_MANY_REQUESTS
    );
    host.shutdown().await.unwrap();
    tracker.close();
    tracker.wait().await;
    task.abort();
}
#[tokio::test]
async fn aliases_share_round_robin_and_anonymous_ignores_invalid_credentials() {
    let (url, task) = upstream().await;
    let mut snapshot = snapshot(&url);
    snapshot.resources.models[0].access.mode = nyro_config::resources::AccessMode::Anonymous;
    snapshot.resources.models[0].limits = Default::default();
    let mut alias = snapshot.resources.models[0].clone();
    alias.id = "alias".into();
    snapshot.resources.models.push(alias);
    snapshot
        .identities
        .models
        .insert("alias".into(), "models:alias".into());
    let application = Application::new(8).unwrap();
    let host = Arc::new(Host::new(Default::default()));
    assert!(application.apply(&host, &snapshot).await.is_ok());
    let tracker = TaskTracker::new();
    let app = router(host.clone(), tracker.clone());
    let mut ids = vec![];
    for model in ["chat", "alias", "chat", "alias"] {
        let response = app
            .clone()
            .oneshot(request(model, "wrong-key"))
            .await
            .unwrap();
        assert_eq!(response.status(), StatusCode::OK);
        ids.push(read(response).await["id"].as_str().unwrap().to_owned());
    }
    assert_eq!(ids.iter().filter(|v| *v == "a").count(), 3);
    assert_eq!(ids.iter().filter(|v| *v == "b").count(), 1);
    host.shutdown().await.unwrap();
    tracker.close();
    tracker.wait().await;
    task.abort();
}
#[tokio::test]
async fn memory_updates_activate_and_ack_only_after_host_is_ready() {
    let host = Arc::new(Host::new(Default::default()));
    let application = Arc::new(Application::new(2).unwrap());
    let hub = nyro_sync::Hub::new(Snapshot::file(Resources::default())).unwrap();
    let stop = CancellationToken::new();
    let source = hub.clone();
    let apply_host = host.clone();
    let cancel = stop.clone();
    let worker = tokio::spawn(async move {
        nyro_sync::run(&source, "embedded", cancel, |snapshot| {
            let app = application.clone();
            let host = apply_host.clone();
            async move { app.apply(&host, &snapshot.config).await }
        })
        .await
    });
    tokio::time::timeout(Duration::from_secs(3), async {
        loop {
            if hub.node("embedded").is_some_and(|s| s.applied.is_some()) {
                break;
            }
            tokio::task::yield_now().await;
        }
    })
    .await
    .unwrap();
    assert!(host.status().accepting);
    stop.cancel();
    worker.await.unwrap().unwrap();
    host.shutdown().await.unwrap();
}

#[tokio::test]
async fn mcp_pool_query_credentials_and_resource_request_limits_are_independent_of_llm() {
    let calls = Arc::new(std::sync::Mutex::new(Vec::new()));
    let recorded = calls.clone();
    let upstream = Router::new().route("/mcp",post(move |request: Request<Body>| {
        let calls = recorded.clone();
        async move {
            assert!(!request.headers().contains_key("authorization"));
            let query = request.uri().query().unwrap().to_owned();
            let value: Value = serde_json::from_slice(&to_bytes(request.into_body(),65536).await.unwrap()).unwrap();
            let result = match value["method"].as_str().unwrap() {
                "server/discover" => json!({"resultType":"complete","supportedVersions":["2026-07-28"],"capabilities":{"tools":{}},"ttlMs":0,"cacheScope":"private"}),
                "tools/list" => { calls.lock().unwrap().push(query); json!({"tools":[{"name":"read","inputSchema":{"type":"object"}}]}) },
                method => panic!("unexpected method {method}"),
            };
            Json(json!({"jsonrpc":"2.0","id":value["id"],"result":result}))
        }
    }));
    let tcp = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let url = format!("http://{}/mcp", tcp.local_addr().unwrap());
    let task = tokio::spawn(async move {
        axum::serve(tcp, upstream).await.unwrap();
    });
    let resources: Resources = serde_json::from_value(json!({"version":1,"upstreams":[{"id":"mcp-pool","kind":"mcp","targets":[
        {"id":"a","url":url,"transport":"streamable-http","weight":3,"auth":{"type":"key-auth","in":"query","name":"key","secret":"a+ /?"}},
        {"id":"b","url":url,"transport":"streamable-http","weight":1,"auth":{"type":"key-auth","in":"query","name":"key","secret":"b"}}
    ]}],"mcps":[{"id":"tools","upstream":"mcp-pool","allowed_tools":["read"],"access":{"mode":"anonymous"},"limits":{"request":[{"limit":4,"window":"1m"}]}}]})).unwrap();
    let application = Application::new(4).unwrap();
    let host = Arc::new(Host::new(Default::default()));
    assert!(
        application
            .apply(&host, &Snapshot::file(resources))
            .await
            .is_ok()
    );
    let tracker = TaskTracker::new();
    let app = router(host.clone(), tracker.clone());
    for index in 0..5 {
        let request = Request::builder()
            .method("POST")
            .uri("/mcp/tools")
            .header("host", "localhost")
            .header("authorization", "Bearer wrong-key")
            .header("content-type", "application/json")
            .header("accept", "application/json, text/event-stream")
            .header("mcp-protocol-version", "2026-07-28")
            .header("mcp-method", "tools/list")
            .body(Body::from(
                json!({"jsonrpc":"2.0","id":index,"method":"tools/list","params":{"_meta":{
                    "io.modelcontextprotocol/protocolVersion":"2026-07-28",
                    "io.modelcontextprotocol/clientInfo":{"name":"test","version":"1"},
                    "io.modelcontextprotocol/clientCapabilities":{}
                }}})
                .to_string(),
            ))
            .unwrap();
        let response = app.clone().oneshot(request).await.unwrap();
        assert_eq!(
            response.status(),
            if index == 4 {
                StatusCode::TOO_MANY_REQUESTS
            } else {
                StatusCode::OK
            }
        );
        read(response).await;
    }
    let calls = calls.lock().unwrap().clone();
    assert_eq!(
        calls
            .iter()
            .filter(|q| q.as_str() == "key=a%2B+%2F%3F")
            .count(),
        3
    );
    assert_eq!(calls.iter().filter(|q| q.as_str() == "key=b").count(), 1);
    host.shutdown().await.unwrap();
    tracker.close();
    tracker.wait().await;
    task.abort();
}
