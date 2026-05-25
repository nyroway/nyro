use serde::{Deserialize, Serialize};
use sqlx::FromRow;

use crate::provider::AuthMode;
use crate::provider::VendorRegistry;

pub fn default_provider_auth_mode() -> String {
    "apikey".to_string()
}

pub fn is_valid_provider_auth_mode(value: &str) -> bool {
    matches!(value.trim(), "apikey" | "oauth")
}

fn auth_mode_to_legacy(mode: AuthMode) -> &'static str {
    // Legacy DB / WebUI vocabulary only knows "apikey" / "oauth"; the
    // newer `setuptoken` mode degrades to "apikey" for storage purposes
    // (the OAuth driver layer knows the real flow via vendor metadata).
    match mode {
        AuthMode::ApiKey => "apikey",
        AuthMode::OAuth => "oauth",
        AuthMode::SetupToken => "apikey",
    }
}

/// Resolve the authentication mode for a `(preset_key, channel_id)`
/// pair by consulting the in-process `VendorRegistry`. Falls back to
/// the preset's `default` channel, then to `None` when the vendor is
/// unknown to the registry.
pub fn resolve_preset_channel_auth_mode(
    preset_key: Option<&str>,
    channel_id: Option<&str>,
) -> Option<String> {
    let preset_key = preset_key?.trim();
    if preset_key.is_empty() {
        return None;
    }
    let requested_channel = channel_id
        .map(str::trim)
        .filter(|value| !value.is_empty())
        .unwrap_or("default");
    let metadata = VendorRegistry::global().metadata(preset_key)?;
    let channel = metadata
        .channels
        .iter()
        .find(|c| c.id.eq_ignore_ascii_case(requested_channel))
        .or_else(|| metadata.channels.iter().find(|c| c.id == "default"))?;
    Some(auth_mode_to_legacy(channel.auth_mode).to_string())
}

#[derive(Debug, Clone, Serialize, Deserialize, FromRow)]
pub struct Provider {
    pub id: String,
    pub name: String,
    pub vendor: Option<String>,
    pub protocol: String,
    pub base_url: String,
    pub preset_key: Option<String>,
    pub channel: Option<String>,
    #[serde(alias = "modelsEndpoint")]
    pub models_source: Option<String>,
    pub static_models: Option<String>,
    pub api_key: String,
    #[serde(default = "default_provider_auth_mode")]
    pub auth_mode: String,
    #[serde(default)]
    pub use_proxy: bool,
    pub last_test_success: Option<bool>,
    pub last_test_at: Option<String>,
    pub is_enabled: bool,
    pub created_at: String,
    pub updated_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, FromRow)]
pub struct OAuthCredential {
    pub provider_id: String,
    pub driver_key: String,
    pub scheme: String,
    pub access_token: String,
    pub refresh_token: Option<String>,
    pub expires_at: Option<String>,
    pub resource_url: Option<String>,
    pub subject_id: Option<String>,
    pub scopes: String,
    pub meta: String,
    pub status: String,
    pub status_version: i32,
    pub last_error: Option<String>,
    pub last_refresh_at: Option<String>,
    pub created_at: String,
    pub updated_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, Default)]
pub struct UpsertOAuthCredential {
    pub driver_key: String,
    pub scheme: String,
    pub access_token: String,
    pub refresh_token: Option<String>,
    pub expires_at: Option<String>,
    pub resource_url: Option<String>,
    pub subject_id: Option<String>,
    pub scopes: Option<String>,
    pub meta: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize, FromRow)]
pub struct Model {
    pub id: String,
    pub name: String,
    #[serde(alias = "vmodel")]
    pub virtual_model: String,
    pub balance: String,
    pub target_provider: String,
    pub target_model: String,
    pub access_control: bool,
    pub is_enabled: bool,
    pub created_at: String,
    #[serde(default)]
    #[sqlx(skip)]
    pub targets: Vec<ModelBackend>,
}

#[derive(Debug, Clone, Serialize, Deserialize, FromRow)]
pub struct ModelBackend {
    pub id: String,
    pub model_id: String,
    pub provider_id: String,
    pub model: String,
    pub weight: i32,
    pub priority: i32,
    pub created_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
#[serde(rename_all = "snake_case")]
#[derive(Default)]
pub enum ModelBalance {
    /// Weighted reservoir sampling — targets with higher weight are preferred.
    #[default]
    Weighted,
    /// Priority groups — lower priority number tried first; random within group.
    Priority,
    /// Cooldown-aware round-robin — deprioritises recently-used targets.
    Cooldown,
    /// Latency-ordered — targets sorted by ascending EMA response latency.
    Latency,
}

impl ModelBalance {
    pub fn as_str(&self) -> &'static str {
        match self {
            Self::Weighted => "weighted",
            Self::Priority => "priority",
            Self::Cooldown => "cooldown",
            Self::Latency => "latency",
        }
    }
}

impl std::str::FromStr for ModelBalance {
    type Err = anyhow::Error;

