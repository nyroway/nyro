use axum::extract::{Path, Query, Request, State};
use axum::http::StatusCode;
use axum::middleware::{self, Next};
use axum::response::IntoResponse;
use axum::routing::{delete, get, post, put};
use axum::{Extension, Json, Router};
use nyro_core::Gateway;
use nyro_core::auth::AuthExchangeInput;
use nyro_core::db::models::*;
use serde::Deserialize;

#[derive(Clone)]
struct AdminToken(String);

async fn admin_auth(
    token_ext: Option<Extension<AdminToken>>,
    req: Request,
    next: Next,
) -> impl IntoResponse {
    let Some(Extension(admin_token)) = token_ext else {
        return next.run(req).await;
    };

    let auth_header = req
        .headers()
        .get("authorization")
        .and_then(|v| v.to_str().ok())
        .unwrap_or("");

    let token = auth_header.strip_prefix("Bearer ").unwrap_or(auth_header);

    if token == admin_token.0 {
        next.run(req).await
    } else {
        (
            StatusCode::UNAUTHORIZED,
            Json(serde_json::json!({"error": "invalid admin token"})),
        )
            .into_response()
    }
}

pub fn create_router(gateway: Gateway, admin_token: Option<String>) -> Router {
    let providers_item = get(get_provider_handler)
        .put(update_provider_handler)
        .delete(delete_provider_handler);

    let routes_item = put(update_route_handler).delete(delete_route_handler);
    let api_keys_item = get(get_api_key_handler)
        .put(update_api_key_handler)
        .delete(delete_api_key_handler);

    let mut api = Router::new()
        .route("/providers/presets", get(list_provider_presets))
        .route(
            "/providers",
            get(list_providers).post(create_provider_handler),
        )
        .route("/providers/:id/copy", post(copy_provider_handler))
        .route("/providers/:id", providers_item)
        .route("/providers/:id/test", get(test_provider_handler))
        .route(
            "/providers/:id/test-models",
            get(test_provider_models_handler),
        )
        .route("/providers/:id/models", get(provider_models_handler))
        .route(
            "/providers/:id/model-capabilities",
            get(provider_model_capabilities_handler),
        )
        .route(
            "/providers/:id/oauth/status",
            get(get_provider_oauth_status_handler),
        )
        .route(
            "/providers/:id/oauth/reconnect",
            post(reconnect_provider_oauth_handler),
        )
        .route(
            "/providers/:id/oauth/logout",
            post(logout_provider_oauth_handler),
        )
        .route(
            "/providers/:id/oauth/bind",
            post(bind_provider_oauth_handler),
        )
        .route("/providers/oauth", post(create_oauth_provider_handler))
        .route("/oauth/sessions/init", post(init_oauth_session_handler))
        .route(
            "/oauth/sessions/:id/status",
            get(get_oauth_session_status_handler),
        )
        .route(
            "/oauth/sessions/:id/cancel",
            post(cancel_oauth_session_handler),
        )
        .route(
            "/oauth/sessions/:id/complete",
            post(complete_oauth_session_handler),
        )
        .route(
            "/routes",
            get(list_routes_handler).post(create_route_handler),
        )
        .route("/routes/:id", routes_item)
        .route(
            "/api-keys",
            get(list_api_keys_handler).post(create_api_key_handler),
        )
        .route("/api-keys/:id", api_keys_item)
        .route("/logs", get(query_logs_handler))
        .route("/logs/:id", get(get_log_handler))
        .route("/stats/overview", get(stats_overview))
        .route("/stats/hourly", get(stats_hourly))
        .route("/stats/models", get(stats_by_model))
        .route("/stats/providers", get(stats_by_provider))
        .route("/settings/:key", get(get_setting).put(set_setting))
        .route(
            "/cache/settings",
            get(get_cache_settings).put(update_cache_settings),
        )
        .route(
            "/cache/embedding-dimensions",
            get(detect_embedding_dimensions),
        )
        .route("/cache/flush", post(flush_cache))
        .route("/cache/:key", delete(delete_cache_key))
        .route("/cache/stats", get(get_cache_stats))
        .route("/status", get(get_status))
        .route("/config/export", get(export_config_handler))
        .route("/config/import", axum::routing::post(import_config_handler))
        .with_state(gateway);

    if let Some(token) = admin_token {
        if !token.is_empty() {
            api = api
                .layer(middleware::from_fn(admin_auth))
                .layer(Extension(AdminToken(token)));
        }
    }

    Router::new().nest("/api/v1", api)
}

