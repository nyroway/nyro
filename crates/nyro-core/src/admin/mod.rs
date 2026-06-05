use anyhow::Context;
use chrono::{DateTime, Utc};
use std::collections::HashMap;
use std::path::{Path, PathBuf};
use std::time::{Duration, Instant};

use reqwest::header::{AUTHORIZATION, HeaderMap, HeaderValue};
use serde::{Deserialize, Serialize};
use serde_json::Value;

use crate::Gateway;
use crate::auth;
use crate::auth::types::{
    AuthBindingStatus, AuthPollState, AuthScheme, AuthSession, AuthSessionInitData,
    AuthSessionStatus, AuthSessionStatusData, CredentialBundle, ExchangeAuthContext,
    RefreshAuthContext, RuntimeBinding, StartAuthContext, StoredCredential, UpdateAuthSession,
};
use crate::db::models::*;
use crate::provider::metadata::CapabilitiesSource;
use crate::provider::{VendorRegistry, vertexai};
use crate::storage::traits::ProviderTestResult;

mod api_keys;
mod auth_data;
mod import_export;
mod model_catalog;
mod model_data;
mod models;
mod oauth;
mod observability;
mod providers;
pub mod settings;

use auth_data::*;
use model_catalog::*;
pub use model_catalog::{
    refresh_models_dev_runtime_cache_if_stale, refresh_models_dev_runtime_cache_on_startup,
};
use model_data::*;

#[cfg(test)]
mod session_tests;

#[derive(Debug, Clone, Default, Deserialize)]
pub struct CopyProviderOptions {
    #[serde(default)]
    pub append_targets: bool,
}

#[derive(Debug, Clone, Serialize)]
pub struct ProviderOAuthStatusData {
    pub provider_id: String,
    pub provider_name: String,
    pub driver_key: String,
    pub status: String,
    pub expires_at: Option<String>,
    pub resource_url: Option<String>,
    pub subject_id: Option<String>,
    pub last_error: Option<String>,
    pub updated_at: Option<String>,
    pub has_refresh_token: bool,
}

#[derive(Clone)]
pub struct AdminService {
    gw: Gateway,
}

pub(crate) struct ResolvedProviderRuntime {
    pub access_token: String,
    pub binding: RuntimeBinding,
}

impl AdminService {
    pub fn new(gw: Gateway) -> Self {
        Self { gw }
    }
}

pub(super) fn format_connectivity_error(error: &reqwest::Error) -> String {
    if error.is_timeout() {
        return "Connection timeout (10s), please check Base URL or network settings".to_string();
    }
    if error.is_connect() {
        return "Unable to connect to the host, please check DNS/network settings".to_string();
    }
    error.to_string()
}

pub(super) fn coded_error(code: &str, message: &str, params: Value) -> anyhow::Error {
    anyhow::anyhow!(
        "{}",
        serde_json::json!({
            "code": code,
            "message": message,
            "params": params,
        })
    )
}
pub(super) fn normalize_name(name: &str, field: &str) -> anyhow::Result<String> {
    let trimmed = name.trim();
    if trimmed.is_empty() {
        anyhow::bail!("{field} cannot be empty");
    }
    Ok(trimmed.to_string())
}

pub(super) fn normalize_vendor(vendor: Option<&str>) -> Option<String> {
    vendor
        .map(str::trim)
        .filter(|v| !v.is_empty() && *v != "custom")
        .map(|v| v.to_lowercase())
}
