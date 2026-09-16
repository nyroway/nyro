//! Local admin HTTP and publication coordination; persistence lives in nyro-control.
use crate::bootstrap::Resources;
use axum::{
    Json, Router,
    body::{Body, to_bytes},
    extract::{Request, State, rejection::JsonRejection},
    http::{StatusCode, header},
    middleware::{self, Next},
    response::{IntoResponse, Response},
    routing::{get, post},
};
use nyro_config::Config;
use nyro_control::{
    Error, MAX_CONFIG_BYTES, Store,
    entity::{EntityChange, EntityKind},
};

use nyro_kernel::{Context, Host};
use nyro_llm::runtime::Runtime;
use nyro_security::ApiKeys;
use serde::Deserialize;
use serde_json::{Value, json};
use std::{
    sync::{Arc, Mutex},
    time::Duration,
};
use tokio::{
    sync::{Mutex as AsyncMutex, Semaphore},
    time::Instant,
};
use tokio_util::{sync::CancellationToken, task::TaskTracker};

mod entity;

struct Managed {
    store: Store,
    resources: Resources,
    active_revision: u64,
}

pub(crate) struct Control {
    managed: AsyncMutex<Managed>,
    host: Arc<Host<Runtime>>,
    keys: ApiKeys,
    slots: Arc<Semaphore>,
    accepting: Mutex<bool>,
    work: TaskTracker,
    stop: CancellationToken,
}

impl Control {
    pub(crate) fn new(
        store: Store,
        resources: Resources,
        host: Arc<Host<Runtime>>,
        active_revision: u64,
        keys: ApiKeys,
    ) -> Arc<Self> {
        Arc::new(Self {
            managed: AsyncMutex::new(Managed {
                store,
                resources,
                active_revision,
            }),
            host,
            keys,
            slots: Arc::new(Semaphore::new(16)),
            accepting: Mutex::new(true),
            work: TaskTracker::new(),
            stop: CancellationToken::new(),
        })
    }

    pub(crate) async fn shutdown(&self) {
        {
            // Serialize closing against spawning, so wait cannot miss an accepted operation.
            let mut accepting = self.accepting.lock().unwrap();
            *accepting = false;
            self.stop.cancel();
            self.work.close();
        }
        self.work.wait().await;
    }

    async fn execute(self: Arc<Self>, command: Command) -> Response {
        let mutating = !matches!(
            &command,
            Command::Read | Command::Export | Command::ReadEntities { .. }
        );
        let task = {
            let accepting = self.accepting.lock().unwrap();
            if !*accepting {
                return failure(StatusCode::SERVICE_UNAVAILABLE, "control_stopping");
            }
            let Ok(permit) = self.slots.clone().try_acquire_owned() else {
                return failure(StatusCode::SERVICE_UNAVAILABLE, "control_busy");
            };
            let control = self.clone();
            // Owned work completes even if the HTTP client disconnects or times out.
            self.work.spawn(async move {
                let _permit = permit;
                let mut state = control.managed.lock().await;
                if control.stop.is_cancelled() {
                    return failure(StatusCode::SERVICE_UNAVAILABLE, "control_stopping");
                }
                control.apply(&mut state, command).await
            })
        };
        match tokio::time::timeout(Duration::from_secs(10), task).await {
            Ok(Ok(response)) => response,
            Ok(Err(_)) => failure(StatusCode::INTERNAL_SERVER_ERROR, "control_failed"),
            Err(_) if mutating => success(StatusCode::ACCEPTED, json!({"operation":"pending"})),
            Err(_) => failure(StatusCode::SERVICE_UNAVAILABLE, "control_busy"),
        }
    }

    async fn apply(&self, managed: &mut Managed, command: Command) -> Response {
        let export = matches!(&command, Command::Export);
        match command {
            Command::Read | Command::Export => match managed.store.state().await {
                Ok(state) => success(
                    StatusCode::OK,
                    json!({
                        "draft":if export { json!(state.draft) } else { state.draft.redacted() },
                        "published_revision":state.published.revision,
                        "active_revision":managed.active_revision,
                        "publication":if state.published.revision == managed.active_revision {"active"} else {"pending"},
                    }),
                ),
                Err(error) => storage_error(error),
            },
            Command::Save(save) => match managed
                .store
                .save(save.expected_revision, &save.config)
                .await
            {
                Ok(snapshot) => {
                    success(StatusCode::OK, json!({"draft_revision":snapshot.revision}))
                }
                Err(error) => storage_error(error),
            },
            Command::ReadEntities { kind, id } => match managed.store.state().await {
                Ok(state) => match state.draft.entities(kind, id.as_deref()) {
                    Ok(view) => success(StatusCode::OK, view),
                    Err(error) => storage_error(error),
                },
                Err(error) => storage_error(error),
            },
            Command::EditEntity {
                expected_revision,
                change,
            } => {
                let status = if matches!(&change, EntityChange::Create { .. }) {
                    StatusCode::CREATED
                } else {
                    StatusCode::OK
                };
                match managed.store.edit(expected_revision, change).await {
                    Ok(revision) => success(status, json!({"draft_revision":revision})),
                    Err(error) => storage_error(error),
                }
            }
            Command::Publish(revision) => self.publish(managed, revision).await,
        }
    }