// ── Providers ──

async fn list_providers(State(gw): State<Gateway>) -> impl IntoResponse {
    match gw.admin().list_providers().await {
        Ok(v) => Json(serde_json::json!({ "data": v })).into_response(),
        Err(e) => err(e),
    }
}

async fn list_provider_presets(State(gw): State<Gateway>) -> impl IntoResponse {
    match gw.admin().list_provider_presets().await {
        Ok(v) => Json(serde_json::json!({ "data": v })).into_response(),
        Err(e) => err(e),
    }
}

async fn get_provider_handler(
    State(gw): State<Gateway>,
    Path(id): Path<String>,
) -> impl IntoResponse {
    match gw.admin().get_provider(&id).await {
        Ok(v) => Json(serde_json::json!({ "data": v })).into_response(),
        Err(e) => err(e),
    }
}

async fn create_provider_handler(
    State(gw): State<Gateway>,
    Json(input): Json<CreateProvider>,
) -> impl IntoResponse {
    match gw.admin().create_provider(input).await {
        Ok(v) => Json(serde_json::json!({ "data": v })).into_response(),
        Err(e) => err(e),
    }
}

async fn copy_provider_handler(
    State(gw): State<Gateway>,
    Path(id): Path<String>,
) -> impl IntoResponse {
    match gw.admin().copy_provider(&id).await {
        Ok(v) => Json(serde_json::json!({ "data": v })).into_response(),
        Err(e) => err(e),
    }
}

async fn update_provider_handler(
    State(gw): State<Gateway>,
    Path(id): Path<String>,
    Json(input): Json<UpdateProvider>,
) -> impl IntoResponse {
    match gw.admin().update_provider(&id, input).await {
        Ok(v) => Json(serde_json::json!({ "data": v })).into_response(),
        Err(e) => err(e),
    }
}

async fn delete_provider_handler(
    State(gw): State<Gateway>,
    Path(id): Path<String>,
) -> impl IntoResponse {
    match gw.admin().delete_provider(&id).await {
        Ok(()) => Json(serde_json::json!({ "ok": true })).into_response(),
        Err(e) => err(e),
    }
}

async fn test_provider_handler(
    State(gw): State<Gateway>,
    Path(id): Path<String>,
) -> impl IntoResponse {
    match gw.admin().test_provider(&id).await {
        Ok(v) => Json(serde_json::json!({ "data": v })).into_response(),
        Err(e) => err(e),
    }
}

async fn test_provider_models_handler(
    State(gw): State<Gateway>,
    Path(id): Path<String>,
) -> impl IntoResponse {
    match gw.admin().test_provider_models(&id).await {
        Ok(v) => Json(serde_json::json!({ "data": v })).into_response(),
        Err(e) => err(e),
    }
}

async fn provider_models_handler(
    State(gw): State<Gateway>,
    Path(id): Path<String>,
) -> impl IntoResponse {
    match gw.admin().get_provider_models(&id).await {
        Ok(v) => Json(serde_json::json!({ "data": v })).into_response(),
        Err(e) => err(e),
    }
}

async fn provider_model_capabilities_handler(
    State(gw): State<Gateway>,
    Path(id): Path<String>,
    Query(query): Query<ModelCapabilitiesQuery>,
) -> impl IntoResponse {
    match gw.admin().get_model_capabilities(&id, &query.model).await {
        Ok(v) => Json(serde_json::json!({ "data": v })).into_response(),
        Err(e) => err(e),
    }
}

#[derive(Deserialize)]
struct ModelCapabilitiesQuery {
    model: String,
}

#[derive(Deserialize)]
struct InitOAuthSessionRequest {
    vendor: String,
    #[serde(default)]
    use_proxy: bool,
}

#[derive(Deserialize)]
struct CreateOAuthProviderRequest {
    session_id: String,
    input: CreateProvider,
}

