//! Trusted LLM execution with bounded failover. Business policy stays outside the kernel.

use std::{collections::BTreeMap, sync::Arc, time::Duration};

use axum::{
    body::Body,
    http::{Request as HttpRequest, Response as HttpResponse, StatusCode},
};
use futures::StreamExt;
use nyro_limit::{ConcurrencyLimit, Permit};
use nyro_security::{ApiKeys, Authorizer, Grant};
use serde_json::{Value, json};
use tokio::time::Instant;
use tokio_util::sync::CancellationToken;

use crate::{
    Request,
    codec::ChatFormat,
    config,
    health::{BackendHealth, HealthRegistry},
    ingress::body::{self, Outcome},
    provider::Driver,
    router,
};

mod endpoint;
mod response;
mod stream;

#[derive(Clone, Copy, Debug)]
pub struct Options {
    pub request_timeout: Duration,
    pub max_body_bytes: usize,
    pub max_response_bytes: usize,
    pub max_frame_bytes: usize,
}

impl Default for Options {
    fn default() -> Self {
        Self {
            request_timeout: Duration::from_secs(120),
            max_body_bytes: 1024 * 1024,
            max_response_bytes: 16 * 1024 * 1024,
            max_frame_bytes: 1024 * 1024,
        }
    }
}

#[derive(Debug, thiserror::Error)]
#[error("Invalid runtime configuration")]
pub struct BuildError;

pub struct Runtime {
    models: BTreeMap<String, config::Model>,
    providers: BTreeMap<String, Driver>,
    keys: Arc<ApiKeys>,
    authorizer: Authorizer,
    limit: ConcurrencyLimit,
    options: Options,
    health: BTreeMap<String, BTreeMap<String, Arc<BackendHealth>>>,
}

impl Runtime {
    pub fn new(
        config: config::Config,
        keys: Arc<ApiKeys>,
        limit: ConcurrencyLimit,
        options: Options,
    ) -> Result<Self, BuildError> {
        Self::with_health(
            config,
            keys,
            limit,
            options,
            Arc::new(HealthRegistry::default()),
        )
    }

    /// Share this registry between generations to retain health for unchanged backends.
    pub fn with_health(
        config: config::Config,
        keys: Arc<ApiKeys>,
        limit: ConcurrencyLimit,
        options: Options,
        health: Arc<HealthRegistry>,
    ) -> Result<Self, BuildError> {
        config.validate().map_err(|_| BuildError)?;
        if options.request_timeout.is_zero()
            || Instant::now()
                .checked_add(options.request_timeout)
                .is_none()
            || options.max_body_bytes == 0
            || options.max_response_bytes == 0
            || options.max_frame_bytes == 0
        {
            return Err(BuildError);
        }
        let providers = config
            .providers
            .iter()
            .map(|(id, provider)| Driver::new(provider).map(|driver| (id.clone(), driver)))
            .collect::<Result<_, _>>()?;
        let grants = config
            .models
            .iter()
            .flat_map(|(id, model)| {
                model.subjects.iter().map(move |subject| Grant {
                    subject: subject.clone(),
                    action: "invoke".into(),
                    resource: id.clone(),
                })
            })
            .collect();
        let health = config
            .models
            .iter()
            .filter_map(|(id, model)| {
                model.health.as_ref().map(|policy| {
                    let backends = model
                        .backends
                        .iter()
                        .map(|backend| {
                            let state = health.backend(
                                id,
                                backend,
                                &config.providers[&backend.provider],
                                policy,
                            );
                            (backend.id.clone(), state)
                        })
                        .collect();
                    (id.clone(), backends)
                })
            })
            .collect();
        Ok(Self {
            health,
            models: config.models,
            providers,
            keys,
            authorizer: Authorizer::new(grants),
            limit,
            options,
        })
    }

    pub fn request_timeout(&self) -> Duration {
        self.options.request_timeout
    }

