//! Trusted single-attempt LLM execution. Business policy stays outside the kernel.

use std::{
    collections::{BTreeMap, VecDeque},
    sync::Arc,
    time::Duration,
};

use axum::{
    body::Body,
    http::{HeaderMap, Method, Request as HttpRequest, Response as HttpResponse, StatusCode},
};
use bytes::Bytes;
use futures::{
    StreamExt,
    stream::{self, BoxStream},
};
use nyro_limit::{ConcurrencyLimit, Permit};
use nyro_protocol::framing::{Decoder, Event};
use nyro_security::{ApiKeys, Authorizer, Grant};
use serde_json::{Value, json};
use tokio::time::Instant;
use tokio_util::sync::CancellationToken;

use crate::{
    Request, Workload,
    codec::openai,
    config,
    ingress::body::{self, Outcome},
    provider::OpenAi,
};

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
    providers: BTreeMap<String, OpenAi>,
    keys: Arc<ApiKeys>,
    authorizer: Authorizer,
    limit: ConcurrencyLimit,
    options: Options,
}

impl Runtime {
    pub fn new(
        config: config::Config,
        keys: Arc<ApiKeys>,
        limit: ConcurrencyLimit,
        options: Options,
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
            .map(|(id, provider)| OpenAi::new(provider).map(|driver| (id.clone(), driver)))
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
        Ok(Self {
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
        let started = Instant::now();
        let deadline = started + self.options.request_timeout;
        let mut exchange = Exchange {
            started,
            model: None,
            permit: None,
            status: None,
            outcome: Outcome::Cancelled,
        };
        let result = tokio::select! {
            biased;
            _ = cancellation.cancelled() => Err(Failure::cancelled()),
            _ = tokio::time::sleep_until(deadline) => Err(Failure::timeout()),
            result = self.execute(request, &mut exchange) => result,
        };
        let (response, body_deadline) = match result {
            Ok(response) => (response, deadline),
            // Error delivery is a separate, bounded terminal action, so a 504 body can be read.
            Err(failure) => (failure.response(), Instant::now() + Duration::from_secs(5)),
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

    async fn execute(
        &self,
        request: HttpRequest<Body>,
        exchange: &mut Exchange,
    ) -> Result<HttpResponse<Body>, Failure> {
        let workload = match request.uri().path() {
            "/v1/chat/completions" => Workload::Chat,
            "/v1/embeddings" => Workload::Embedding,
            _ => {
                return Err(Failure::new(
                    StatusCode::NOT_FOUND,
                    "not_found",
                    "Unknown endpoint",
                ));
            }
        };
        if request.method() != Method::POST {
            return Err(Failure::new(
                StatusCode::METHOD_NOT_ALLOWED,
                "method_not_allowed",
                "POST is required",
            ));
        }
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
        let mut request = match workload {
            Workload::Chat => Request::Chat(
                openai::decode_chat(value)
                    .map_err(|_| Failure::invalid("Invalid or unsupported Chat request"))?,
            ),
            Workload::Embedding => Request::Embedding(
                openai::decode_embedding(value)
                    .map_err(|_| Failure::invalid("Invalid or unsupported Embedding request"))?,
            ),
        };
        let public_model = request.model().to_owned();
        exchange.model = Some(public_model.clone());
        // Resolve -> Authenticate -> Authorize -> Admit are mandatory ordinary runtime steps.
        let model = self.models.get(&public_model).ok_or_else(|| {
            Failure::new(StatusCode::NOT_FOUND, "model_not_found", "Unknown model")
        })?;
        if !model.workloads.contains(&workload) {
            return Err(Failure::invalid("Model does not support this workload"));
        }
        match credential(&parts.headers)? {
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
        let streaming = request.is_streaming();
        request.set_model(model.upstream_model.clone());
        let encoded = match &request {
            Request::Chat(chat) => openai::encode_chat(chat),
            Request::Embedding(embedding) => openai::encode_embedding(embedding),
        }
        .map_err(|_| Failure::invalid("Cannot encode request"))?;
        let provider = self
            .providers
            .get(&model.provider)
            .ok_or_else(Failure::upstream)?;
        let response = provider
            .send(workload, &encoded)
            .await
            .map_err(|_| Failure::upstream())?;
        if !response.status().is_success() {
            let status = response.status();
            let status = if status.is_redirection()
                || status == StatusCode::UNAUTHORIZED
                || status == StatusCode::FORBIDDEN
            {
                StatusCode::BAD_GATEWAY
            } else {
                status
            };
            return Err(Failure::new(
                status,
                "upstream_error",
                "Upstream rejected the request",
            ));
        }
        if streaming {
            let is_sse = response
                .headers()
                .get("content-type")
                .and_then(|v| v.to_str().ok())
                .is_some_and(|v| {
                    v.split(';')
                        .next()
                        .is_some_and(|v| v.trim().eq_ignore_ascii_case("text/event-stream"))
                });
            if !is_sse {
                return Err(Failure::upstream());
            }
            let mut state = StreamState {
                input: response.bytes_stream().boxed(),
                pending: Bytes::new(),
                decoder: Decoder::new(self.options.max_frame_bytes),
                events: VecDeque::new(),
                done: false,
                eof: false,
                public_model,
            };
            // Validate one complete frame before handing the response to HTTP. This is not a flush acknowledgement.
            let first = state.next_frame().await?.ok_or_else(Failure::upstream)?;
            let output = stream::once(async { Ok::<_, Failure>(first) }).chain(stream::try_unfold(
                state,
                |mut state| async move {
                    state
                        .next_frame()
                        .await
                        .map(|frame| frame.map(|frame| (frame, state)))
                },
            ));
            return Ok(HttpResponse::builder()
                .header("content-type", "text/event-stream")
                .header("cache-control", "no-cache")
                .body(Body::from_stream(output))
                .expect("static response headers"));
        }
        let mut input = response.bytes_stream();
        let mut bytes = Vec::new();
        while let Some(chunk) = input.next().await {
            let chunk = chunk.map_err(|_| Failure::upstream())?;
            if chunk.len() > self.options.max_response_bytes.saturating_sub(bytes.len()) {
                return Err(Failure::upstream());
            }
            bytes.extend_from_slice(&chunk);
        }
        let payload: Value = serde_json::from_slice(&bytes).map_err(|_| Failure::upstream())?;
        let payload = match workload {
            Workload::Chat => {
                let mut response =
                    openai::decode_chat_response(payload).map_err(|_| Failure::upstream())?;
                response.model = public_model;
                openai::encode_chat_response(&response)
            }
            Workload::Embedding => {
                let mut response =
                    openai::decode_embedding_response(payload).map_err(|_| Failure::upstream())?;
                response.model = public_model;
                openai::encode_embedding_response(&response)
            }
        }
        .map_err(|_| Failure::upstream())?;
        Ok(json_response(StatusCode::OK, payload))
    }
}

fn credential(headers: &HeaderMap) -> Result<Option<&str>, Failure> {
    let mut values = headers.get_all("authorization").iter();
    let Some(value) = values.next() else {
        return Ok(None);
    };
    if values.next().is_some() {
        return Err(Failure::unauthorized());
    }
    let value = value.to_str().map_err(|_| Failure::unauthorized())?;
    let (scheme, secret) = value.split_once(' ').ok_or_else(Failure::unauthorized)?;
    if !scheme.eq_ignore_ascii_case("bearer")
        || secret.is_empty()
        || secret.bytes().any(|b| b.is_ascii_whitespace())
    {
        return Err(Failure::unauthorized());
    }
    Ok(Some(secret))
}

struct Exchange {
    started: Instant,
    model: Option<String>,
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
        tracing::info!(target: "nyro::request", model = self.model.as_deref().unwrap_or(""), status = self.status.map_or(0, |status| status.as_u16()),
            duration_ms = self.started.elapsed().as_millis() as u64, outcome = ?self.outcome, "LLM request finished");
        // This slice owns only synchronous finalization; the permit releases after observation.
    }
}

struct StreamState {
    input: BoxStream<'static, Result<Bytes, reqwest::Error>>,
    pending: Bytes,
    decoder: Decoder,
    events: VecDeque<Event>,
    done: bool,
    eof: bool,
    public_model: String,
}

impl StreamState {
    async fn next_frame(&mut self) -> Result<Option<String>, Failure> {
        loop {
            if self.done {
                return Ok(None);
            }
            if let Some(event) = self.events.pop_front() {
                let event =
                    openai::decode_chat_event(&event.data).map_err(|_| Failure::upstream())?;
                self.done = event.is_done();
                return openai::encode_chat_event(&event, &self.public_model)
                    .map(Some)
                    .map_err(|_| Failure::upstream());
            }
            if self.eof {
                return Err(Failure::upstream());
            }
            if !self.pending.is_empty() {
                let bytes = self.pending.split_to(self.pending.len().min(8192));
                self.events
                    .extend(self.decoder.push(&bytes).map_err(|_| Failure::upstream())?);
                continue;
            }
            match self.input.next().await {
                Some(Ok(bytes)) => self.pending = bytes,
                Some(Err(_)) => return Err(Failure::upstream()),
                None => {
                    self.eof = true;
                    self.events
                        .extend(self.decoder.finish().map_err(|_| Failure::upstream())?);
                }
            }
        }
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
    fn response(&self) -> HttpResponse<Body> {
        json_response(
            self.status,
            json!({"error":{"message":self.message,"type":self.code,"code":self.code,"param":null}}),
        )
    }
}

fn json_response(status: StatusCode, payload: Value) -> HttpResponse<Body> {
    HttpResponse::builder()
        .status(status)
        .header("content-type", "application/json")
        .body(Body::from(payload.to_string()))
        .expect("static response headers")
}
