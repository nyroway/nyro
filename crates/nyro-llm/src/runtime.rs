//! Trusted LLM execution with bounded failover. Business policy stays outside the kernel.

use std::{collections::BTreeMap, sync::Arc, time::Duration};

use axum::{
    body::Body,
    http::{HeaderMap, Method, Request as HttpRequest, Response as HttpResponse, StatusCode},
};
use futures::StreamExt;
use nyro_limit::{ConcurrencyLimit, Permit};
use nyro_security::{ApiKeys, Authorizer, Grant, Identity};
use serde_json::{Value, json};
use tokio::time::Instant;
use tokio_util::sync::CancellationToken;

use crate::{
    Request, Workload,
    codec::ChatFormat,
    config,
    health::{BackendHealth, HealthRegistry},
    ingress::body::{self, Outcome},
    observation::{self, RequestObservation},
    provider::Driver,
    quota::{BoundQuota, QuotaRegistry},
    rate::{BoundRate, RateRegistry},
    router,
};

mod endpoint;
mod native;
use native::Input;
mod response;
mod stream;

/// Application-owned state shared by immutable runtime generations.
#[derive(Clone, Default)]
pub struct SharedResources {
    pub health: Arc<HealthRegistry>,
    pub rates: Arc<RateRegistry>,
    pub quotas: Arc<QuotaRegistry>,
}

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
    rates: BTreeMap<String, Arc<BoundRate>>,
    quotas: BTreeMap<String, Arc<BoundQuota>>,
}

impl Runtime {
    pub fn new(
        config: config::Config,
        keys: Arc<ApiKeys>,
        limit: ConcurrencyLimit,
        options: Options,
    ) -> Result<Self, BuildError> {
        Self::with_resources(config, keys, limit, options, SharedResources::default())
    }

    /// Share this registry between generations to retain health for unchanged backends.
    pub fn with_health(
        config: config::Config,
        keys: Arc<ApiKeys>,
        limit: ConcurrencyLimit,
        options: Options,
        health: Arc<HealthRegistry>,
    ) -> Result<Self, BuildError> {
        Self::with_resources(
            config,
            keys,
            limit,
            options,
            SharedResources {
                health,
                ..SharedResources::default()
            },
        )
    }