async fn init_oauth_session_handler(
    State(gw): State<Gateway>,
    Json(input): Json<InitOAuthSessionRequest>,
) -> impl IntoResponse {
    match gw
        .admin()
        .init_oauth_session(&input.vendor, input.use_proxy)
        .await
    {
        Ok(v) => Json(serde_json::json!({ "data": v })).into_response(),
        Err(e) => err(e),
    }
}

async fn get_oauth_session_status_handler(
    State(gw): State<Gateway>,
    Path(id): Path<String>,
) -> impl IntoResponse {
    match gw.admin().get_oauth_session_status(&id).await {
        Ok(v) => Json(serde_json::json!({ "data": v })).into_response(),
        Err(e) => err(e),
    }
}

async fn cancel_oauth_session_handler(
    State(gw): State<Gateway>,
    Path(id): Path<String>,
) -> impl IntoResponse {
    match gw.admin().cancel_oauth_session(&id).await {
        Ok(()) => Json(serde_json::json!({ "ok": true })).into_response(),
        Err(e) => err(e),
    }
}

async fn complete_oauth_session_handler(
    State(gw): State<Gateway>,
    Path(id): Path<String>,
    Json(input): Json<AuthExchangeInput>,
) -> impl IntoResponse {
    match gw.admin().complete_oauth_session(&id, input).await {
        Ok(v) => Json(serde_json::json!({ "data": v })).into_response(),
        Err(e) => err(e),
    }
}

async fn create_oauth_provider_handler(
    State(gw): State<Gateway>,
    Json(input): Json<CreateOAuthProviderRequest>,
) -> impl IntoResponse {
    match gw
        .admin()
        .create_provider_with_oauth_session(&input.session_id, input.input)
        .await
    {
        Ok(v) => Json(serde_json::json!({ "data": v })).into_response(),
        Err(e) => err(e),
    }
}

async fn get_provider_oauth_status_handler(
    State(gw): State<Gateway>,
    Path(id): Path<String>,
) -> impl IntoResponse {
    match gw.admin().get_provider_oauth_status(&id).await {
        Ok(v) => Json(serde_json::json!({ "data": v })).into_response(),
        Err(e) => err(e),
    }
}

async fn reconnect_provider_oauth_handler(
    State(gw): State<Gateway>,
    Path(id): Path<String>,
) -> impl IntoResponse {
    match gw.admin().reconnect_provider_oauth(&id).await {
        Ok(v) => Json(serde_json::json!({ "data": v })).into_response(),
        Err(e) => err(e),
    }
}

async fn logout_provider_oauth_handler(
    State(gw): State<Gateway>,
    Path(id): Path<String>,
) -> impl IntoResponse {
    match gw.admin().logout_provider_oauth(&id).await {
        Ok(v) => Json(serde_json::json!({ "data": v })).into_response(),
        Err(e) => err(e),
    }
}

#[derive(serde::Deserialize)]
struct BindProviderOAuthRequest {
    session_id: String,
}

async fn bind_provider_oauth_handler(
    State(gw): State<Gateway>,
    Path(id): Path<String>,
    Json(input): Json<BindProviderOAuthRequest>,
) -> impl IntoResponse {
    match gw
        .admin()
        .bind_provider_with_oauth_session(&id, &input.session_id)
        .await
    {
        Ok(v) => Json(serde_json::json!({ "data": v })).into_response(),
        Err(e) => err(e),
    }
}

// ── Routes ──

async fn list_routes_handler(State(gw): State<Gateway>) -> impl IntoResponse {
    match gw.admin().list_routes().await {
        Ok(v) => Json(serde_json::json!({ "data": v })).into_response(),
        Err(e) => err(e),
    }
}

async fn create_route_handler(
    State(gw): State<Gateway>,
    Json(input): Json<CreateRoute>,
) -> impl IntoResponse {
    match gw.admin().create_route(input).await {
        Ok(v) => Json(serde_json::json!({ "data": v })).into_response(),
        Err(e) => err(e),
    }
}

async fn update_route_handler(
    State(gw): State<Gateway>,
    Path(id): Path<String>,
    Json(input): Json<UpdateRoute>,
) -> impl IntoResponse {
    match gw.admin().update_route(&id, input).await {
        Ok(v) => Json(serde_json::json!({ "data": v })).into_response(),
        Err(e) => err(e),
    }
}

