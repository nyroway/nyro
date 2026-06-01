//! Security layer: API-key authentication and per-key quota enforcement.
//!
//! This module owns all logic that was previously inside `handler.rs` under
//! `authorize_model_access`, `extract_api_key`, and `is_key_expired`.
//!
//! The public API is a single async function `check_model_access` that returns
//! an `AuthenticatedKey` on success or a `GatewayError` on failure.

use async_trait::async_trait;
use axum::http::{HeaderMap, header};
use chrono::{NaiveDateTime, Utc};

use crate::Gateway;
use crate::db::models::{Model, Provider};
use crate::error::{AccessDenial, AuthFailure, GatewayError, QuotaWindow};
use crate::proxy::context::{AuthSubject, RequestContext};
use crate::storage::traits::{ApiKeyAccessRecord, UsageWindow};

// ── Public result type ────────────────────────────────────────────────────────

/// The result of a successful security check.
#[derive(Debug, Clone)]
pub struct AuthenticatedKey {
    /// Database row-id of the validated API key, `None` if route has no access
    /// control.
    pub id: Option<String>,
}

impl AuthenticatedKey {
    pub fn as_auth_subject(&self) -> Option<AuthSubject> {
        self.id.as_ref().map(|id| AuthSubject {
            api_key_id: Some(id.clone()),
            label: None,
        })
    }
}

// ── Access store abstraction ──────────────────────────────────────────────────

#[async_trait]
pub trait ProxyAccessStore: Send + Sync {
    async fn get_active_provider(&self, id: &str) -> anyhow::Result<Option<Provider>>;
    async fn find_api_key(&self, raw_key: &str) -> anyhow::Result<Option<ApiKeyAccessRecord>>;
    async fn model_binding_exists(&self, api_key_id: &str, model_id: &str) -> anyhow::Result<bool>;
    async fn request_count_since(
        &self,
        api_key_id: &str,
        window: UsageWindow,
    ) -> anyhow::Result<i64>;
    async fn token_count_since(&self, api_key_id: &str, window: UsageWindow)
    -> anyhow::Result<i64>;
}

pub struct GatewayProxyAccessStore<'a> {
    gw: &'a Gateway,
}

impl<'a> GatewayProxyAccessStore<'a> {
    pub fn new(gw: &'a Gateway) -> Self {
        Self { gw }
    }
}

#[async_trait]
impl ProxyAccessStore for GatewayProxyAccessStore<'_> {
    async fn get_active_provider(&self, id: &str) -> anyhow::Result<Option<Provider>> {
        let provider = self.gw.storage.providers().get(id).await?;
        Ok(provider.filter(|p| p.is_enabled))
    }

    async fn find_api_key(&self, raw_key: &str) -> anyhow::Result<Option<ApiKeyAccessRecord>> {
        match self.gw.storage.auth() {
            Some(store) => store.find_api_key(raw_key).await,
            None => Ok(None),
        }
    }

    async fn model_binding_exists(&self, api_key_id: &str, model_id: &str) -> anyhow::Result<bool> {
        match self.gw.storage.auth() {
            Some(store) => store.model_binding_exists(api_key_id, model_id).await,
            None => Ok(false),
        }
    }

    async fn request_count_since(
        &self,
        api_key_id: &str,
        window: UsageWindow,
    ) -> anyhow::Result<i64> {
        match self.gw.storage.auth() {
            Some(store) => store.request_count_since(api_key_id, window).await,
            None => Ok(0),
        }
    }

    async fn token_count_since(
        &self,
        api_key_id: &str,
        window: UsageWindow,
    ) -> anyhow::Result<i64> {
        match self.gw.storage.auth() {
            Some(store) => store.token_count_since(api_key_id, window).await,
            None => Ok(0),
        }
    }
}

// ── Public API ────────────────────────────────────────────────────────────────

