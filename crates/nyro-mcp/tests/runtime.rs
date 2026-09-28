use axum::{
    body::{Body, to_bytes},
    http::{Request, StatusCode},
};
use nyro_limit::ConcurrencyLimit;
use nyro_mcp::{Runtime, config::Config};
use nyro_security::{ApiKey, ApiKeys};
use serde_json::{Value, json};
use std::sync::Arc;
use tokio_util::sync::CancellationToken;

fn config(url: &str) -> Config {
    serde_json::from_value(json!({"servers":{"knowledge":{
        "transport":"http","url":url,"bearer_token":"upstream-secret",
        "subjects":["client-a"],"allowed_tools":["search_documents"]
    }}}))
    .unwrap()
}
fn runtime(config: Config) -> Runtime {
    let keys = ApiKeys::new(vec![ApiKey {
        id: "client-a".into(),
        secret: "client-secret".into(),
        enabled: true,
        expires_at: None,
    }])
    .unwrap();
    Runtime::new(config, Arc::new(keys), ConcurrencyLimit::new(1).unwrap()).unwrap()
}
fn request(method: &str, name: Option<&str>, key: &str) -> Request<Body> {
    let mut params = json!({"_meta":{
        "io.modelcontextprotocol/protocolVersion":"2026-07-28",
        "io.modelcontextprotocol/clientInfo":{"name":"nyro-test","version":"1"},
        "io.modelcontextprotocol/clientCapabilities":{}
    }});
    if let Some(name) = name {
        params["name"] = json!(name);
        params["arguments"] = json!({"query":"Hello"});
    }
    let mut builder = Request::builder()
        .method("POST")
        .uri("/mcp/knowledge")
        .header("authorization", format!("Bearer {key}"))
        .header("host", "127.0.0.1")
        .header("content-type", "application/json")
        .header("accept", "application/json, text/event-stream")
        .header("mcp-protocol-version", "2026-07-28")
        .header("mcp-method", method);
    if let Some(name) = name {
        builder = builder.header("mcp-name", name);
    }
    builder
        .body(Body::from(
            json!({"jsonrpc":"2.0","id":7,"method":method,"params":params}).to_string(),
        ))
        .unwrap()
}
async fn response(runtime: &Runtime, request: Request<Body>) -> (StatusCode, Value) {
    let response = runtime.handle(request, CancellationToken::new()).await;
    let status = response.status();
    let bytes = to_bytes(response.into_body(), 1 << 20).await.unwrap();
    (
        status,
        serde_json::from_slice(&bytes)
            .unwrap_or_else(|e| panic!("{status}: {}: {e}", String::from_utf8_lossy(&bytes))),
    )
}
#[tokio::test]
async fn credentials_and_tool_rules_are_checked_before_network() {
    let runtime = runtime(config("http://127.0.0.1:1/mcp"));
    assert_eq!(
        response(
            &runtime,
            request("tools/call", Some("search_documents"), "wrong")
        )
        .await
        .0,
        StatusCode::UNAUTHORIZED
    );
    assert_eq!(
        response(
            &runtime,
            request("tools/call", Some("forbidden"), "client-secret")
        )
        .await
        .0,
        StatusCode::FORBIDDEN
    );
}
#[tokio::test]
async fn discovery_only_advertises_tools() {
    let runtime = runtime(config("http://127.0.0.1:1/mcp"));
    let (status, body) =
        response(&runtime, request("server/discover", None, "client-secret")).await;
    assert_eq!(status, StatusCode::OK, "{body}");
    assert_eq!(body["result"]["supportedVersions"], json!(["2026-07-28"]));
    assert_eq!(body["result"]["capabilities"], json!({"tools":{}}));
    assert_eq!(body["id"], 7);
}
#[tokio::test]
async fn metadata_mismatch_never_calls_forbidden_tool() {
    let runtime = runtime(config("http://127.0.0.1:1/mcp"));
    let mut req = request("tools/call", Some("forbidden"), "client-secret");
    req.headers_mut()
        .insert("mcp-name", "search_documents".parse().unwrap());
    assert_eq!(response(&runtime, req).await.0, StatusCode::BAD_REQUEST);
}