    /// The caller owns its generation lease and keeps it through the returned body.
    /// Dropping the future/body cancels owned upstream work; no detached dispatcher is spawned.
    pub async fn handle(
        &self,
        request: HttpRequest<Body>,
        cancellation: CancellationToken,
    ) -> HttpResponse<Body> {
        let format = endpoint::kind(request.uri().path());
        let started = Instant::now();
        let deadline = started + self.options.request_timeout;
        let mut exchange = Exchange {
            started,
            model: None,
            backend: None,
            attempts: 0,
            permit: None,
            status: None,
            outcome: Outcome::Cancelled,
        };
        let result = tokio::select! {
            biased;
            _ = cancellation.cancelled() => Err(Failure::cancelled()),
            _ = tokio::time::sleep_until(deadline) => Err(Failure::timeout()),
            result = self.execute(request, &mut exchange, &cancellation, deadline) => result,
        };
        let (response, body_deadline) = match result {
            Ok(response) => (response, deadline),
            // Error delivery is a separate, bounded terminal action, so a 504 body can be read.
            Err(failure) => (
                failure.response(format),
                Instant::now() + Duration::from_secs(5),
            ),
        };
        exchange.status = Some(response.status());
        let (parts, response_body) = response.into_parts();
        HttpResponse::from_parts(
            parts,
            body::managed(response_body, cancellation, body_deadline, move |outcome| {
                exchange.finish(outcome)
            }),
        )
    }

