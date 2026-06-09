use axum::Router;
use axum::extract::{DefaultBodyLimit, State};
use axum::http::{HeaderValue, Method, StatusCode, header};
use axum::middleware;
use axum::response::IntoResponse;
use axum::routing::{get, post};
use tower_http::cors::{AllowOrigin, CorsLayer};
use tower_http::trace::TraceLayer;

use super::context::inject_context;
use super::handler;
use super::ingress;
use crate::Gateway;

// Multimodal Gemini/OpenAI-compatible requests commonly carry base64 media in JSON.
const PROXY_JSON_BODY_LIMIT_BYTES: usize = 100 * 1024 * 1024;

pub fn create_router(gateway: Gateway) -> Router {
    let router = Router::new()
        .route(
            "/v1/chat/completions",
            post(ingress::openai_compatible::chat_completions::handler),
        )
        .route(
            "/v1/responses",
            post(ingress::openai_responses::responses::handler),
        )
        .route(
            "/v1/messages",
            post(ingress::anthropic_messages::messages::handler),
        )
        .route(
            "/v1/embeddings",
            post(ingress::openai_compatible::embeddings::handler),
        )
        .route(
            "/v1beta/models/:model_action",
            post(ingress::google_generative::generate_content::handler),
        )
        .route("/v1/models", get(handler::models_list))
        .route("/health", get(health))
        .route("/healthz", get(health))
        .route("/readyz", get(readyz))
        .route("/", get(health));

    let cors = build_proxy_cors_layer(
        &gateway.config.proxy_cors_origins,
        gateway.config.proxy_port,
    );

    router
        .layer(DefaultBodyLimit::max(PROXY_JSON_BODY_LIMIT_BYTES))
        .layer(middleware::from_fn(inject_context))
        .layer(cors)
        .layer(TraceLayer::new_for_http())
        .with_state(gateway)
}

async fn health() -> &'static str {
    r#"{"status":"ok"}"#
}

async fn readyz(State(gw): State<Gateway>) -> impl IntoResponse {
    match gw.storage.bootstrap().health().await {
        Ok(h) if h.can_connect && h.schema_compatible => (StatusCode::OK, r#"{"status":"ok"}"#),
        Ok(h) if h.can_connect => (
            StatusCode::SERVICE_UNAVAILABLE,
            r#"{"status":"schema_pending"}"#,
        ),
        _ => (
            StatusCode::SERVICE_UNAVAILABLE,
            r#"{"status":"unavailable"}"#,
        ),
    }
}

fn build_proxy_cors_layer(origins: &[String], proxy_port: u16) -> CorsLayer {
    let source_origins = if origins.is_empty() {
        default_proxy_origins(proxy_port)
    } else {
        origins.to_vec()
    };

    CorsLayer::new()
        .allow_origin(parse_allow_origin(&source_origins))
        .allow_methods([Method::GET, Method::POST, Method::OPTIONS])
        .allow_headers([
            header::AUTHORIZATION,
            header::CONTENT_TYPE,
            header::ACCEPT,
            header::HeaderName::from_static("x-api-key"),
            header::HeaderName::from_static("anthropic-version"),
            header::HeaderName::from_static("anthropic-beta"),
            header::HeaderName::from_static("openai-beta"),
            header::HeaderName::from_static("openai-organization"),
            header::HeaderName::from_static("openai-project"),
            header::HeaderName::from_static("idempotency-key"),
            header::HeaderName::from_static("x-request-id"),
        ])
}

fn default_proxy_origins(proxy_port: u16) -> Vec<String> {
    vec![
        format!("http://127.0.0.1:{proxy_port}"),
        format!("http://localhost:{proxy_port}"),
        "tauri://localhost".to_string(),
        "http://tauri.localhost".to_string(),
    ]
}

fn parse_allow_origin(origins: &[String]) -> AllowOrigin {
    if origins.iter().any(|o| o.trim() == "*") {
        return AllowOrigin::any();
    }

    let values = origins
        .iter()
        .filter_map(|o| HeaderValue::from_str(o.trim()).ok())
        .collect::<Vec<_>>();

    if values.is_empty() {
        AllowOrigin::any()
    } else {
        AllowOrigin::list(values)
    }
}

#[cfg(test)]
mod tests {
    use std::path::PathBuf;

    use crate::config::GatewayConfig;

    use super::*;

    async fn spawn_proxy() -> String {
        let mut config = GatewayConfig::default();
        config.data_dir = PathBuf::from(std::env::temp_dir()).join(format!(
            "nyro-proxy-body-limit-test-{}",
            uuid::Uuid::new_v4()
        ));
        let (gateway, _log_rx) = Gateway::new(config).await.expect("gateway init");
        let app = create_router(gateway);

        let listener = tokio::net::TcpListener::bind("127.0.0.1:0")
            .await
            .expect("bind test proxy");
        let addr = listener.local_addr().expect("test proxy address");
        tokio::spawn(async move {
            let _ = axum::serve(listener, app).await;
        });
        format!("http://{addr}")
    }

    #[tokio::test]
    async fn proxy_accepts_json_bodies_larger_than_axum_default_limit() {
        let base_url = spawn_proxy().await;
        let large_content = "x".repeat(2 * 1024 * 1024);
        let body = serde_json::json!({
            "contents": [
                {
                    "role": "user",
                    "parts": [
                        {
                            "text": large_content,
                        }
                    ],
                }
            ],
        });

        let response = reqwest::Client::new()
            .post(format!(
                "{base_url}/v1beta/models/unconfigured-gemini:generateContent"
            ))
            .header(reqwest::header::CONTENT_TYPE, "application/json")
            .body(body.to_string())
            .send()
            .await
            .expect("proxy response");

        assert_ne!(
            response.status(),
            reqwest::StatusCode::PAYLOAD_TOO_LARGE,
            "proxy must not reject large Gemini JSON bodies with axum's default 2 MiB limit"
        );
    }
}