use axum::{Json, Router, http::HeaderMap, routing::post};
use std::sync::{
    Mutex,
    atomic::{AtomicUsize, Ordering},
};
struct Upstream {
    url: String,
    calls: Arc<AtomicUsize>,
    seen: Arc<Mutex<Vec<(HeaderMap, Value)>>>,
    task: tokio::task::JoinHandle<()>,
}
impl Drop for Upstream {
    fn drop(&mut self) {
        self.task.abort();
    }
}
async fn upstream(mode: &'static str) -> Upstream {
    let calls = Arc::new(AtomicUsize::new(0));
    let seen = Arc::new(Mutex::new(Vec::new()));
    let count = calls.clone();
    let capture = seen.clone();
    let router=Router::new().route("/mcp",post(move |headers:HeaderMap,Json(value):Json<Value>|{
        let calls=count.clone(); let seen=capture.clone();
        async move {
            seen.lock().unwrap().push((headers,value.clone()));
            let result=match value["method"].as_str().unwrap() {
                "server/discover"=>json!({"resultType":"complete","supportedVersions":["2026-07-28"],"capabilities":{"tools":{}},"ttlMs":0,"cacheScope":"private"}),
                "tools/list"=>{
                    if mode=="annotated" {
                        json!({"tools":[{"name":"search_documents","inputSchema":{"type":"object","properties":{"query":{"type":"string","x-mcp-header":"query"}}}}]})
                    } else if value["params"]["cursor"]=="page2" {
                        json!({"tools":[{"name":"search_documents","inputSchema":{"type":"object"}}]})
                    } else {
                        json!({"tools":[{"name":"hidden","inputSchema":{"type":"object"}}],"nextCursor":"page2","ttlMs":60000,"cacheScope":"public"})
                    }
                },
                "tools/call"=>{calls.fetch_add(1,Ordering::SeqCst);json!({"resultType":"complete","content":[{"type":"text","text":"tool-result"}],"structuredContent":[1,2],"isError":true})},
                other=>panic!("unexpected method {other}"),
            };
            let body=json!({"jsonrpc":"2.0","id":value["id"],"result":result}).to_string();
            if value["method"]=="tools/call" {
                if mode=="protocol-error" {
                    return axum::http::Response::builder().header("content-type","application/json")
                        .body(Body::from(json!({"jsonrpc":"2.0","id":value["id"],"error":{"code":-32602,"message":"sensitive-upstream-error","data":{"secret":"sensitive-error-data"}}}).to_string())).unwrap();
                }

                if mode=="slow" {tokio::time::sleep(std::time::Duration::from_secs(5)).await;}
                if mode=="disconnect" {
                    return axum::http::Response::builder().header("content-type","text/event-stream")
                        .body(Body::from_stream(futures::stream::iter([Err::<axum::body::Bytes,_>(std::io::Error::other("fixture disconnect"))]))).unwrap();
                }
                if mode=="large" {
                    return axum::http::Response::builder().header("content-type","text/event-stream")
                        .body(Body::from(format!("data: {}\n\n", "x".repeat(2048)))).unwrap();
                }
            }

            if mode=="sse" && value["method"]=="tools/call" {
                let bytes=format!("event: message\ndata: {body}\n\n").into_bytes();
                let chunks=bytes.chunks(3).map(|c|Ok::<_,std::io::Error>(axum::body::Bytes::copy_from_slice(c))).collect::<Vec<_>>();
                axum::http::Response::builder().header("content-type","text/event-stream").body(Body::from_stream(futures::stream::iter(chunks))).unwrap()
            } else {axum::http::Response::builder().header("content-type","application/json").body(Body::from(body)).unwrap()}
        }
    }));
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let url = format!("http://{}/mcp", listener.local_addr().unwrap());
    let task = tokio::spawn(async move {
        axum::serve(listener, router).await.unwrap();
    });
    Upstream {
        url,
        calls,
        seen,
        task,
    }
}
#[tokio::test]
async fn filtered_empty_page_preserves_cursor() {
    let upstream = upstream("json").await;
    let runtime = runtime(config(&upstream.url));
    let (status, body) = response(&runtime, request("tools/list", None, "client-secret")).await;
    assert_eq!(status, StatusCode::OK, "{body}");
    assert_eq!(body["result"]["tools"], json!([]));
    assert_eq!(body["result"]["nextCursor"], "page2");
    assert_eq!(body["result"]["cacheScope"], "private");
    assert_eq!(upstream.seen.lock().unwrap().len(), 2);
}
#[tokio::test]
async fn json_and_split_sse_preserve_tool_results_and_isolate_credentials() {
    for mode in ["json", "sse"] {
        let upstream = upstream(mode).await;
        let runtime = runtime(config(&upstream.url));
        let (status, body) = response(
            &runtime,
            request("tools/call", Some("search_documents"), "client-secret"),
        )
        .await;
        assert_eq!(status, StatusCode::OK, "{body}");
        assert_eq!(body["id"], 7);
        assert_eq!(body["result"]["isError"], true, "{body}");
        assert_eq!(body["result"]["structuredContent"], json!([1, 2]));
        assert_eq!(upstream.calls.load(Ordering::SeqCst), 1);
        for (headers, value) in upstream.seen.lock().unwrap().iter() {
            assert_eq!(headers["authorization"], "Bearer upstream-secret");
            assert_eq!(headers["mcp-protocol-version"], "2026-07-28");
            assert_eq!(
                headers["mcp-method"].to_str().unwrap(),
                value["method"].as_str().unwrap()
            );
            assert_eq!(
                value["params"]["_meta"]["io.modelcontextprotocol/clientCapabilities"],
                json!({})
            );
        }
    }
}
#[tokio::test]
async fn unknown_revision_initialize_and_invalid_http_are_rejected() {
    let upstream = upstream("json").await;
    let runtime = runtime(config(&upstream.url));
    for version in ["2025-11-25", "unknown"] {
        let mut req = request("tools/call", Some("search_documents"), "client-secret");
        req.headers_mut()
            .insert("mcp-protocol-version", version.parse().unwrap());
        assert_eq!(response(&runtime, req).await.0, StatusCode::BAD_REQUEST);
    }
    assert_eq!(
        response(&runtime, request("initialize", None, "client-secret"))
            .await
            .0,
        StatusCode::NOT_FOUND
    );
    for path in [
        "/mcp/know%6cedge",
        "/mcp/knowledge/extra",
        "/mcp/knowledge?key=x",
    ] {
        let mut req = request("server/discover", None, "client-secret");
        *req.uri_mut() = path.parse().unwrap();
        assert!(response(&runtime, req).await.0.is_client_error());
    }
    for name in ["authorization", "mcp-method"] {
        let mut req = request("server/discover", None, "client-secret");
        req.headers_mut().append(name, "duplicate".parse().unwrap());
        assert!(response(&runtime, req).await.0.is_client_error());
    }
    let mut req = request("server/discover", None, "client-secret");
    req.headers_mut()
        .insert("origin", "https://example.test".parse().unwrap());
    assert_eq!(response(&runtime, req).await.0, StatusCode::FORBIDDEN);
    assert!(upstream.seen.lock().unwrap().is_empty());
}
fn limited_runtime(config: Config, limit: ConcurrencyLimit) -> Runtime {
    Runtime::new(
        config,
        Arc::new(
            ApiKeys::new(vec![ApiKey {
                id: "client-a".into(),
                secret: "client-secret".into(),
                enabled: true,
                expires_at: None,
            }])
            .unwrap(),
        ),
        limit,
    )
    .unwrap()
}
#[tokio::test]
async fn disconnect_after_side_effect_is_not_retried() {
    let upstream = upstream("disconnect").await;
    let mut config = config(&upstream.url);
    config.request_timeout_ms = 250;
    let limit = ConcurrencyLimit::new(1).unwrap();
    let runtime = limited_runtime(config, limit.clone());
    let (_, body) = response(
        &runtime,
        request("tools/call", Some("search_documents"), "client-secret"),
    )
    .await;
    assert!(body.get("error").is_some(), "{body}");
    assert_eq!(upstream.calls.load(Ordering::SeqCst), 1);
    assert_eq!(limit.available(), 1);
}
#[tokio::test]
async fn oversized_frame_or_response_stops_reading() {
    for frame in [true, false] {
        let upstream = upstream("large").await;
        let mut config = config(&upstream.url);
        config.request_timeout_ms = 250;
        if frame {
            config.max_frame_bytes = 1024;
        } else {
            config.max_response_bytes = 1500;
        }
        let limit = ConcurrencyLimit::new(1).unwrap();
        let runtime = limited_runtime(config, limit.clone());
        let (_, body) = response(
            &runtime,
            request("tools/call", Some("search_documents"), "client-secret"),
        )
        .await;
        assert!(body.get("error").is_some(), "{body}");
        assert_eq!(limit.available(), 1);
        assert_eq!(upstream.calls.load(Ordering::SeqCst), 1);
    }
}
#[tokio::test]
async fn drop_unpolled_body_and_deadline_release_permit() {
    let upstream = upstream("json").await;
    let mut config = config(&upstream.url);
    config.request_timeout_ms = 150;
    let limit = ConcurrencyLimit::new(1).unwrap();
    let runtime = limited_runtime(config, limit.clone());
    let body = runtime
        .handle(
            request("tools/call", Some("search_documents"), "client-secret"),
            CancellationToken::new(),
        )
        .await;
    assert_eq!(limit.available(), 0);
    drop(body);
    assert_eq!(limit.available(), 1);
    let body = runtime
        .handle(
            request("tools/call", Some("search_documents"), "client-secret"),
            CancellationToken::new(),
        )
        .await;
    tokio::time::sleep(std::time::Duration::from_millis(200)).await;
    assert_eq!(limit.available(), 1);
    drop(body);
}
#[tokio::test]
async fn deadline_includes_slow_upload_and_upstream() {
    let upstream = upstream("slow").await;
    let mut config = config(&upstream.url);
    config.request_timeout_ms = 100;
    let limit = ConcurrencyLimit::new(1).unwrap();
    let runtime = limited_runtime(config, limit.clone());
    let req = request("tools/call", Some("search_documents"), "client-secret");
    let (parts, _) = req.into_parts();
    let slow = Body::from_stream(futures::stream::pending::<
        Result<axum::body::Bytes, std::io::Error>,
    >());
    assert_eq!(
        response(&runtime, Request::from_parts(parts, slow)).await.0,
        StatusCode::GATEWAY_TIMEOUT
    );
    assert_eq!(upstream.calls.load(Ordering::SeqCst), 0);
    let (_, body) = response(
        &runtime,
        request("tools/call", Some("search_documents"), "client-secret"),
    )
    .await;
    assert!(body.get("error").is_some());
    assert_eq!(upstream.calls.load(Ordering::SeqCst), 1);
    tokio::time::sleep(std::time::Duration::from_millis(50)).await;
    assert_eq!(limit.available(), 1);
}