async fn delete_route_handler(
    State(gw): State<Gateway>,
    Path(id): Path<String>,
) -> impl IntoResponse {
    match gw.admin().delete_route(&id).await {
        Ok(()) => Json(serde_json::json!({ "ok": true })).into_response(),
        Err(e) => err(e),
    }
}

// ── API Keys ──

async fn list_api_keys_handler(State(gw): State<Gateway>) -> impl IntoResponse {
    match gw.admin().list_api_keys().await {
        Ok(v) => Json(serde_json::json!({ "data": v })).into_response(),
        Err(e) => err(e),
    }
}

async fn get_api_key_handler(
    State(gw): State<Gateway>,
    Path(id): Path<String>,
) -> impl IntoResponse {
    match gw.admin().get_api_key(&id).await {
        Ok(v) => Json(serde_json::json!({ "data": v })).into_response(),
        Err(e) => err(e),
    }
}

async fn create_api_key_handler(
    State(gw): State<Gateway>,
    Json(input): Json<CreateApiKey>,
) -> impl IntoResponse {
    match gw.admin().create_api_key(input).await {
        Ok(v) => Json(serde_json::json!({ "data": v })).into_response(),
        Err(e) => err(e),
    }
}

async fn update_api_key_handler(
    State(gw): State<Gateway>,
    Path(id): Path<String>,
    Json(input): Json<UpdateApiKey>,
) -> impl IntoResponse {
    match gw.admin().update_api_key(&id, input).await {
        Ok(v) => Json(serde_json::json!({ "data": v })).into_response(),
        Err(e) => err(e),
    }
}

async fn delete_api_key_handler(
    State(gw): State<Gateway>,
    Path(id): Path<String>,
) -> impl IntoResponse {
    match gw.admin().delete_api_key(&id).await {
        Ok(()) => Json(serde_json::json!({ "ok": true })).into_response(),
        Err(e) => err(e),
    }
}

// ── Logs ──

#[derive(Deserialize, Default)]
struct LogQueryParams {
    limit: Option<i64>,
    offset: Option<i64>,
    provider: Option<String>,
    model: Option<String>,
    status_min: Option<i32>,
    status_max: Option<i32>,
}

async fn get_log_handler(
    State(gw): State<Gateway>,
    axum::extract::Path(id): axum::extract::Path<String>,
) -> impl IntoResponse {
    match gw.admin().get_log(&id).await {
        Ok(Some(v)) => Json(serde_json::json!({ "data": v })).into_response(),
        Ok(None) => (
            axum::http::StatusCode::NOT_FOUND,
            Json(serde_json::json!({ "error": "not found" })),
        )
            .into_response(),
        Err(e) => err(e),
    }
}

async fn query_logs_handler(
    State(gw): State<Gateway>,
    Query(params): Query<LogQueryParams>,
) -> impl IntoResponse {
    let q = LogQuery {
        limit: params.limit,
        offset: params.offset,
        provider: params.provider,
        model: params.model,
        status_min: params.status_min,
        status_max: params.status_max,
    };
    match gw.admin().query_logs(q).await {
        Ok(v) => Json(serde_json::json!({ "data": v })).into_response(),
        Err(e) => err(e),
    }
}

// ── Stats ──

#[derive(Deserialize, Default)]
struct StatsRangeParams {
    hours: Option<i32>,
}

async fn stats_overview(
    State(gw): State<Gateway>,
    Query(params): Query<StatsRangeParams>,
) -> impl IntoResponse {
    match gw.admin().get_stats_overview(params.hours).await {
        Ok(v) => Json(serde_json::json!({ "data": v })).into_response(),
        Err(e) => err(e),
    }
}

#[derive(Deserialize)]
struct HourlyParams {
    #[serde(default = "default_hours")]
    hours: i32,
}

fn default_hours() -> i32 {
    24
}

async fn stats_hourly(
    State(gw): State<Gateway>,
    Query(params): Query<HourlyParams>,
) -> impl IntoResponse {
    match gw.admin().get_stats_hourly(params.hours).await {
        Ok(v) => Json(serde_json::json!({ "data": v })).into_response(),
        Err(e) => err(e),
    }
}