    /// Reuse registries across generations; changing retained limit rules requires new resources.
    pub fn with_resources(
        config: config::Config,
        keys: Arc<ApiKeys>,
        limit: ConcurrencyLimit,
        options: Options,
        resources: SharedResources,
    ) -> Result<Self, BuildError> {
        let SharedResources {
            health,
            rates,
            quotas,
        } = resources;
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
        let rates = config
            .models
            .iter()
            .filter_map(|(id, model)| {
                model
                    .rate
                    .as_ref()
                    .map(|policy| rates.bind(id, policy).map(|rate| (id.clone(), rate)))
            })
            .collect::<Result<_, _>>()?;
        let quotas = config
            .models
            .iter()
            .filter_map(|(id, model)| {
                model
                    .quota
                    .as_ref()
                    .map(|policy| quotas.bind(id, policy).map(|quota| (id.clone(), quota)))
            })
            .collect::<Result<_, _>>()?;
        Ok(Self {
            quotas,
            rates,
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
            observation: RequestObservation::new(started, deadline, cancellation.clone()),
            permit: None,
        };
        let result = tokio::select! {
            biased;
            _ = cancellation.cancelled() => Err(Failure::cancelled()),
            _ = tokio::time::sleep_until(deadline) => Err(Failure::timeout()),
            result = self.execute(request, &mut exchange, &cancellation, deadline) => result,
        };
        let (mut response, body_deadline) = match result {
            Ok(response) => (response, deadline),
            // Error delivery is a separate, bounded terminal action, so a 504 body can be read.
            Err(failure) => {
                exchange.observation.error_code = failure.code;
                (
                    failure.response(format),
                    Instant::now() + Duration::from_secs(5),
                )
            }
        };
        exchange.observation.status = response.status().as_u16();
        response.headers_mut().insert(
            "x-request-id",
            exchange
                .observation
                .id
                .parse()
                .expect("generated request ID"),
        );
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
        request: &Input,
        downstream: ChatFormat,
    ) -> Result<Vec<Prepared<'a>>, Failure> {
        let mut eligible = Vec::new();
        for backend in model.backends.iter().filter(|backend| backend.weight > 0) {
            let provider = self
                .providers
                .get(&backend.provider)
                .ok_or_else(Failure::upstream)?;
            let encoded = (|| {
                if let Input::Native(request) = request
                    && provider.native_chat
                    && request.format == provider.format
                {
                    return Some(request.encode(&backend.upstream_model));
                }
                let mut request = request.typed()?;
                request.set_model(backend.upstream_model.clone());
                if provider.format == ChatFormat::OpenAiChat
                    && let Request::Chat(chat) = &mut request
                {
                    if downstream == ChatFormat::OpenAiResponses {
                        chat.openai.store = Some(false);
                    }
                    if chat.stream == Some(true) {
                        chat.openai
                            .stream_options
                            .get_or_insert(nyro_protocol::openai::chat::StreamOptions {
                                include_usage: None,
                                include_obfuscation: None,
                            })
                            .include_usage = Some(true);
                    }
                }
                provider.encode(&request).ok()
            })();
            if let Some(encoded) = encoded {
                eligible.push(Prepared {
                    backend,
                    provider,
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

    fn authenticate(
        &self,
        kind: config::ProviderKind,
        headers: &HeaderMap,
    ) -> Result<Option<Identity>, Failure> {
        endpoint::credential(kind, headers)?
            .map(|secret| {
                self.keys
                    .authenticate(secret)
                    .map_err(|_| Failure::unauthorized())
            })
            .transpose()
    }

    fn list_models(&self, request: &HttpRequest<Body>) -> Result<HttpResponse<Body>, Failure> {
        if request.method() != Method::GET {
            return Err(Failure::new(
                StatusCode::METHOD_NOT_ALLOWED,
                "method_not_allowed",
                "GET is required",
            ));
        }
        endpoint::validate_query(request.uri(), config::ProviderKind::Openai, false)?;
        let identity = self.authenticate(config::ProviderKind::Openai, request.headers())?;
        // The generation's ordered model map is the catalog; no upstream discovery or admission.
        let data: Vec<_> = self
            .models
            .iter()
            .filter(|(id, model)| {
                model.allow_anonymous
                    || identity.as_ref().is_some_and(|identity| {
                        self.authorizer.authorize(identity, "invoke", id).is_ok()
                    })
            })
            .map(|(id, _)| json!({"id": id, "object": "model", "created": 0, "owned_by": "Nyro"}))
            .collect();
        let mut response = json_response(StatusCode::OK, json!({"object": "list", "data": data}));
        // This response depends on credentials and must not survive configuration changes in caches.
        response
            .headers_mut()
            .insert("cache-control", "no-store".parse().expect("static header"));
        Ok(response)
    }

    async fn execute(
        &self,
        request: HttpRequest<Body>,
        exchange: &mut Exchange,
        cancellation: &CancellationToken,
        deadline: Instant,
    ) -> Result<HttpResponse<Body>, Failure> {
        if request.uri().path() == "/v1/models" {
            exchange.observation.protocol = "openai_models";
            exchange.observation.workload = "none";
            return self.list_models(&request);
        }
        let endpoint = endpoint::Endpoint::parse(request.uri(), request.method())?;
        let workload = endpoint.workload;
        exchange.observation.protocol = observation::protocol(endpoint.format, workload);
        exchange.observation.workload = match workload {
            Workload::Chat => "chat",
            Workload::Embedding => "embedding",
        };
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
        let native = workload == Workload::Chat
            && matches!(
                endpoint.format,
                ChatFormat::OpenAiChat | ChatFormat::Anthropic
            )
            && value
                .get("model")
                .and_then(Value::as_str)
                .and_then(|id| self.models.get(id))
                .is_some_and(|model| {
                    model.backends.iter().any(|backend| {
                        backend.weight > 0
                            && self.providers[&backend.provider].native_chat
                            && self.providers[&backend.provider].format == endpoint.format
                    })
                });
        let request = if native {
            Input::Native(native::NativeRequest::parse(value, endpoint.format)?)
        } else {
            Input::Typed(endpoint.decode(value)?)
        };
        let public_model = request.model().to_owned();
        exchange.observation.streaming = request.is_streaming();
        // Resolve -> Authenticate -> Authorize -> Admit are mandatory ordinary runtime steps.
        let model = self.models.get(&public_model).ok_or_else(|| {
            Failure::new(StatusCode::NOT_FOUND, "model_not_found", "Unknown model")
        })?;
        exchange.observation.model = public_model.clone();
        if !model.workloads.contains(&workload) {
            return Err(Failure::invalid("Model does not support this workload"));
        }
        match self.authenticate(endpoint.kind, &parts.headers)? {
            Some(identity) => {
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
        if cancellation.is_cancelled() {
            return Err(Failure::cancelled());
        }
        if Instant::now() >= deadline {
            return Err(Failure::timeout());
        }
        if let Some(rate) = self.rates.get(&public_model)
            && let Err(exceeded) = rate.try_acquire()
        {
            // Rate admission rejects synchronously; its error body must not occupy an in-flight slot.
            drop(exchange.permit.take());
            return Err(Failure::rate(exceeded.retry_after));
        }
        // One logical admission; retries, failed upstreams and cancellation never refund it.
        let mut last_failure = None;
        while exchange.observation.attempts < model.max_attempts {
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
            let quota = match self.quotas.get(&public_model) {
                Some(bound) => match bound.reserve() {
                    Ok(reservation) => Some(reservation),
                    Err(_) => {
                        drop(exchange.permit.take());
                        return Err(Failure::new(
                            StatusCode::TOO_MANY_REQUESTS,
                            "quota_exceeded",
                            "Token quota cannot admit another attempt",
                        ));
                    }
                },
                None => None,
            };
            let mut attempt = Some(exchange.observation.attempt(
                &selected.backend.id,
                &selected.backend.provider,
                observation::protocol(selected.provider.format, workload),
                quota,
            ));
            let response = match selected
                .provider
                .send(
                    workload,
                    &selected.backend.upstream_model,
                    request.is_streaming(),
                    &selected.encoded,
                )
                .await
            {
                Ok(response) => response,
                Err(error) => {
                    if let Some(health) = health.take() {
                        health.failure();
                    }
                    // Only connection establishment is known to precede sending the request.
                    let connect = error.is_connect();
                    attempt.as_mut().unwrap().fail(if connect {
                        "connect_error"
                    } else {
                        "transport_error"
                    });
                    if !connect {
                        return Err(Failure::upstream());
                    }
                    last_failure = Some(Failure::upstream());
                    continue;
                }
            };
            attempt.as_mut().unwrap().status = response.status().as_u16();
            if !response.status().is_success() {
                attempt.as_mut().unwrap().fail("http_error");
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
                    &mut attempt,
                )
                .await;
            if result.is_err()
                && let Some(attempt) = attempt.as_mut()
            {
                attempt.fail("protocol_error");
            }
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
    encoded: Value,
    health: Option<&'a Arc<BackendHealth>>,
}

struct Exchange {
    observation: RequestObservation,
    permit: Option<Permit>,
}

impl Exchange {
    fn finish(mut self, outcome: Outcome) {
        self.observation.delivery = Some(outcome);
    }
}

#[derive(Debug, thiserror::Error)]
#[error("{message}")]
struct Failure {
    status: StatusCode,
    code: &'static str,
    message: &'static str,
    retry_after: Option<Duration>,
}

impl Failure {
    fn new(status: StatusCode, code: &'static str, message: &'static str) -> Self {
        Self {
            status,
            code,
            message,
            retry_after: None,
        }
    }
    fn rate(retry_after: Duration) -> Self {
        Self {
            retry_after: Some(retry_after),
            ..Self::new(
                StatusCode::TOO_MANY_REQUESTS,
                "rate_limit_exceeded",
                "Request rate limit reached",
            )
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
        let mut response = json_response(self.status, payload);
        if let Some(retry_after) = self.retry_after {
            let seconds = retry_after
                .as_secs()
                .saturating_add(u64::from(retry_after.subsec_nanos() > 0))
                .max(1);
            response
                .headers_mut()
                .insert(axum::http::header::RETRY_AFTER, seconds.into());
        }
        response
    }
}

fn json_response(status: StatusCode, payload: Value) -> HttpResponse<Body> {
    HttpResponse::builder()
        .status(status)
        .header("content-type", "application/json")
        .body(Body::from(payload.to_string()))
        .expect("static response headers")
}
