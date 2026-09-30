use crate::gateway::GatewayRuntime;
use axum::{
    Router,
    extract::{Request, State},
    http::StatusCode,
    response::{IntoResponse, Response},
    routing::get,
};
use nyro_kernel::Host;
use std::{sync::Arc, time::Duration};
use tokio::time::Instant;
use tokio_util::task::TaskTracker;

mod body;

#[derive(Clone)]
struct App {
    host: Arc<Host<GatewayRuntime>>,
    tracker: TaskTracker,
}

pub(crate) fn router(host: Arc<Host<GatewayRuntime>>, tracker: TaskTracker) -> Router {
    Router::new()
        .route("/healthz", get(|| async { StatusCode::OK }))
        .route(
            "/readyz",
            get(|State(app): State<App>| async move {
                if app.host.status().accepting {
                    StatusCode::OK
                } else {
                    StatusCode::SERVICE_UNAVAILABLE
                }
            }),
        )
        .fallback(dispatch)
        .with_state(App { host, tracker })
}

async fn dispatch(State(app): State<App>, request: Request) -> Response {
    let lease = match app.host.acquire() {
        Ok(lease) => lease,
        Err(_) => return (StatusCode::SERVICE_UNAVAILABLE, axum::Json(serde_json::json!({"error":{"type":"unavailable","message":"Gateway is not ready"}}))).into_response(),
    };
    let deadline = Instant::now() + lease.value().request_timeout(request.uri().path());
    let response = lease.value().handle(request, lease.cancellation()).await;
    let deadline = if response.status().is_success() {
        deadline
    } else {
        Instant::now() + Duration::from_secs(5)
    };
    let (parts, source) = response.into_parts();
    Response::from_parts(parts, body::retain(source, lease, deadline, &app.tracker))
}

#[cfg(test)]
mod tests;