async fn stats_by_model(
    State(gw): State<Gateway>,
    Query(params): Query<StatsRangeParams>,
) -> impl IntoResponse {
    match gw.admin().get_stats_by_model(params.hours).await {
        Ok(v) => Json(serde_json::json!({ "data": v })).into_response(),
        Err(e) => err(e),
    }
}

async fn stats_by_provider(
    State(gw): State<Gateway>,
    Query(params): Query<StatsRangeParams>,
) -> impl IntoResponse {
    match gw.admin().get_stats_by_provider(params.hours).await {
        Ok(v) => Json(serde_json::json!({ "data": v })).into_response(),
        Err(e) => err(e),
    }
}

// ── Settings ──

async fn get_setting(State(gw): State<Gateway>, Path(key): Path<String>) -> impl IntoResponse {
    match gw.admin().get_setting(&key).await {
        Ok(v) => Json(serde_json::json!({ "data": v })).into_response(),
        Err(e) => err(e),
    }
}

#[derive(Deserialize)]
struct SettingBody {
    value: String,
}

async fn set_setting(
    State(gw): State<Gateway>,
    Path(key): Path<String>,
    Json(body): Json<SettingBody>,
) -> impl IntoResponse {
    match gw.admin().set_setting(&key, &body.value).await {
        Ok(()) => Json(serde_json::json!({ "ok": true })).into_response(),
        Err(e) => err(e),
    }
}

async fn get_cache_settings(State(gw): State<Gateway>) -> impl IntoResponse {
    match gw.admin().get_cache_settings().await {
        Ok(v) => Json(serde_json::json!({ "data": v })).into_response(),
        Err(e) => err(e),
    }
}

async fn update_cache_settings(
    State(gw): State<Gateway>,
    Json(input): Json<serde_json::Value>,
) -> impl IntoResponse {
    match gw.admin().update_cache_settings(input).await {
        Ok(()) => Json(serde_json::json!({ "ok": true })).into_response(),
        Err(e) => err(e),
    }
}

#[derive(Deserialize)]
struct EmbeddingDimensionsQuery {
    embedding_route: String,
}

async fn detect_embedding_dimensions(
    State(gw): State<Gateway>,
    Query(query): Query<EmbeddingDimensionsQuery>,
) -> impl IntoResponse {
    match gw
        .admin()
        .detect_embedding_dimensions(&query.embedding_route)
        .await
    {
        Ok(v) => Json(serde_json::json!({ "data": v })).into_response(),
        Err(e) => err(e),
    }
}

async fn flush_cache(State(gw): State<Gateway>) -> impl IntoResponse {
    match gw.admin().flush_cache().await {
        Ok(()) => Json(serde_json::json!({ "ok": true })).into_response(),
        Err(e) => err(e),
    }
}

async fn delete_cache_key(State(gw): State<Gateway>, Path(key): Path<String>) -> impl IntoResponse {
    match gw.admin().delete_cache_key(&key).await {
        Ok(()) => Json(serde_json::json!({ "ok": true })).into_response(),
        Err(e) => err(e),
    }
}

async fn get_cache_stats(State(gw): State<Gateway>) -> impl IntoResponse {
    match gw.admin().get_cache_stats().await {
        Ok(v) => Json(serde_json::json!({ "data": v })).into_response(),
        Err(e) => err(e),
    }
}

// ── Status ──

async fn get_status(State(gw): State<Gateway>) -> impl IntoResponse {
    Json(serde_json::json!({
        "status": "running",
        "proxy_port": gw.config.proxy_port,
    }))
}

// ── Config Import/Export ──

async fn export_config_handler(State(gw): State<Gateway>) -> impl IntoResponse {
    match gw.admin().export_config().await {
        Ok(v) => Json(serde_json::json!({ "data": v })).into_response(),
        Err(e) => err(e),
    }
}

async fn import_config_handler(
    State(gw): State<Gateway>,
    Json(data): Json<ExportData>,
) -> impl IntoResponse {
    match gw.admin().import_config(data).await {
        Ok(v) => Json(serde_json::json!({ "data": v })).into_response(),
        Err(e) => err(e),
    }
}

fn err(e: anyhow::Error) -> axum::response::Response {
    Json(serde_json::json!({ "error": e.to_string() })).into_response()
}