#[tokio::test]
async fn protocol_errors_keep_the_code_without_exposing_upstream_details() {
    let upstream = upstream("protocol-error").await;
    let runtime = runtime(config(&upstream.url));
    let (_, body) = response(
        &runtime,
        request("tools/call", Some("search_documents"), "client-secret"),
    )
    .await;
    assert_eq!(body["error"]["code"], -32602, "{body}");
    assert!(!body.to_string().contains("sensitive-"));
    assert_eq!(upstream.calls.load(Ordering::SeqCst), 1);
}

#[tokio::test]
async fn stalled_discovery_closes_socket_when_gateway_deadline_expires() {
    use tokio::io::{AsyncReadExt, AsyncWriteExt};
    for prefix in [
        "",
        "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 1000\r\n\r\n{",
        "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\n\r\ndata: {",
    ] {
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let url = format!("http://{}/mcp", listener.local_addr().unwrap());
        let (closed_tx, closed_rx) = tokio::sync::oneshot::channel();
        let server = tokio::spawn(async move {
            let (mut socket, _) = listener.accept().await.unwrap();
            let mut buf = [0u8; 4096];
            assert!(socket.read(&mut buf).await.unwrap() > 0);
            socket.write_all(prefix.as_bytes()).await.unwrap();
            loop {
                match socket.read(&mut buf).await {
                    Ok(0) | Err(_) => break,
                    Ok(_) => {}
                }
            }
            let _ = closed_tx.send(());
        });
        let mut config = config(&url);
        config.request_timeout_ms = 100;
        let limit = ConcurrencyLimit::new(1).unwrap();
        let runtime = limited_runtime(config, limit.clone());
        let (_, value) = response(
            &runtime,
            request("tools/call", Some("search_documents"), "client-secret"),
        )
        .await;
        assert!(value.get("error").is_some());
        let closed = tokio::time::timeout(std::time::Duration::from_millis(500), closed_rx).await;
        server.abort();
        assert!(
            closed.is_ok(),
            "stalled discovery socket survived request cancellation"
        );
        assert_eq!(limit.available(), 1);
    }
}