/// Authenticate the caller and enforce per-key quota limits.
///
/// Returns `Ok(AuthenticatedKey)` if the request is allowed to proceed, or a
/// typed `GatewayError` otherwise.
///
/// Internally stamps `ctx.auth_subject` so downstream layers don't need to
/// repeat the lookup.
pub async fn check_model_access(
    access_store: &dyn ProxyAccessStore,
    model: &Model,
    headers: &HeaderMap,
    ctx: &mut RequestContext,
) -> Result<AuthenticatedKey, GatewayError> {
    if !model.enable_auth {
        return Ok(AuthenticatedKey { id: None });
    }

    let raw_key = extract_api_key(headers).ok_or(GatewayError::Unauthorized {
        reason: AuthFailure::Missing,
    })?;

    let key_row = access_store
        .find_api_key(&raw_key)
        .await
        .map_err(GatewayError::internal)?;

    let key_row = key_row.ok_or(GatewayError::Unauthorized {
        reason: AuthFailure::Invalid,
    })?;

    if !key_row.is_enabled {
        return Err(GatewayError::Forbidden {
            reason: AccessDenial::Custom("api key disabled".into()),
        });
    }

    if let Some(expires) = key_row.expires_at.as_ref()
        && is_key_expired(expires)
    {
        return Err(GatewayError::Unauthorized {
            reason: AuthFailure::Expired,
        });
    }

    let allowed = access_store
        .model_binding_exists(&key_row.id, &model.id)
        .await
        .map_err(GatewayError::internal)?;
    if !allowed {
        return Err(GatewayError::Forbidden {
            reason: AccessDenial::ModelNotAllowed,
        });
    }

    // ── Quota checks ─────────────────────────────────────────────────────────

    if let Some(limit) = key_row.rpm.filter(|v| *v > 0) {
        let count = access_store
            .request_count_since(&key_row.id, UsageWindow::Minute)
            .await
            .map_err(GatewayError::internal)?;
        if count >= i64::from(limit) {
            return Err(GatewayError::QuotaExceeded {
                window: QuotaWindow {
                    window_type: "rpm".into(),
                    reset_at_secs: None,
                },
            });
        }
    }

    if let Some(limit) = key_row.rpd.filter(|v| *v > 0) {
        let count = access_store
            .request_count_since(&key_row.id, UsageWindow::Day)
            .await
            .map_err(GatewayError::internal)?;
        if count >= i64::from(limit) {
            return Err(GatewayError::QuotaExceeded {
                window: QuotaWindow {
                    window_type: "rpd".into(),
                    reset_at_secs: None,
                },
            });
        }
    }

    if let Some(limit) = key_row.tpm.filter(|v| *v > 0) {
        let count = access_store
            .token_count_since(&key_row.id, UsageWindow::Minute)
            .await
            .map_err(GatewayError::internal)?;
        if count >= i64::from(limit) {
            return Err(GatewayError::QuotaExceeded {
                window: QuotaWindow {
                    window_type: "tpm".into(),
                    reset_at_secs: None,
                },
            });
        }
    }

    if let Some(limit) = key_row.tpd.filter(|v| *v > 0) {
        let count = access_store
            .token_count_since(&key_row.id, UsageWindow::Day)
            .await
            .map_err(GatewayError::internal)?;
        if count >= i64::from(limit) {
            return Err(GatewayError::QuotaExceeded {
                window: QuotaWindow {
                    window_type: "tpd".into(),
                    reset_at_secs: None,
                },
            });
        }
    }

    let key = AuthenticatedKey {
        id: Some(key_row.id),
    };
    ctx.auth_subject = key.as_auth_subject();
    Ok(key)
}

// ── Helpers ───────────────────────────────────────────────────────────────────

/// Extract the client API key from supported ingress authentication headers.
pub fn extract_api_key(headers: &HeaderMap) -> Option<String> {
    if let Some(value) = headers
        .get(header::AUTHORIZATION)
        .and_then(|v| v.to_str().ok())
        && let Some(token) = value.strip_prefix("Bearer ")
    {
        let token = token.trim();
        if !token.is_empty() {
            return Some(token.to_string());
        }
    }

    for header_name in ["x-api-key", "x-goog-api-key"] {
        if let Some(key) = headers
            .get(header_name)
            .and_then(|v| v.to_str().ok())
            .map(str::trim)
            .filter(|v| !v.is_empty())
        {
            return Some(key.to_string());
        }
    }

    None
}

pub(crate) fn is_key_expired(expires_at: &str) -> bool {
    if let Ok(parsed) = chrono::DateTime::parse_from_rfc3339(expires_at) {
        return parsed.with_timezone(&Utc) <= Utc::now();
    }

    NaiveDateTime::parse_from_str(expires_at, "%Y-%m-%d %H:%M:%S")
        .map(|parsed| parsed.and_utc() <= Utc::now())
        .unwrap_or(false)
}

pub async fn get_provider(
    access_store: &dyn ProxyAccessStore,
    id: &str,
) -> anyhow::Result<Provider> {
    access_store
        .get_active_provider(id)
        .await?
        .ok_or_else(|| anyhow::anyhow!("provider not found or inactive: {id}"))
}

#[cfg(test)]
mod tests {
    use axum::http::{HeaderMap, HeaderValue, header};

    use super::extract_api_key;

    #[test]
    fn extract_api_key_accepts_bearer_and_trims_token() {
        let mut headers = HeaderMap::new();
        headers.insert(
            header::AUTHORIZATION,
            HeaderValue::from_static("Bearer   sk-openai"),
        );

        assert_eq!(extract_api_key(&headers).as_deref(), Some("sk-openai"));
    }

    #[test]
    fn extract_api_key_accepts_anthropic_header() {
        let mut headers = HeaderMap::new();
        headers.insert("x-api-key", HeaderValue::from_static(" sk-anthropic "));

        assert_eq!(extract_api_key(&headers).as_deref(), Some("sk-anthropic"));
    }

    #[test]
    fn extract_api_key_accepts_google_genai_header() {
        let mut headers = HeaderMap::new();
        headers.insert("x-goog-api-key", HeaderValue::from_static(" sk-google "));

        assert_eq!(extract_api_key(&headers).as_deref(), Some("sk-google"));
    }
}
