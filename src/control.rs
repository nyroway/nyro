//! Admin transport and owned mutation tasks. Durable resources live in nyro-control.
use axum::{
    Json, Router,
    body::to_bytes,
    extract::{Path, Request, State},
    http::{Method, StatusCode},
    middleware::{self, Next},
    response::{IntoResponse, Response},
    routing::get,
};
use nyro_config::compile::Snapshot;
use nyro_control::{
    Error,
    resource::{self, Kind, Store},
};
use serde_json::{Value, json};
use std::sync::{Arc, Mutex};
use tokio::sync::{Mutex as AsyncMutex, Semaphore};
use tokio_util::task::TaskTracker;

struct Managed {
    store: Store,
    snapshot: Snapshot,
}
pub(crate) struct Control {
    managed: AsyncMutex<Managed>,
    pub(crate) hub: nyro_sync::Hub<Snapshot>,
    keys: nyro_authn::KeyAuth,
    work: TaskTracker,
    accepting: Mutex<bool>,
    slots: Arc<Semaphore>,
}
impl Control {
    pub(crate) fn new(
        store: Store,
        snapshot: Snapshot,
        keys: nyro_authn::KeyAuth,
    ) -> anyhow::Result<Arc<Self>> {
        Ok(Arc::new(Self {
            hub: nyro_sync::Hub::new(snapshot.clone())?,
            managed: AsyncMutex::new(Managed { store, snapshot }),
            keys,
            work: TaskTracker::new(),
            accepting: Mutex::new(true),
            slots: Arc::new(Semaphore::new(16)),
        }))
    }
    pub(crate) async fn shutdown(&self) {
        {
            let mut accepting = self.accepting.lock().unwrap();
            *accepting = false;
            self.work.close();
        }
        self.work.wait().await;
        self.hub.close();
    }
    async fn mutate(
        self: Arc<Self>,
        kind: Kind,
        id: Option<String>,
        value: Option<Value>,
    ) -> Response {
        let task = {
            let accepting = self.accepting.lock().unwrap();
            if !*accepting {
                return failure(StatusCode::SERVICE_UNAVAILABLE, "control_stopping");
            }
            let Ok(permit) = self.slots.clone().try_acquire_owned() else {
                return failure(StatusCode::SERVICE_UNAVAILABLE, "control_busy");
            };
            let control = self.clone();
            self.work.spawn(async move {
                let _permit = permit;
                let mut state = control.managed.lock().await;
                let candidate = match resource::edit(&state.snapshot, kind, id.as_deref(), value) { Ok(c) => c, Err(e) => return error(e) };
                if let Err(e) = state.store.save(&candidate).await { return error(e); }
                // A disconnected caller cannot interrupt this commit-to-publication sequence.
                state.snapshot = candidate.clone();
                match control.hub.publish(candidate) {
                    Ok(snapshot) => Json(json!({"saved": true, "version": snapshot.version, "fingerprint": snapshot.fingerprint})).into_response(),
                    Err(_) => failure(StatusCode::INTERNAL_SERVER_ERROR, "publication_failed"),
                }
            })
        };
        match task.await {
            Ok(response) => response,
            Err(_) => failure(StatusCode::INTERNAL_SERVER_ERROR, "control_failed"),
        }
    }
}
fn failure(status: StatusCode, code: &str) -> Response {
    (status, Json(json!({"error":{"code":code}}))).into_response()
}
fn error(error: Error) -> Response {
    let (status, code) = match error {
        Error::NotFound => (StatusCode::NOT_FOUND, "not_found"),
        Error::AlreadyExists | Error::Referenced => (StatusCode::CONFLICT, "resource_conflict"),
        Error::Invalid => (StatusCode::BAD_REQUEST, "invalid_resource"),
        _ => (StatusCode::SERVICE_UNAVAILABLE, "storage_unavailable"),
    };
    failure(status, code)
}
async fn auth(State(control): State<Arc<Control>>, request: Request, next: Next) -> Response {
    let mut values = request.headers().get_all("authorization").iter();
    let valid = values
        .next()
        .and_then(|h| h.to_str().ok())
        .and_then(|h| h.split_once(' '))
        .filter(|(scheme, _)| scheme.eq_ignore_ascii_case("bearer"))
        .is_some_and(|(_, key)| control.keys.authenticate(key).is_ok())
        && values.next().is_none();
    if !valid {
        return failure(StatusCode::UNAUTHORIZED, "unauthorized");
    }
    let mut response = next.run(request).await;
    response
        .headers_mut()
        .insert("cache-control", "no-store".parse().unwrap());
    response
}
pub(crate) fn router(control: Arc<Control>) -> Router {
    Router::new()
        .route("/v1/resources", get(read_all))
        .route("/v1/resources/:kind", get(collection).post(collection))
        .route("/v1/resources/:kind/:id", get(item).put(item).delete(item))
        .route("/v1/nodes/:id", get(node))
        .layer(middleware::from_fn_with_state(control.clone(), auth))
        .with_state(control)
}
async fn read_all(State(control): State<Arc<Control>>) -> Response {
    Json(resource::redacted(
        &control.managed.lock().await.snapshot.resources,
    ))
    .into_response()
}
async fn node(State(control): State<Arc<Control>>, Path(id): Path<String>) -> Response {
    match control.hub.node(&id) {
        Some(status) => Json(status).into_response(),
        None => failure(StatusCode::NOT_FOUND, "node_not_found"),
    }
}
async fn collection(
    State(control): State<Arc<Control>>,
    Path(kind): Path<String>,
    request: Request,
) -> Response {
    resource_request(control, kind, None, request).await
}
async fn item(
    State(control): State<Arc<Control>>,
    Path((kind, id)): Path<(String, String)>,
    request: Request,
) -> Response {
    resource_request(control, kind, Some(id), request).await
}
async fn resource_request(
    control: Arc<Control>,
    kind: String,
    id: Option<String>,
    request: Request,
) -> Response {
    let kind = match Kind::parse(&kind) {
        Ok(kind) => kind,
        Err(e) => return error(e),
    };
    match *request.method() {
        Method::GET => {
            let view = resource::redacted(&control.managed.lock().await.snapshot.resources);
            let items = &view[kind.name()];
            match id {
                None => Json(items.clone()).into_response(),
                Some(id) => match items.as_array().unwrap().iter().find(|v| v["id"] == id) {
                    Some(item) => Json(item.clone()).into_response(),
                    None => error(Error::NotFound),
                },
            }
        }
        Method::DELETE => control.mutate(kind, id, None).await,
        _ => {
            if !request
                .headers()
                .get("content-type")
                .and_then(|v| v.to_str().ok())
                .is_some_and(|s| {
                    s.split(';')
                        .next()
                        .is_some_and(|s| s.trim() == "application/json")
                })
            {
                return failure(StatusCode::UNSUPPORTED_MEDIA_TYPE, "json_required");
            }
            let bytes = match to_bytes(request.into_body(), nyro_control::MAX_CONFIG_BYTES).await {
                Ok(bytes) => bytes,
                Err(_) => return failure(StatusCode::PAYLOAD_TOO_LARGE, "body_too_large"),
            };
            let value = match serde_json::from_slice(&bytes) {
                Ok(value) => value,
                Err(_) => return error(Error::Invalid),
            };
            control.mutate(kind, id, Some(value)).await
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use axum::body::Body;
    use tower::ServiceExt;

    async fn fixture(path: &std::path::Path) -> Arc<Control> {
        let mut store = Store::open(path).await.unwrap();
        let snapshot = store.snapshot().await.unwrap();
        let keys = nyro_authn::KeyAuth::new(vec![nyro_authn::KeyCredential {
            id: "admin".into(),
            secret: "admin-test-key".into(),
            enabled: true,
            expires_at: None,
        }])
        .unwrap();
        Control::new(store, snapshot, keys).unwrap()
    }
    #[tokio::test]
    async fn disconnected_caller_cannot_cancel_an_accepted_write_or_publication() {
        let directory = tempfile::tempdir().unwrap();
        let path = directory.path().join("control.db");
        let control = fixture(&path).await;
        let guard = control.managed.lock().await;
        let caller = control.clone();
        let task = tokio::spawn(async move {
            caller
                .mutate(Kind::Consumers, None, Some(json!({"id":"saved"})))
                .await
        });
        tokio::time::timeout(std::time::Duration::from_secs(1), async {
            while control.work.is_empty() {
                tokio::task::yield_now().await;
            }
        })
        .await
        .unwrap();
        task.abort();
        let _ = task.await;
        drop(guard);
        control.shutdown().await;
        assert_eq!(
            control.hub.current().config.resources.consumers[0].id,
            "saved"
        );
        drop(control);
        let mut reopened = Store::open(&path).await.unwrap();
        assert_eq!(
            reopened.snapshot().await.unwrap().resources.consumers[0].id,
            "saved"
        );
        reopened.close().await.unwrap();
    }
    #[tokio::test]
    async fn oversized_admin_body_never_mutates_or_publishes() {
        let directory = tempfile::tempdir().unwrap();
        let control = fixture(&directory.path().join("control.db")).await;
        let version = control.hub.current().version.clone();
        let request = Request::builder()
            .method("POST")
            .uri("/v1/resources/consumers")
            .header("authorization", "Bearer admin-test-key")
            .header("content-type", "application/json")
            .body(Body::from("x".repeat(nyro_control::MAX_CONFIG_BYTES + 1)))
            .unwrap();
        assert_eq!(
            router(control.clone())
                .oneshot(request)
                .await
                .unwrap()
                .status(),
            StatusCode::PAYLOAD_TOO_LARGE
        );
        assert_eq!(control.hub.current().version, version);
        assert!(
            control
                .managed
                .lock()
                .await
                .snapshot
                .resources
                .consumers
                .is_empty()
        );
        control.shutdown().await;
    }
}