    /// Preparation is local and side-effect free: incompatible codecs never get a network attempt.
    fn prepare_backends<'a>(
        &'a self,
        model_id: &str,
        model: &'a config::Model,
        request: &Request,
        downstream: ChatFormat,
    ) -> Result<Vec<Prepared<'a>>, Failure> {
        let mut eligible = Vec::new();
        for backend in model.backends.iter().filter(|backend| backend.weight > 0) {
            let provider = self
                .providers
                .get(&backend.provider)
                .ok_or_else(Failure::upstream)?;
            let mut request = request.clone();
            request.set_model(backend.upstream_model.clone());
            if provider.format == ChatFormat::OpenAiChat
                && let Request::Chat(chat) = &mut request
            {
                if downstream == ChatFormat::OpenAiResponses {
                    chat.openai.store = Some(false);
                }
                if chat.stream == Some(true) && downstream != ChatFormat::OpenAiChat {
                    chat.openai
                        .stream_options
                        .get_or_insert(nyro_protocol::openai::chat::StreamOptions {
                            include_usage: None,
                            include_obfuscation: None,
                        })
                        .include_usage = Some(true);
                }
            }
            if let Ok(encoded) = provider.encode(&request) {
                eligible.push(Prepared {
                    backend,
                    provider,
                    request,
                    encoded,
                    health: self
                        .health
                        .get(model_id)
                        .and_then(|states| states.get(&backend.id)),
                });
            }
        }
        if eligible.is_empty() {
            return Err(Failure::invalid(
                "Request cannot be represented by any enabled backend",
            ));
        }
        Ok(eligible)
    }

    async fn execute(
        &self,
        request: HttpRequest<Body>,
        exchange: &mut Exchange,
        cancellation: &CancellationToken,
        deadline: Instant,
    ) -> Result<HttpResponse<Body>, Failure> {
        let endpoint = endpoint::Endpoint::parse(request.uri(), request.method())?;
        let workload = endpoint.workload;
        let (parts, body) = request.into_parts();
        let mut input = Vec::new();
        let mut data = body.into_data_stream();
        while let Some(chunk) = data.next().await {
            let chunk = chunk.map_err(|_| Failure::invalid("Cannot read request body"))?;
            if chunk.len() > self.options.max_body_bytes.saturating_sub(input.len()) {
                return Err(Failure::new(
                    StatusCode::PAYLOAD_TOO_LARGE,
                    "request_too_large",
                    "Request body exceeds the configured limit",
                ));
            }
            input.extend_from_slice(&chunk);
        }
        let value: Value =
            serde_json::from_slice(&input).map_err(|_| Failure::invalid("Invalid JSON request"))?;
        let request = endpoint.decode(value)?;
        let public_model = request.model().to_owned();
        exchange.model = Some(public_model.clone());
        // Resolve -> Authenticate -> Authorize -> Admit are mandatory ordinary runtime steps.
        let model = self.models.get(&public_model).ok_or_else(|| {
            Failure::new(StatusCode::NOT_FOUND, "model_not_found", "Unknown model")
        })?;
        if !model.workloads.contains(&workload) {
            return Err(Failure::invalid("Model does not support this workload"));
        }
        match endpoint.credential(&parts.headers)? {
            Some(secret) => {
                let identity = self
                    .keys
                    .authenticate(secret)
                    .map_err(|_| Failure::unauthorized())?;
                if !model.allow_anonymous {
                    self.authorizer
                        .authorize(&identity, "invoke", &public_model)
                        .map_err(|_| {
                            Failure::new(
                                StatusCode::FORBIDDEN,
                                "permission_denied",
                                "Model access denied",
                            )
                        })?;
                }
            }
            None if model.allow_anonymous => {}
            None => return Err(Failure::unauthorized()),
        }
        exchange.permit = Some(self.limit.try_acquire().map_err(|_| {
            Failure::new(
                StatusCode::TOO_MANY_REQUESTS,
                "concurrency_limit_exceeded",
                "Concurrency limit reached",
            )
        })?);
        let mut eligible =
            self.prepare_backends(&public_model, model, &request, endpoint.format)?;
        let mut last_failure = None;
        while exchange.attempts < model.max_attempts {
            if cancellation.is_cancelled() {
                return Err(Failure::cancelled());
            }
            if Instant::now() >= deadline {
                return Err(Failure::timeout());
            }
            let available: Vec<_> = eligible
                .iter()
                .enumerate()
                .filter(|(_, candidate)| candidate.health.is_none_or(|state| state.available()))
                .collect();
            let Some(priority) = available
                .iter()
                .map(|(_, candidate)| candidate.backend.priority)
                .min()
            else {
                break;
            };
            let choice = router::choose(available.iter().map(|(_, candidate)| {
                if candidate.backend.priority == priority {
                    candidate.backend.weight
                } else {
                    0
                }
            }))
            .expect("available candidates have positive weights");
            let index = available[choice].0;
            // Another request may have claimed the recovery probe after our availability check.
            let selected = eligible.swap_remove(index);
            let mut health = match selected.health {
                Some(state) => match state.try_acquire() {
                    Some(attempt) => Some(attempt),
                    None => continue,
                },
                None => None,
            };
            exchange.backend = Some(selected.backend.id.clone());
            exchange.attempts += 1;
            let response = match selected
                .provider
                .send(&selected.request, &selected.encoded)
                .await
            {
                Ok(response) => response,
                Err(error) => {
                    if let Some(health) = health.take() {
                        health.failure();
                    }
                    // Only connection establishment is known to precede sending the request.
                    if !error.is_connect() {
                        return Err(Failure::upstream());
                    }
                    last_failure = Some(Failure::upstream());
                    continue;
                }
            };
            if !response.status().is_success() {
                let status = response.status();
                let retryable = matches!(status.as_u16(), 429 | 500 | 502 | 503 | 504 | 529);
                let safe_status = if status.is_redirection()
                    || matches!(status, StatusCode::UNAUTHORIZED | StatusCode::FORBIDDEN)
                {
                    StatusCode::BAD_GATEWAY
                } else {
                    status
                };
                let failure = Failure::new(
                    safe_status,
                    "upstream_error",
                    "Upstream rejected the request",
                );
                // Discard failed headers/body and release the connection before another send.
                drop(response);
                if !retryable {
                    return Err(failure);
                }
                if let Some(health) = health.take() {
                    health.failure();
                }
                last_failure = Some(failure);
                continue;
            }
            // An upstream 2xx commits the attempt, including malformed or interrupted bodies.
            let result = self
                .decode_response(
                    response,
                    selected.provider,
                    &endpoint,
                    &request,
                    &mut health,
                )
                .await;
            if let Some(health) = health {
                if result.is_ok() {
                    health.success();
                } else {
                    health.failure();
                }
            }
            return result;
        }
        Err(last_failure.unwrap_or_else(|| {
            Failure::new(
                StatusCode::SERVICE_UNAVAILABLE,
                "backends_unavailable",
                "No backend is currently available",
            )
        }))
    }
}