#[tokio::test]
async fn parameter_header_mismatch_is_rejected_before_tool_execution() {
    let upstream = upstream("annotated").await;
    let runtime = runtime(config(&upstream.url));
    for header in [None, Some("wrong")] {
        let mut req = request("tools/call", Some("search_documents"), "client-secret");
        if let Some(value) = header {
            req.headers_mut()
                .insert("mcp-param-query", value.parse().unwrap());
        }
        let (status, value) = response(&runtime, req).await;
        assert_eq!(status, StatusCode::BAD_REQUEST, "{value}");
        assert_eq!(upstream.calls.load(Ordering::SeqCst), 0);
    }
    let mut duplicated = request("tools/call", Some("search_documents"), "client-secret");
    duplicated
        .headers_mut()
        .append("mcp-param-query", "Hello".parse().unwrap());
    duplicated
        .headers_mut()
        .append("mcp-param-query", "wrong".parse().unwrap());
    assert_eq!(
        response(&runtime, duplicated).await.0,
        StatusCode::BAD_REQUEST
    );
    assert_eq!(upstream.calls.load(Ordering::SeqCst), 0);
    for header in ["Hello", "=?base64?SGVsbG8=?="] {
        let mut req = request("tools/call", Some("search_documents"), "client-secret");
        req.headers_mut()
            .insert("mcp-param-query", header.parse().unwrap());
        let (status, value) = response(&runtime, req).await;
        assert_eq!(status, StatusCode::OK, "{value}");
        assert!(value.get("result").is_some());
    }
    assert_eq!(upstream.calls.load(Ordering::SeqCst), 2);
}
