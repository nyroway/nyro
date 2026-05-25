//! API-key authentication and per-route quota enforcement.

use async_trait::async_trait;
use axum::http::HeaderMap;
use axum::response::Response;

use crate::Gateway;
use crate::db::models::{Model, Provider};
use crate::proxy::security::{extract_api_key, is_key_expired};
use crate::storage::traits::{ApiKeyAccessRecord, UsageWindow};

use super::error_response;

// ── Types ─────────────────────────────────────────────────────────────────────

pub(super) struct AuthenticatedKey {
    pub(super) id: Option<String>,
    pub(super) name: Option<String>,
}

#[async_trait]
pub(super) trait ProxyAccessStore {
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

pub(super) struct GatewayProxyAccessStore<'a> {
    pub(super) gw: &'a Gateway,
}

impl<'a> GatewayProxyAccessStore<'a> {
    pub(super) fn new(gw: &'a Gateway) -> Self {
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

// ── Functions ─────────────────────────────────────────────────────────────────

pub(super) async fn authorize_model_access<S: ProxyAccessStore + ?Sized>(
    access_store: &S,
    model: &Model,
    headers: &HeaderMap,
) -> Result<AuthenticatedKey, Response> {
    if !model.access_control {
        return Ok(AuthenticatedKey {
            id: None,
            name: None,
        });
    }

    let Some(raw_key) = extract_api_key(headers) else {
        return Err(error_response(401, "missing api key"));
    };

    let key_row = access_store
        .find_api_key(&raw_key)
        .await
        .map_err(|e| error_response(500, &format!("auth db error: {e}")))?;

    let Some(key_row) = key_row else {
        return Err(error_response(401, "invalid api key"));
    };

    if !key_row.is_enabled {
        return Err(error_response(403, "api key disabled"));
    }

    if let Some(expires) = key_row.expires_at.as_ref()
        && is_key_expired(expires)
    {
        return Err(error_response(403, "api key expired"));
    }

    let allowed = access_store
        .model_binding_exists(&key_row.id, &model.id)
        .await
        .map_err(|e| error_response(500, &format!("auth db error: {e}")))?;
    if !allowed {
        return Err(error_response(403, "api key not allowed for this model"));
    }

    if let Some(limit) = key_row.rpm.filter(|v| *v > 0) {
        let req_count = access_store
            .request_count_since(&key_row.id, UsageWindow::Minute)
            .await
            .map_err(|e| error_response(500, &format!("quota db error: {e}")))?;
        if req_count >= i64::from(limit) {
            return Err(error_response(429, "api key rpm quota exceeded"));
        }
    }

    if let Some(limit) = key_row.rpd.filter(|v| *v > 0) {
        let req_count = access_store
            .request_count_since(&key_row.id, UsageWindow::Day)
            .await
            .map_err(|e| error_response(500, &format!("quota db error: {e}")))?;
        if req_count >= i64::from(limit) {
            return Err(error_response(429, "api key rpd quota exceeded"));
        }
    }

    if let Some(limit) = key_row.tpm.filter(|v| *v > 0) {
        let token_count = access_store
            .token_count_since(&key_row.id, UsageWindow::Minute)
            .await
            .map_err(|e| error_response(500, &format!("quota db error: {e}")))?;
        if token_count >= i64::from(limit) {
            return Err(error_response(429, "api key tpm quota exceeded"));
        }
    }

    if let Some(limit) = key_row.tpd.filter(|v| *v > 0) {
        let token_count = access_store
            .token_count_since(&key_row.id, UsageWindow::Day)
            .await
            .map_err(|e| error_response(500, &format!("quota db error: {e}")))?;
        if token_count >= i64::from(limit) {
            return Err(error_response(429, "api key tpd quota exceeded"));
        }
    }

    Ok(AuthenticatedKey {
        id: Some(key_row.id),
        name: Some(key_row.name),
    })
}

pub(super) async fn get_provider<S: ProxyAccessStore + ?Sized>(
    access_store: &S,
    id: &str,
) -> anyhow::Result<Provider> {
    access_store
        .get_active_provider(id)
        .await?
        .ok_or_else(|| anyhow::anyhow!("provider not found or inactive: {id}"))
}