    fn from_str(s: &str) -> Result<Self, Self::Err> {
        match s.trim().to_ascii_lowercase().as_str() {
            "weighted" => Ok(Self::Weighted),
            "priority" => Ok(Self::Priority),
            "cooldown" => Ok(Self::Cooldown),
            "latency" => Ok(Self::Latency),
            other => anyhow::bail!("unsupported model balance: {other}"),
        }
    }
}

#[derive(Debug, Clone, Serialize, Deserialize, FromRow)]
pub struct ApiKey {
    pub id: String,
    pub key: String,
    pub name: String,
    pub rpm: Option<i32>,
    pub rpd: Option<i32>,
    pub tpm: Option<i32>,
    pub tpd: Option<i32>,
    pub is_enabled: bool,
    pub expires_at: Option<String>,
    pub created_at: String,
    pub updated_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ApiKeyWithBindings {
    pub id: String,
    pub key: String,
    pub name: String,
    pub rpm: Option<i32>,
    pub rpd: Option<i32>,
    pub tpm: Option<i32>,
    pub tpd: Option<i32>,
    pub is_enabled: bool,
    pub expires_at: Option<String>,
    pub created_at: String,
    pub updated_at: String,
    #[serde(alias = "route_ids")]
    pub model_ids: Vec<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize, FromRow)]
pub struct RequestLog {
    pub id: String,
    /// Unix 毫秒时间戳
    pub created_at: i64,
    pub api_key_id: Option<String>,
    pub api_key_name: Option<String>,

    pub client_protocol: Option<String>,
    pub upstream_protocol: Option<String>,
    pub provider_id: Option<String>,
    pub provider_name: Option<String>,
    #[serde(alias = "route_id")]
    pub model_id: Option<String>,
    #[serde(alias = "route_name")]
    pub model_name: Option<String>,
    pub upstream_url: Option<String>,
    pub client_model: Option<String>,
    pub upstream_model: Option<String>,

    pub method: Option<String>,
    pub path: Option<String>,

    #[serde(skip_serializing_if = "Option::is_none")]
    pub client_request_headers: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub client_request_body: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub client_response_headers: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub client_response_body: Option<String>,

    #[serde(skip_serializing_if = "Option::is_none")]
    pub upstream_request_headers: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub upstream_request_body: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub upstream_response_headers: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub upstream_response_body: Option<String>,

    pub upstream_status_code: Option<i32>,
    pub client_status_code: Option<i32>,

    pub latency_total_ms: Option<i64>,
    pub latency_upstream_ms: Option<i64>,
    pub input_tokens: i32,
    pub output_tokens: i32,
    #[serde(default)]
    pub cache_read_tokens: i32,

