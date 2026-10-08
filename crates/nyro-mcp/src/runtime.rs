use crate::{
    body,
    config::{self, Config},
    error,
    handler::Handler,
};
use axum::{
    body::{Body, to_bytes},
    http::{Request, Response, StatusCode},
};
use nyro_authn::Authenticator;
use nyro_limit::ConcurrencyLimit;
use rmcp::{
    model::{ErrorData, ProtocolVersion},
    transport::streamable_http_server::{
        StreamableHttpServerConfig, StreamableHttpService, session::never::NeverSessionManager,
    },
};
use serde_json::Value;
use std::{
    sync::{
        Arc,
        atomic::{AtomicU64, Ordering},
    },
    time::Duration,
};
use thiserror::Error;
use tokio::time::Instant;
use tokio_util::sync::CancellationToken;
use tracing::Instrument;

/// Application-owned pool selection state, shared by services and generations.
#[derive(Clone, Default)]
pub struct Pools(Arc<std::sync::Mutex<std::collections::HashMap<String, nyro_balance::Balancer>>>);
#[derive(Clone)]
pub struct Target {
    pub id: String,
    pub weight: u32,
    pub server: config::Server,
}
#[derive(Clone)]
pub struct ServicePolicy {
    pub anonymous: bool,
    pub pool: String,
    pub strategy: nyro_balance::Strategy,
    pub targets: Vec<Target>,
    pub limits: nyro_limit::token::Policy,
    pub execution: Arc<Config>,
}
#[derive(Clone, Default)]
pub struct Policies {
    pub registry: nyro_limit::token::Registry,
    pub consumers: std::collections::BTreeMap<String, nyro_limit::token::Policy>,
    pub services: std::collections::BTreeMap<String, ServicePolicy>,
    pub pools: Pools,
}

