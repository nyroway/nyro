//! Single-operation SDK clients with bounded HTTP and no recovery/replay.
use crate::{
    body,
    config::{Config, Server},
    error,
};
use axum::http::{HeaderName, HeaderValue};
use futures::{StreamExt, stream::BoxStream};
use rmcp::{
    ClientHandler, RoleClient, RoleServer,
    model::*,
    service::{ClientLifecycleMode, NotificationContext, Peer, serve_client_with_lifecycle_and_ct},
    transport::{
        StreamableHttpClientTransport,
        common::client_side_sse::SseRetryPolicy,
        streamable_http_client::{
            StreamableHttpClient, StreamableHttpClientTransportConfig, StreamableHttpError,
            StreamableHttpPostResponse,
        },
    },
};
use sse_stream::{Error as SseError, Sse, SseStream};
use std::{
    collections::{HashMap, HashSet},
    io,
    sync::{Arc, atomic::AtomicUsize},
    time::Duration,
};
use tokio::time::Instant;
use tokio_util::sync::CancellationToken;

#[derive(Clone)]
struct BoundedClient {
    http: reqwest::Client,
    remaining: Arc<AtomicUsize>,
    max_frame: usize,
    cancel: CancellationToken,
    deadline: Instant,
}
type HttpError = StreamableHttpError<io::Error>;
fn bad() -> HttpError {
    StreamableHttpError::Client(io::Error::other("MCP upstream transport failed"))
}
impl BoundedClient {
    async fn post(
        &self,
        uri: Arc<str>,
        message: ClientJsonRpcMessage,
        session: Option<Arc<str>>,
        auth: Option<String>,
        headers: HashMap<HeaderName, HeaderValue>,
    ) -> Result<StreamableHttpPostResponse, HttpError> {
        if session.is_some() {
            return Err(bad());
        }
        let mut request = self
            .http
            .post(uri.as_ref())
            .timeout(self.deadline.saturating_duration_since(Instant::now()))
            .header("accept", "application/json, text/event-stream")
            .json(&message);
        for (name, value) in headers {
            request = request.header(name, value);
        }
        if let Some(token) = auth {
            request = request.bearer_auth(token);
        }
        let response = request.send().await.map_err(|_| bad())?;
        // No status is a signal to reinitialize or replay a side-effecting call.
        if !response.status().is_success() || response.headers().contains_key("mcp-session-id") {
            return Err(bad());
        }
        if response
            .headers()
            .get("mcp-protocol-version")
            .is_some_and(|v| v != "2026-07-28")
        {
            return Err(bad());
        }
        let mime = response
            .headers()
            .get("content-type")
            .and_then(|s| s.to_str().ok())
            .unwrap_or("")
            .split(';')
            .next()
            .unwrap_or("")
            .trim()
            .to_owned();
        if mime == "application/json" {
            let mut stream = body::bounded(response, self.remaining.clone(), None);
            let mut bytes = Vec::new();
            while let Some(chunk) = stream.next().await {
                bytes.extend_from_slice(&chunk.map_err(|_| bad())?);
            }
            let message = serde_json::from_slice(&bytes).map_err(|_| bad())?;
            Ok(StreamableHttpPostResponse::Json(message, None))
        } else if mime == "text/event-stream" {
            let cancel = self.cancel.clone();
            let deadline = self.deadline;
            let stream = body::bounded(response, self.remaining.clone(), Some(self.max_frame))
                .take_until(async move {
                    tokio::select! { _=cancel.cancelled()=>{}, _=tokio::time::sleep_until(deadline)=>{} }
                });
            Ok(StreamableHttpPostResponse::Sse(
                SseStream::from_bytes_stream(stream).boxed(),
                None,
            ))
        } else {
            Err(bad())
        }
    }
}
impl StreamableHttpClient for BoundedClient {
    type Error = io::Error;
    async fn post_message(
        &self,
        uri: Arc<str>,
        message: ClientJsonRpcMessage,
        session: Option<Arc<str>>,
        auth: Option<String>,
        headers: HashMap<HeaderName, HeaderValue>,
    ) -> Result<StreamableHttpPostResponse, HttpError> {
        // SDK startup awaits its first POST outside the worker cancellation loop.
        // Enforce lifetime at the actual I/O boundary, including that first POST.
        tokio::select! {
            biased;
            _ = self.cancel.cancelled() => Err(bad()),
            _ = tokio::time::sleep_until(self.deadline) => Err(bad()),
            result = self.post(uri, message, session, auth, headers) => result,
        }
    }
    async fn delete_session(
        &self,
        _: Arc<str>,
        _: Arc<str>,
        _: Option<String>,
        _: HashMap<HeaderName, HeaderValue>,
    ) -> Result<(), HttpError> {
        Err(bad())
    }
    async fn get_stream(
        &self,
        _: Arc<str>,
        _: Option<Arc<str>>,
        _: Option<String>,
        _: Option<String>,
        _: HashMap<HeaderName, HeaderValue>,
    ) -> Result<BoxStream<'static, Result<Sse, SseError>>, HttpError> {
        Err(bad())
    }
}
#[derive(Debug)]
struct NoRetry;
impl SseRetryPolicy for NoRetry {
    fn retry(&self, _: usize) -> Option<Duration> {
        None
    }
}
struct Client {
    downstream: Peer<RoleServer>,
}
impl ClientHandler for Client {
    async fn on_progress(
        &self,
        params: ProgressNotificationParam,
        _: NotificationContext<RoleClient>,
    ) {
        let _ = self.downstream.notify_progress(params).await;
    }
    fn get_info(&self) -> ClientConfig {
        ClientConfig::new(
            ClientCapabilities::default(),
            Implementation::new("nyro", env!("CARGO_PKG_VERSION")),
        )
        .with_protocol_version(ProtocolVersion::V_2026_07_28)
    }
}
pub(crate) enum Operation {
    List(Option<PaginatedRequestParams>),
    Call(CallToolRequestParams, axum::http::HeaderMap),
}
pub(crate) async fn run(
    http: reqwest::Client,
    config: Arc<Config>,
    server: Server,
    operation: Operation,
    peer: Peer<RoleServer>,
    cancel: CancellationToken,
    deadline: Instant,
) -> Result<ServerResult, ErrorData> {
    let ct = cancel.child_token();
    let _guard = ct.clone().drop_guard();
    let adapter = BoundedClient {
        http,
        remaining: Arc::new(AtomicUsize::new(config.max_response_bytes)),
        max_frame: config.max_frame_bytes,
        cancel: ct.clone(),
        deadline,
    };
    let mut transport_config = StreamableHttpClientTransportConfig::with_uri(server.url.clone())
        .reinit_on_expired_session(false);
    transport_config.auth_header = server.bearer_token.clone();
    transport_config.retry_config = Arc::new(NoRetry);
    transport_config.max_sse_event_size = config.max_frame_bytes;
    let transport = StreamableHttpClientTransport::with_client(adapter, transport_config);
    let connect = serve_client_with_lifecycle_and_ct(
        Client { downstream: peer },
        transport,
        ClientLifecycleMode::Discover {
            preferred_versions: vec![ProtocolVersion::V_2026_07_28],
        },
        ct.clone(),
    );
    let mut client = tokio::select! {
        biased;
        _=cancel.cancelled()=>return Err(error::upstream()),
        _=tokio::time::sleep_until(deadline)=>return Err(error::upstream()),
        result=connect=>result.map_err(|_|error::upstream())?,
    };
    let operation = async {
        match operation {
            Operation::List(mut params) => {
                if let Some(params) = &mut params {
                    params.meta = None;
                }
                let mut result = client.list_tools(params).await.map_err(error::service)?;
                result.cache_scope = Some(CacheScope::Private);
                result.tools.retain(|tool| {
                    server
                        .allowed_tools
                        .iter()
                        .any(|name| name == tool.name.as_ref())
                });
                Ok(ServerResult::ListToolsResult(result))
            }
            Operation::Call(mut params, headers) => {
                if !server
                    .allowed_tools
                    .iter()
                    .any(|name| name == params.name.as_ref())
                {
                    return Err(ErrorData::invalid_request("Tool access denied", None));
                }
                // Populate the SDK's schema/header cache before a call. Unlike tools/list
                // proxying, this local lookup can follow pages, within the same total
                // byte budget/deadline, with repeated cursors rejected.
                let mut cursor = None;
                let mut seen = HashSet::new();
                loop {
                    let page = client
                        .list_tools(Some(PaginatedRequestParams::default().with_cursor(cursor)))
                        .await
                        .map_err(error::service)?;
                    if let Some(tool) = page.tools.iter().find(|tool| tool.name == params.name) {
                        crate::headers::validate_params(
                            &headers,
                            params.arguments.as_ref(),
                            &tool.input_schema,
                        )?;
                        break;
                    }
                    cursor = page.next_cursor;
                    if cursor.as_ref().is_none_or(|c| !seen.insert(c.clone())) {
                        return Err(ErrorData::invalid_params("Tool unavailable", None));
                    }
                }
                // Keep only the request's progress token. Client identity/capabilities
                // on the next hop describe Nyro, never the downstream peer.
                params.meta = params.meta.and_then(|meta| {
                    let raw = serde_json::to_value(meta).ok()?;
                    let token = raw.get("progressToken")?;
                    serde_json::from_value(serde_json::json!({"progressToken":token})).ok()
                });
                match client
                    .call_tool_once(params)
                    .await
                    .map_err(error::service)?
                {
                    CallToolResponse::Complete(result) => Ok(ServerResult::CallToolResult(result)),
                    _ => Err(ErrorData::internal_error(
                        "Upstream returned an unsupported MCP capability",
                        None,
                    )),
                }
            }
        }
    };
    let result = tokio::select! {
        biased;
        _=cancel.cancelled()=>Err(error::upstream()),
        _=tokio::time::sleep_until(deadline)=>Err(error::upstream()),
        result=operation=>result,
    };
    let _ = client.close_with_timeout(Duration::from_secs(1)).await;
    result
}