    pub is_stream: bool,
    pub stream_chunks_count: i32,
    pub stream_first_chunk_ms: Option<i64>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct CreateProvider {
    pub name: String,
    pub vendor: Option<String>,
    pub protocol: String,
    pub base_url: String,
    pub preset_key: Option<String>,
    pub channel: Option<String>,
    #[serde(alias = "modelsSource")]
    pub models_source: Option<String>,
    pub static_models: Option<String>,
    pub api_key: String,
    #[serde(default = "default_provider_auth_mode")]
    pub auth_mode: String,
    #[serde(default)]
    pub use_proxy: bool,
}

#[derive(Debug, Clone, Serialize, Deserialize, Default)]
pub struct UpdateProvider {
    pub name: Option<String>,
    pub vendor: Option<String>,
    pub protocol: Option<String>,
    pub base_url: Option<String>,
    pub preset_key: Option<String>,
    pub channel: Option<String>,
    #[serde(alias = "modelsSource")]
    pub models_source: Option<String>,
    pub static_models: Option<String>,
    pub api_key: Option<String>,
    pub auth_mode: Option<String>,
    pub use_proxy: Option<bool>,
    pub is_enabled: Option<bool>,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct UpdateModel {
    pub name: Option<String>,
    #[serde(alias = "vmodel")]
    pub virtual_model: Option<String>,
    #[serde(rename = "balance", alias = "strategy")]
    pub balance: Option<String>,
    pub target_provider: Option<String>,
    pub target_model: Option<String>,
    #[serde(default)]
    pub targets: Option<Vec<UpsertModelBackend>>,
    pub access_control: Option<bool>,
    pub is_enabled: Option<bool>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct CreateModel {
    pub name: String,
    #[serde(alias = "vmodel")]
    pub virtual_model: String,
    #[serde(rename = "balance", alias = "strategy")]
    pub balance: Option<String>,
    pub target_provider: String,
    pub target_model: String,
    #[serde(default)]
    pub targets: Vec<CreateModelBackend>,
    pub access_control: Option<bool>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct CreateModelBackend {
    pub provider_id: String,
    pub model: String,
    pub weight: Option<i32>,
    pub priority: Option<i32>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct UpsertModelBackend {
    pub id: Option<String>,
    pub provider_id: String,
    pub model: String,
    pub weight: Option<i32>,
    pub priority: Option<i32>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct CreateApiKey {
    pub name: String,
    pub rpm: Option<i32>,
    pub rpd: Option<i32>,
    pub tpm: Option<i32>,
    pub tpd: Option<i32>,
    pub expires_at: Option<String>,
    #[serde(default, alias = "route_ids")]
    pub model_ids: Vec<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct UpdateApiKey {
    pub name: Option<String>,
    pub rpm: Option<i32>,
    pub rpd: Option<i32>,
    pub tpm: Option<i32>,
    pub tpd: Option<i32>,
    pub is_enabled: Option<bool>,
    pub expires_at: Option<String>,
    #[serde(alias = "route_ids")]
    pub model_ids: Option<Vec<String>>,
}

#[derive(Debug, Clone, Serialize, Deserialize, Default)]
pub struct LogQuery {
    pub limit: Option<i64>,
    pub offset: Option<i64>,
    pub provider: Option<String>,
    pub model: Option<String>,
    pub status_min: Option<i32>,
    pub status_max: Option<i32>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct LogPage {
    pub items: Vec<RequestLog>,
    pub total: i64,
}

#[derive(Debug, Clone, Serialize, Deserialize, Default, FromRow)]
pub struct StatsOverview {
    pub total_requests: i64,
    pub total_input_tokens: i64,
    pub total_output_tokens: i64,
    pub avg_duration_ms: f64,
    pub error_count: i64,
}

#[derive(Debug, Clone, Serialize, Deserialize, FromRow)]
pub struct StatsHourly {
    pub hour: String,
    pub request_count: i64,
    pub error_count: i64,
    pub total_input_tokens: i64,
    pub total_output_tokens: i64,
    pub avg_duration_ms: f64,
}

#[derive(Debug, Clone, Serialize, Deserialize, FromRow)]
pub struct ModelStats {
    pub model: String,
    pub request_count: i64,
    pub total_input_tokens: i64,
    pub total_output_tokens: i64,
    pub avg_duration_ms: f64,
}

#[derive(Debug, Clone, Serialize, Deserialize, FromRow)]
pub struct ProviderStats {
    pub provider: String,
    pub request_count: i64,
    pub error_count: i64,
    pub avg_duration_ms: f64,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct TestResult {
    pub success: bool,
    pub latency_ms: u64,
    pub model: Option<String>,
    pub error: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ModelCapabilities {
    pub provider: String,
    pub model_id: String,
    pub context_window: u64,
    pub embedding_length: Option<u64>,
    pub output_max_tokens: Option<u64>,
    pub tool_call: bool,
    pub reasoning: bool,
    pub input_modalities: Vec<String>,
    pub output_modalities: Vec<String>,
    pub input_cost: Option<f64>,
    pub output_cost: Option<f64>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ExportData {
    pub version: u32,
    pub providers: Vec<ExportProvider>,
    #[serde(alias = "routes")]
    pub models: Vec<ExportModel>,
    pub settings: Vec<(String, String)>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ExportProvider {
    pub name: String,
    pub vendor: Option<String>,
    pub protocol: String,
    pub base_url: String,
    #[serde(default, skip_serializing)]
    pub default_protocol: String,
    #[serde(default, skip_serializing)]
    pub protocol_endpoints: String,
    pub preset_key: Option<String>,
    pub channel: Option<String>,
    #[serde(alias = "modelsEndpoint")]
    pub models_source: Option<String>,
    pub static_models: Option<String>,
    pub api_key: String,
    #[serde(default = "default_provider_auth_mode")]
    pub auth_mode: String,
    #[serde(default)]
    pub use_proxy: bool,
    pub is_enabled: bool,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ExportModel {
    pub name: String,
    pub virtual_model: String,
    pub target_model: String,
    #[serde(default)]
    pub access_control: bool,
    pub is_enabled: bool,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ImportResult {
    pub providers_imported: u32,
    #[serde(alias = "routes_imported")]
    pub models_imported: u32,
    pub settings_imported: u32,
}

impl Provider {
    pub fn effective_auth_mode(&self) -> String {
        resolve_preset_channel_auth_mode(self.preset_key.as_deref(), self.channel.as_deref())
            .unwrap_or_else(|| {
                let mode = self.auth_mode.trim();
                if mode.is_empty() {
                    default_provider_auth_mode()
                } else {
                    mode.to_string()
                }
            })
    }

    pub fn effective_models_source(&self) -> Option<&str> {
        self.models_source
            .as_deref()
            .filter(|v| !v.trim().is_empty())
    }
}

impl CreateProvider {
    pub fn effective_models_source(&self) -> Option<&str> {
        self.models_source
            .as_deref()
            .filter(|v| !v.trim().is_empty())
    }
}

impl UpdateProvider {
    pub fn effective_models_source(&self) -> Option<&str> {
        self.models_source
            .as_deref()
            .filter(|v| !v.trim().is_empty())
    }
}