    async fn publish(&self, managed: &mut Managed, revision: u64) -> Response {
        let state = match managed.store.state().await {
            Ok(state) => state,
            Err(error) => return storage_error(error),
        };
        if state.draft.revision != revision {
            return storage_error(Error::Conflict);
        }
        let config = &state.draft.config;
        if managed.resources.check_settings(config).is_err() {
            return failure(StatusCode::CONFLICT, "restart_required");
        }
        let Ok(fingerprint) = config.fingerprint() else {
            return storage_error(Error::Invalid);
        };
        let unchanged =
            self.host.status().active.is_some_and(|active| {
                active.generation.fingerprint.as_deref() == Some(&fingerprint)
            });
        let candidate = if unchanged {
            None
        } else {
            match managed.resources.candidate(config) {
                Ok(mut candidate) => {
                    candidate.version = format!("control:{revision}");
                    Some(candidate)
                }
                Err(_) => return failure(StatusCode::UNPROCESSABLE_ENTITY, "candidate_rejected"),
            }
        };
        // Commit is the durable publication boundary. Nothing after it reports a rejection:
        // activation may be pending, and restart must recover this committed target.
        if let Err(error) = managed.store.publish(revision).await {
            return storage_error(error);
        }
        let activated = match candidate {
            Some(candidate) => self
                .host
                .activate(
                    candidate,
                    Context {
                        deadline: Instant::now() + Duration::from_secs(10),
                        cancellation: self.stop.child_token(),
                    },
                )
                .await
                .is_ok(),
            None => true,
        };
        if activated {
            managed.active_revision = revision;
        }
        let outcome = if activated { "active" } else { "pending" };
        tracing::info!(target:"nyro::control", revision, outcome, "Configuration publication finished");
        success(
            if activated {
                StatusCode::OK
            } else {
                StatusCode::ACCEPTED
            },
            json!({
                "published_revision":revision, "active_revision":managed.active_revision, "publication":outcome,
            }),
        )
    }
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Save {
    expected_revision: u64,
    config: Config,
}
#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Publish {
    revision: u64,
}
enum Command {
    Read,
    Export,
    ReadEntities {
        kind: EntityKind,
        id: Option<String>,
    },
    EditEntity {
        expected_revision: u64,
        change: EntityChange,
    },
    Save(Save),
    Publish(u64),
}

pub(crate) fn router(control: Arc<Control>) -> Router {
    Router::new()
        .merge(entity::routes())
        .route("/admin/config", get(read).put(save))
        .route("/admin/config/export", get(export))
        .route("/admin/config/publish", post(publish))
        .fallback(|| async { failure(StatusCode::NOT_FOUND, "not_found") })
        .route_layer(middleware::from_fn_with_state(
            control.clone(),
            authenticate,
        ))
        .with_state(control)
}

async fn authenticate(
    State(control): State<Arc<Control>>,
    request: Request,
    next: Next,
) -> Response {
    let headers = request.headers();
    let credential = headers
        .get(header::AUTHORIZATION)
        .and_then(|value| value.to_str().ok())
        .and_then(|value| value.split_once(' '))
        .filter(|(scheme, _)| scheme.eq_ignore_ascii_case("Bearer"))
        .map(|(_, secret)| secret);
    if headers.get_all(header::AUTHORIZATION).iter().count() != 1
        || headers.contains_key("x-api-key")
        || headers.contains_key("x-goog-api-key")
        || credential.is_none_or(|secret| control.keys.authenticate(secret).is_err())
    {
        return failure(StatusCode::UNAUTHORIZED, "authentication_failed");
    }
    if request.uri().query().is_some() {
        return failure(StatusCode::BAD_REQUEST, "query_not_supported");
    }
    let (parts, body) = request.into_parts();
    let bytes =
        match tokio::time::timeout(Duration::from_secs(15), to_bytes(body, MAX_CONFIG_BYTES)).await
        {
            Ok(Ok(bytes)) => bytes,
            Ok(Err(_)) => return failure(StatusCode::PAYLOAD_TOO_LARGE, "invalid_request"),
            Err(_) => return failure(StatusCode::REQUEST_TIMEOUT, "request_timeout"),
        };
    let mut response = next
        .run(Request::from_parts(parts, Body::from(bytes)))
        .await;
    response.headers_mut().insert(
        header::CACHE_CONTROL,
        header::HeaderValue::from_static("no-store"),
    );
    response
}
async fn read(State(control): State<Arc<Control>>) -> Response {
    control.execute(Command::Read).await
}
async fn export(State(control): State<Arc<Control>>) -> Response {
    control.execute(Command::Export).await
}
async fn save(
    State(control): State<Arc<Control>>,
    body: Result<Json<Save>, JsonRejection>,
) -> Response {
    match body {
        Ok(Json(save)) => control.execute(Command::Save(save)).await,
        Err(error) => failure(error.status(), "invalid_request"),
    }
}
async fn publish(
    State(control): State<Arc<Control>>,
    body: Result<Json<Publish>, JsonRejection>,
) -> Response {
    match body {
        Ok(Json(publish)) => control.execute(Command::Publish(publish.revision)).await,
        Err(error) => failure(error.status(), "invalid_request"),
    }
}
fn storage_error(error: Error) -> Response {
    match error {
        Error::Invalid => failure(StatusCode::UNPROCESSABLE_ENTITY, "invalid_config"),
        Error::Conflict => failure(StatusCode::CONFLICT, "revision_conflict"),
        Error::AlreadyExists => failure(StatusCode::CONFLICT, "entity_exists"),
        Error::Referenced => failure(StatusCode::CONFLICT, "entity_referenced"),
        Error::NotFound => failure(StatusCode::NOT_FOUND, "not_found"),
        _ => failure(StatusCode::SERVICE_UNAVAILABLE, "storage_failed"),
    }
}
fn success(status: StatusCode, value: Value) -> Response {
    (status, [(header::CACHE_CONTROL, "no-store")], Json(value)).into_response()
}
fn failure(status: StatusCode, code: &'static str) -> Response {
    success(status, json!({"error":{"code":code}}))
}

#[cfg(test)]
mod tests;