struct Prepared<'a> {
    backend: &'a config::Backend,
    provider: &'a Driver,
    request: Request,
    encoded: Value,
    health: Option<&'a Arc<BackendHealth>>,
}

struct Exchange {
    started: Instant,
    model: Option<String>,
    backend: Option<String>,
    attempts: u32,
    permit: Option<Permit>,
    status: Option<StatusCode>,
    outcome: Outcome,
}

impl Exchange {
    fn finish(mut self, outcome: Outcome) {
        self.outcome = outcome;
    }
}

impl Drop for Exchange {
    fn drop(&mut self) {
        tracing::info!(target: "nyro::request", model = self.model.as_deref().unwrap_or(""), backend = self.backend.as_deref().unwrap_or(""), status = self.status.map_or(0, |status| status.as_u16()),
            attempts = self.attempts, duration_ms = self.started.elapsed().as_millis() as u64, outcome = ?self.outcome, "LLM request finished");
        // This slice owns only synchronous finalization; the permit releases after observation.
    }
}

#[derive(Debug, thiserror::Error)]
#[error("{message}")]
struct Failure {
    status: StatusCode,
    code: &'static str,
    message: &'static str,
}

impl Failure {
    fn new(status: StatusCode, code: &'static str, message: &'static str) -> Self {
        Self {
            status,
            code,
            message,
        }
    }
    fn invalid(message: &'static str) -> Self {
        Self::new(StatusCode::BAD_REQUEST, "invalid_request_error", message)
    }
    fn unauthorized() -> Self {
        Self::new(
            StatusCode::UNAUTHORIZED,
            "authentication_error",
            "A valid Bearer API key is required",
        )
    }
    fn upstream() -> Self {
        Self::new(
            StatusCode::BAD_GATEWAY,
            "upstream_error",
            "Invalid or unavailable upstream response",
        )
    }
    fn timeout() -> Self {
        Self::new(
            StatusCode::GATEWAY_TIMEOUT,
            "request_timeout",
            "Request deadline exceeded",
        )
    }
    fn cancelled() -> Self {
        Self::new(
            StatusCode::SERVICE_UNAVAILABLE,
            "request_cancelled",
            "Request cancelled",
        )
    }
    fn response(&self, kind: config::ProviderKind) -> HttpResponse<Body> {
        let payload = match kind {
            config::ProviderKind::Openai => {
                json!({"error":{"message":self.message,"type":self.code,"code":self.code,"param":null}})
            }
            config::ProviderKind::Anthropic => {
                json!({"type":"error","error":{"type":match self.status {
                StatusCode::UNAUTHORIZED=>"authentication_error", StatusCode::FORBIDDEN=>"permission_error", StatusCode::NOT_FOUND=>"not_found_error", StatusCode::TOO_MANY_REQUESTS=>"rate_limit_error", StatusCode::BAD_REQUEST|StatusCode::PAYLOAD_TOO_LARGE=>"invalid_request_error", _=>"api_error"
            },"message":self.message}})
            }
            config::ProviderKind::Gemini => {
                json!({"error":{"code":self.status.as_u16(),"message":self.message,"status":match self.status {
                    StatusCode::UNAUTHORIZED=>"UNAUTHENTICATED",StatusCode::FORBIDDEN=>"PERMISSION_DENIED",StatusCode::NOT_FOUND=>"NOT_FOUND",StatusCode::TOO_MANY_REQUESTS=>"RESOURCE_EXHAUSTED",StatusCode::BAD_REQUEST|StatusCode::PAYLOAD_TOO_LARGE=>"INVALID_ARGUMENT",StatusCode::GATEWAY_TIMEOUT=>"DEADLINE_EXCEEDED",_=>"UNAVAILABLE"
                }}})
            }
        };
        json_response(self.status, payload)
    }
}

fn json_response(status: StatusCode, payload: Value) -> HttpResponse<Body> {
    HttpResponse::builder()
        .status(status)
        .header("content-type", "application/json")
        .body(Body::from(payload.to_string()))
        .expect("static response headers")
}