pub struct Runtime {
    policies: Policies,
    config: Arc<Config>,
    keys: Arc<dyn Authenticator>,
    authorizer: nyro_authz::Authorizer,
    limit: ConcurrencyLimit,
    http: reqwest::Client,
}
#[derive(Debug, Error)]
pub enum BuildError {
    #[error(transparent)]
    Config(#[from] config::ConfigError),
    #[error("could not build MCP HTTP client")]
    Http,
}
impl Runtime {
    pub fn new(
        config: Config,
        keys: Arc<dyn Authenticator>,
        limit: ConcurrencyLimit,
    ) -> Result<Self, BuildError> {
        Self::with_policies(config, keys, limit, Policies::default())
    }
    pub fn with_policies(
        config: Config,
        keys: Arc<dyn Authenticator>,
        limit: ConcurrencyLimit,
        policies: Policies,
    ) -> Result<Self, BuildError> {
        config.validate()?;
        for policy in policies.services.values() {
            if !matches!(
                policy.strategy,
                nyro_balance::Strategy::WeightedRandom | nyro_balance::Strategy::WeightedRoundrobin
            ) || !policy.targets.iter().any(|t| t.weight > 0)
            {
                return Err(BuildError::Config(config::ConfigError("invalid MCP pool")));
            }
            policy.execution.validate()?;
            for target in &policy.targets {
                let mut cfg = Config::default();
                cfg.servers.insert("target".into(), target.server.clone());
                cfg.validate()?;
            }
        }
        let http = reqwest::Client::builder()
            .redirect(reqwest::redirect::Policy::none())
            .no_proxy()
            .retry(reqwest::retry::never())
            .build()
            .map_err(|_| BuildError::Http)?;
        let authorizer = nyro_authz::Authorizer::new(
            config
                .servers
                .iter()
                .flat_map(|(id, server)| {
                    server.subjects.iter().map(|subject| nyro_authz::Grant {
                        subject: subject.clone(),
                        action: "invoke".into(),
                        resource: id.clone(),
                    })
                })
                .collect(),
        );
        Ok(Self {
            authorizer,
            policies,
            config: Arc::new(config),
            keys,
            limit,
            http,
        })
    }
    pub fn request_timeout(&self) -> Duration {
        Duration::from_millis(self.config.request_timeout_ms)
    }
    pub async fn handle(
        &self,
        request: Request<Body>,
        cancellation: CancellationToken,
    ) -> Response<Body> {
        static NEXT: AtomicU64 = AtomicU64::new(1);
        let request_id = NEXT.fetch_add(1, Ordering::Relaxed);
        let server_id = request
            .uri()
            .path()
            .strip_prefix("/mcp/")
            .filter(|id| config::valid_id(id))
            .unwrap_or("unknown")
            .to_owned();
        let operation = match request
            .headers()
            .get("mcp-method")
            .and_then(|v| v.to_str().ok())
        {
            Some("server/discover") => "server/discover",
            Some("tools/list") => "tools/list",
            Some("tools/call") => "tools/call",
            _ => "unsupported",
        };
        let span = tracing::info_span!("mcp_request", request_id, server_id, operation);
        let start = Instant::now();
        let timeout = self
            .policies
            .services
            .get(&server_id)
            .map_or(self.request_timeout(), |p| {
                Duration::from_millis(p.execution.request_timeout_ms)
            });
        let deadline = start + timeout;
        let cancel = cancellation.child_token();
        let guard = cancel.clone().drop_guard();
        let result = tokio::select! {
            biased;
            _=cancel.cancelled()=>error::reject(StatusCode::SERVICE_UNAVAILABLE,"MCP request cancelled"),
            _=tokio::time::sleep_until(deadline)=>error::reject(StatusCode::GATEWAY_TIMEOUT,"MCP request deadline exceeded"),
            result=self.dispatch(request,cancel.clone(),deadline).instrument(span)=>result,
        };
        tracing::info!(
            request_id,
            server_id,
            operation,
            application = "mcp",
            status = result.status().as_u16(),
            elapsed_ms = start.elapsed().as_millis() as u64,
            "MCP request dispatched"
        );
        guard.disarm();
        result
    }
    async fn dispatch(
        &self,
        request: Request<Body>,
        cancel: CancellationToken,
        deadline: Instant,
    ) -> Response<Body> {
        let (parts, body) = request.into_parts();
        let Some(id) = parts
            .uri
            .path()
            .strip_prefix("/mcp/")
            .filter(|id| config::valid_id(id))
        else {
            return error::reject(StatusCode::NOT_FOUND, "MCP service not found");
        };
        let Some(server) = self.config.servers.get(id) else {
            return error::reject(StatusCode::NOT_FOUND, "MCP service not found");
        };
        if parts.uri.query().is_some() {
            return error::reject(
                StatusCode::BAD_REQUEST,
                "MCP query parameters are not supported",
            );
        }
        if parts.headers.contains_key("origin") {
            return error::reject(StatusCode::FORBIDDEN, "Browser origins are not supported");
        }
        if parts.method != axum::http::Method::POST {
            return error::reject(StatusCode::METHOD_NOT_ALLOWED, "MCP requires POST");
        }
        if parts.headers.contains_key("mcp-session-id")
            || parts.headers.contains_key("last-event-id")
        {
            return error::reject(
                StatusCode::BAD_REQUEST,
                "MCP sessions and stream resumption are not supported",
            );
        }
        let policy = self.policies.services.get(id);
        let execution = policy.map_or(&self.config, |p| &p.execution);
        let identity = if policy.is_some_and(|p| p.anonymous) {
            None
        } else {
            let mut auth = parts.headers.get_all("authorization").iter();
            let identity = auth
                .next()
                .and_then(|v| v.to_str().ok())
                .and_then(|v| v.split_once(' '))
                .filter(|(scheme, _)| scheme.eq_ignore_ascii_case("bearer"))
                .and_then(|(_, secret)| self.keys.authenticate(secret).ok());
            let Some(identity) = identity.filter(|_| auth.next().is_none()) else {
                return error::reject(StatusCode::UNAUTHORIZED, "Authentication failed");
            };
            if self.authorizer.authorize(&identity, "invoke", id).is_err() {
                return error::reject(StatusCode::FORBIDDEN, "MCP service access denied");
            }
            Some(identity)
        };
        let bytes = match to_bytes(body, execution.max_body_bytes).await {
            Ok(bytes) => bytes,
            Err(_) => {
                return error::reject(
                    StatusCode::PAYLOAD_TOO_LARGE,
                    "Invalid or oversized MCP body",
                );
            }
        };
        let raw: Value = match serde_json::from_slice(&bytes) {
            Ok(value) => value,
            Err(_) => {
                return error::response(
                    StatusCode::BAD_REQUEST,
                    None,
                    ErrorData::parse_error("Invalid JSON", None),
                );
            }
        };
        let rpc_id = raw.get("id").cloned();
        let method = raw.get("method").and_then(Value::as_str).unwrap_or("");
        if rpc_id.is_none() || raw.get("jsonrpc").and_then(Value::as_str) != Some("2.0") {
            return error::reject(StatusCode::BAD_REQUEST, "A JSON-RPC request is required");
        }
        // SDK validates encoding and body/header consistency. Reject duplicates
        // here so HTTP and JSON parsers cannot choose different routing metadata.
        for header in [
            "mcp-protocol-version",
            "mcp-method",
            "mcp-name",
            "content-type",
        ] {
            if parts.headers.get_all(header).iter().count() > 1 {
                return error::response(
                    StatusCode::BAD_REQUEST,
                    rpc_id,
                    ErrorData::header_mismatch("Duplicate MCP metadata", None),
                );
            }
        }
        let revision = parts
            .headers
            .get("mcp-protocol-version")
            .and_then(|v| v.to_str().ok());
        if let Some(revision) = revision.filter(|v| *v != "2026-07-28") {
            let version: ProtocolVersion =
                serde_json::from_value(Value::String(revision.into())).unwrap();
            return error::response(
                StatusCode::BAD_REQUEST,
                rpc_id,
                ErrorData::unsupported_protocol_version(version, &[ProtocolVersion::V_2026_07_28]),
            );
        }
        if !matches!(method, "server/discover" | "tools/list" | "tools/call") {
            return error::response(
                StatusCode::NOT_FOUND,
                rpc_id,
                ErrorData::new(
                    rmcp::model::ErrorCode::METHOD_NOT_FOUND,
                    "Method not found",
                    None,
                ),
            );
        }
        if method == "tools/call" {
            let params = &raw["params"];
            if params.get("inputResponses").is_some()
                || params.get("requestState").is_some()
                || params.get("task").is_some()
            {
                return error::response(
                    StatusCode::BAD_REQUEST,
                    rpc_id,
                    ErrorData::invalid_params("MCP continuation and tasks are not supported", None),
                );
            }
            let name = params["name"].as_str().unwrap_or("");
            // Leave encoded names to SDK validation; plain mismatch must take
            // precedence over the local tool rule to avoid header-based bypasses.
            if let Some(header) = parts.headers.get("mcp-name").and_then(|v| v.to_str().ok())
                && !header.starts_with("=?base64?")
                && header != name
            {
                return error::response(
                    StatusCode::BAD_REQUEST,
                    rpc_id,
                    ErrorData::header_mismatch("MCP name does not match body", None),
                );
            }
            if !server.allowed_tools.iter().any(|tool| tool == name) {
                return error::response(
                    StatusCode::FORBIDDEN,
                    rpc_id,
                    ErrorData::invalid_request("Tool access denied", None),
                );
            }
        }
        let permit = if method == "server/discover" {
            None
        } else {
            match self.limit.try_acquire() {
                Ok(permit) => Some(permit),
                Err(_) => {
                    return error::reject(
                        StatusCode::TOO_MANY_REQUESTS,
                        "Gateway concurrency exhausted",
                    );
                }
            }
        };
        let limits: Vec<_> = policy
            .map(|p| &p.limits)
            .into_iter()
            .chain(
                identity
                    .as_ref()
                    .and_then(|i| self.policies.consumers.get(&i.id)),
            )
            .collect();
        if self.policies.registry.admit(&limits).is_err() {
            return error::reject(StatusCode::TOO_MANY_REQUESTS, "Request limit exceeded");
        }
        let selected = policy
            .map(|policy| {
                let targets: Vec<_> = policy
                    .targets
                    .iter()
                    .map(|t| (t.id.as_str(), t.weight))
                    .collect();
                let mut pools = self.policies.pools.0.lock().unwrap();
                let balancer = pools.entry(policy.pool.clone()).or_default();
                let choice = balancer
                    .choose(policy.strategy, &targets, &[], &mut rand::thread_rng())
                    .expect("validated pool");
                &policy.targets[choice].server
            })
            .unwrap_or(server);
        let handler = Handler {
            http: self.http.clone(),
            config: execution.clone(),
            server: selected.clone(),
            deadline,
        };
        let config = StreamableHttpServerConfig::default()
            .with_legacy_session_mode(false)
            .with_stateless_protocol_metadata_required(true)
            .with_json_response(true)
            .with_max_request_body_bytes(execution.max_body_bytes)
            .disable_allowed_hosts()
            .with_cancellation_token(cancel.clone());
        let service = StreamableHttpService::new(
            move || Ok(handler.clone()),
            Arc::new(NeverSessionManager::default()),
            config,
        );
        let response = service
            .handle(Request::from_parts(parts, Body::from(bytes)))
            .await;
        let (parts, body) = response.into_parts();
        Response::from_parts(
            parts,
            body::retain(Body::new(body), permit, cancel, deadline),
        )
    }
}
