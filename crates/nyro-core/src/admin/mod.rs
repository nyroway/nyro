use anyhow::Context;
use chrono::{DateTime, Utc};
use std::collections::HashMap;
use std::path::{Path, PathBuf};
use std::time::{Duration, Instant};

use reqwest::header::{AUTHORIZATION, HeaderMap, HeaderValue};
use serde::Serialize;
use serde_json::Value;

use crate::Gateway;
use crate::auth;
use crate::auth::types::{
    AuthBindingStatus, AuthPollState, AuthScheme, AuthSession, AuthSessionInitData,
    AuthSessionStatus, AuthSessionStatusData, CredentialBundle, ExchangeAuthContext,
    RefreshAuthContext, RuntimeBinding, StartAuthContext, StoredCredential, UpdateAuthSession,
};
use crate::db::models::*;
use crate::protocol::ProviderProtocols;
use crate::protocol::ids::OPENAI_CHAT_COMPLETIONS_V1;
use crate::protocol::ids::OPENAI_EMBEDDINGS_V1;
use crate::provider::metadata::CapabilitiesSource;
use crate::provider::{VendorCtx, VendorRegistry, vertexai};
use crate::proxy::client::ProxyClient;
use crate::router::TargetSelector;
use crate::storage::traits::ProviderTestResult;

const MODELS_DEV_SNAPSHOT: &str = include_str!("../../assets/models.dev.json");
const MODELS_DEV_RUNTIME_FILE: &str = "models.dev.json";
const MODELS_DEV_SOURCE_URL: &str = "https://models.dev/api.json";
const MODELS_DEV_RUNTIME_TTL: Duration = Duration::from_secs(24 * 60 * 60);

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

    // ── Providers ──

    pub async fn list_providers(&self) -> anyhow::Result<Vec<Provider>> {
        self.gw.storage.providers().list().await
    }

    pub async fn list_provider_presets(&self) -> anyhow::Result<Vec<Value>> {
        parse_provider_presets_snapshot()
    }

    pub async fn get_provider(&self, id: &str) -> anyhow::Result<Provider> {
        self.gw
            .storage
            .providers()
            .get(id)
            .await?
            .ok_or_else(|| anyhow::anyhow!("provider not found: {id}"))
    }

    pub async fn init_oauth_session(
        &self,
        vendor: &str,
        use_proxy: bool,
    ) -> anyhow::Result<AuthSessionInitData> {
        let driver_key = auth::normalize_driver_key(vendor);
        if driver_key.is_empty() {
            anyhow::bail!("auth vendor cannot be empty");
        }
        let driver = auth::build_driver(&driver_key)
            .ok_or_else(|| anyhow::anyhow!("auth vendor not implemented: {driver_key}"))?;
        let client = self.gw.http_client_for_provider(use_proxy).await?;
        let created = driver
            .start(StartAuthContext {
                use_proxy,
                http_client: Some(client),
                ..Default::default()
            })
            .await?;
        let session = self.create_auth_session_record(created).await?;
        build_auth_session_init_data(&session)
    }

    pub async fn get_oauth_session_status(
        &self,
        session_id: &str,
    ) -> anyhow::Result<AuthSessionStatusData> {
        let session = self
            .get_auth_session_record(session_id)
            .await?
            .ok_or_else(|| anyhow::anyhow!("auth session not found: {session_id}"))?;

        if is_expired_at(session.expires_at.as_deref()) {
            self.delete_auth_session_record(&session.id).await?;
            return Ok(AuthSessionStatusData::Error {
                code: "AUTH_TIMEOUT".to_string(),
                message: "auth session expired".to_string(),
            });
        }

        match session.status.as_str() {
            "ready" => {
                let bundle = parse_auth_session_bundle(&session)?;
                return Ok(build_auth_session_ready_data(&session, &bundle));
            }
            "error" => {
                let message = session
                    .last_error
                    .filter(|value| !value.trim().is_empty())
                    .unwrap_or_else(|| "auth session failed".to_string());
                self.delete_auth_session_record(&session.id).await?;
                return Ok(AuthSessionStatusData::Error {
                    code: "AUTH_SESSION_ERROR".to_string(),
                    message,
                });
            }
            "cancelled" => {
                self.delete_auth_session_record(&session.id).await?;
                return Ok(AuthSessionStatusData::Error {
                    code: "AUTH_SESSION_CANCELLED".to_string(),
                    message: "auth session cancelled".to_string(),
                });
            }
            _ => {}
        }

        if session.scheme == AuthScheme::OAuthAuthCodePkce.as_str()
            || session.scheme == AuthScheme::SetupToken.as_str()
        {
            return Ok(build_auth_session_pending_data(&session));
        }

        let driver = auth::build_driver(&session.driver_key).ok_or_else(|| {
            anyhow::anyhow!("auth vendor not implemented: {}", session.driver_key)
        })?;
        let client = self.gw.http_client_for_provider(session.use_proxy).await?;

        match driver
            .poll(
                &session,
                RefreshAuthContext {
                    use_proxy: session.use_proxy,
                    http_client: Some(client),
                    ..Default::default()
                },
            )
            .await?
        {
            AuthPollState::Pending(progress) => {
                let updated = self
                    .update_auth_session_record(
                        &session.id,
                        UpdateAuthSession {
                            user_code: progress.user_code,
                            verification_uri: progress.verification_uri,
                            verification_uri_complete: progress.verification_uri_complete,
                            expires_at: progress.expires_at,
                            poll_interval_seconds: progress.poll_interval_seconds,
                            ..Default::default()
                        },
                    )
                    .await?;
                Ok(build_auth_session_pending_data(&updated))
            }
            AuthPollState::Ready(bundle) => {
                let updated = self
                    .update_auth_session_record(
                        &session.id,
                        UpdateAuthSession {
                            status: Some(AuthSessionStatus::Ready.as_str().to_string()),
                            result_json: Some(serde_json::to_string(&bundle)?),
                            expires_at: bundle.expires_at.clone(),
                            last_error: Some(String::new()),
                            ..Default::default()
                        },
                    )
                    .await?;
                Ok(build_auth_session_ready_data(&updated, &bundle))
            }
            AuthPollState::Error { code, message } => {
                self.delete_auth_session_record(&session.id).await?;
                Ok(AuthSessionStatusData::Error { code, message })
            }
        }
    }

    pub async fn cancel_oauth_session(&self, session_id: &str) -> anyhow::Result<()> {
        self.delete_auth_session_record(session_id).await
    }

    pub async fn complete_oauth_session(
        &self,
        session_id: &str,
        input: auth::AuthExchangeInput,
    ) -> anyhow::Result<AuthSessionStatusData> {
        let session = self
            .get_auth_session_record(session_id)
            .await?
            .ok_or_else(|| anyhow::anyhow!("auth session not found: {session_id}"))?;
        if !session
            .status
            .eq_ignore_ascii_case(AuthSessionStatus::Pending.as_str())
        {
            anyhow::bail!("auth session is not pending");
        }
        if is_expired_at(session.expires_at.as_deref()) {
            self.delete_auth_session_record(&session.id).await?;
            anyhow::bail!("auth session expired");
        }

        let driver = auth::build_driver(&session.driver_key).ok_or_else(|| {
            anyhow::anyhow!("auth vendor not implemented: {}", session.driver_key)
        })?;

        let client = self.gw.http_client_for_provider(session.use_proxy).await?;
        let bundle = match driver
            .exchange(
                &session,
                input,
                ExchangeAuthContext {
                    use_proxy: session.use_proxy,
                    http_client: Some(client),
                    ..Default::default()
                },
            )
            .await
        {
            Ok(bundle) => bundle,
            Err(error) => {
                self.delete_auth_session_record(&session.id).await?;
                return Err(error);
            }
        };
        let updated = self
            .update_auth_session_record(
                &session.id,
                UpdateAuthSession {
                    status: Some(AuthSessionStatus::Ready.as_str().to_string()),
                    result_json: Some(serde_json::to_string(&bundle)?),
                    expires_at: bundle.expires_at.clone(),
                    last_error: Some(String::new()),
                    ..Default::default()
                },
            )
            .await?;

        Ok(build_auth_session_ready_data(&updated, &bundle))
    }

    pub async fn create_provider_with_oauth_session(
        &self,
        session_id: &str,
        mut input: CreateProvider,
    ) -> anyhow::Result<Provider> {
        let session = self.take_ready_auth_session_record(session_id).await?;
        if is_expired_at(session.expires_at.as_deref()) {
            anyhow::bail!("auth session expired");
        }

        let bundle = parse_auth_session_bundle(&session)?;
        bundle
            .access_token
            .as_deref()
            .filter(|value| !value.trim().is_empty())
            .ok_or_else(|| anyhow::anyhow!("auth session missing access token"))?;

        if input.vendor.as_deref().unwrap_or("").trim().is_empty() {
            input.vendor = Some(session.driver_key.clone());
        }
        input.auth_mode = "oauth".to_string();

        let provider = match self.create_provider(input).await {
            Ok(provider) => provider,
            Err(error) => {
                self.restore_auth_session_record(session).await?;
                return Err(error);
            }
        };

        let credential_input =
            upsert_credential_from_bundle(&session.driver_key, &session.scheme, &bundle);
        let provisioned = async {
            self.gw
                .storage
                .oauth_credentials()
                .upsert(&provider.id, credential_input)
                .await?;
            let credential =
                stored_credential_from_bundle(&session.driver_key, &session.scheme, &bundle);
            self.sync_provider_runtime_fields(&provider, &credential)
                .await
        }
        .await;

        let provider = match provisioned {
            Ok(provider) => provider,
            Err(error) => {
                if let Err(cleanup_error) = self.delete_provider(&provider.id).await {
                    tracing::warn!(
                        "failed to rollback oauth provider {} after provisioning error: {}",
                        provider.id,
                        cleanup_error
                    );
                }
                self.restore_auth_session_record(session).await?;
                return Err(error.context("create oauth provider"));
            }
        };

        Ok(provider)
    }

    pub async fn get_provider_oauth_status(
        &self,
        id: &str,
    ) -> anyhow::Result<ProviderOAuthStatusData> {
        let provider = self.get_provider(id).await?;
        let driver_key = provider
            .vendor
            .as_deref()
            .map(auth::normalize_driver_key)
            .unwrap_or_default();

        if driver_key.is_empty() {
            return Ok(build_provider_oauth_status(&provider, "", None, None));
        }

        let oauth_cred = self.gw.storage.oauth_credentials().get(id).await?;
        match oauth_cred {
            Some(cred) => Ok(build_provider_oauth_status_from_credential(
                &provider,
                &driver_key,
                &cred,
            )),
            None => Ok(build_provider_oauth_status(
                &provider,
                &driver_key,
                None,
                None,
            )),
        }
    }

    pub async fn reconnect_provider_oauth(
        &self,
        id: &str,
    ) -> anyhow::Result<ProviderOAuthStatusData> {
        let provider = self.get_provider(id).await?;
        let driver_key = provider
            .vendor
            .as_deref()
            .map(auth::normalize_driver_key)
            .unwrap_or_default();

        if driver_key.is_empty() {
            anyhow::bail!("provider vendor is empty");
        }
        let driver = auth::build_driver(&driver_key)
            .ok_or_else(|| anyhow::anyhow!("auth vendor not implemented: {driver_key}"))?;
        if !driver.metadata().supports_existing_provider {
            anyhow::bail!("auth vendor does not support reconnect: {driver_key}");
        }

        let oauth_cred = self
            .gw
            .storage
            .oauth_credentials()
            .get(&provider.id)
            .await?
            .ok_or_else(|| anyhow::anyhow!("provider oauth credential not found"))?;

        let credential = stored_credential_from_oauth(&oauth_cred, &driver_key);
        let refresh_token = credential
            .refresh_token
            .as_deref()
            .unwrap_or("")
            .trim()
            .to_string();
        if refresh_token.is_empty() {
            anyhow::bail!("provider oauth refresh token is missing");
        }

        let client = self.gw.http_client_for_provider(provider.use_proxy).await?;
        let bundle = match driver
            .refresh(
                &credential,
                RefreshAuthContext {
                    use_proxy: provider.use_proxy,
                    http_client: Some(client),
                    ..Default::default()
                },
            )
            .await
        {
            Ok(bundle) => bundle,
            Err(error) => {
                let _ = self
                    .gw
                    .storage
                    .oauth_credentials()
                    .fail_refresh(&provider.id, &error.to_string())
                    .await;
                return Ok(build_provider_oauth_status(
                    &provider,
                    &driver_key,
                    Some(AuthBindingStatus::Error.as_str().to_string()),
                    Some(error.to_string()),
                ));
            }
        };

        let refreshed_credential =
            stored_credential_from_bundle(&driver_key, driver.metadata().scheme.as_str(), &bundle);
        let credential_input =
            upsert_credential_from_bundle(&driver_key, driver.metadata().scheme.as_str(), &bundle);
        self.gw
            .storage
            .oauth_credentials()
            .upsert(&provider.id, credential_input)
            .await?;
        let refreshed_provider = self
            .sync_provider_runtime_fields(&provider, &refreshed_credential)
            .await?;

        Ok(build_provider_oauth_status(
            &refreshed_provider,
            &driver_key,
            Some(AuthBindingStatus::Connected.as_str().to_string()),
            None,
        ))
    }

    pub async fn logout_provider_oauth(&self, id: &str) -> anyhow::Result<ProviderOAuthStatusData> {
        let provider = self.get_provider(id).await?;
        let driver_key = provider
            .vendor
            .as_deref()
            .map(auth::normalize_driver_key)
            .unwrap_or_default();

        if driver_key.is_empty() {
            return Ok(build_provider_oauth_status(&provider, "", None, None));
        }

        self.gw
            .storage
            .oauth_credentials()
            .delete(&provider.id)
            .await?;

        let updated = self
            .gw
            .storage
            .providers()
            .update(
                &provider.id,
                UpdateProvider {
                    auth_mode: Some("oauth".to_string()),
                    api_key: Some(String::new()),
                    ..Default::default()
                },
            )
            .await?;

        Ok(build_provider_oauth_status(
            &updated,
            &driver_key,
            Some(AuthBindingStatus::Disconnected.as_str().to_string()),
            None,
        ))
    }

    pub async fn bind_provider_with_oauth_session(
        &self,
        provider_id: &str,
        session_id: &str,
    ) -> anyhow::Result<Provider> {
        let provider = self.get_provider(provider_id).await?;
        let session = self.take_ready_auth_session_record(session_id).await?;
        if is_expired_at(session.expires_at.as_deref()) {
            anyhow::bail!("auth session expired");
        }

        let bundle = parse_auth_session_bundle(&session)?;
        bundle
            .access_token
            .as_deref()
            .filter(|value| !value.trim().is_empty())
            .ok_or_else(|| anyhow::anyhow!("auth session missing access token"))?;

        let credential =
            stored_credential_from_bundle(&session.driver_key, &session.scheme, &bundle);
        let credential_input =
            upsert_credential_from_bundle(&session.driver_key, &session.scheme, &bundle);
        match self
            .gw
            .storage
            .oauth_credentials()
            .upsert(&provider.id, credential_input)
            .await
        {
            Ok(_) => {}
            Err(error) => {
                self.restore_auth_session_record(session).await?;
                return Err(error);
            }
        }
        let provider = match self
            .sync_provider_runtime_fields(&provider, &credential)
            .await
        {
            Ok(provider) => provider,
            Err(error) => {
                let _ = self
                    .gw
                    .storage
                    .oauth_credentials()
                    .delete(&provider.id)
                    .await;
                self.restore_auth_session_record(session).await?;
                return Err(error);
            }
        };

        Ok(provider)
    }

    pub async fn create_provider(&self, input: CreateProvider) -> anyhow::Result<Provider> {
        let name = normalize_name(&input.name, "provider name")?;
        self.ensure_provider_name_unique(None, &name).await?;
        let vendor = normalize_vendor(input.vendor.as_deref());
        let auth_mode =
            resolve_preset_channel_auth_mode(input.preset_key.as_deref(), input.channel.as_deref())
                .unwrap_or(input.auth_mode);
        let api_key = if auth_mode == "oauth" {
            String::new()
        } else {
            input.api_key
        };
        self.gw
            .storage
            .providers()
            .create(CreateProvider {
                name,
                vendor,
                protocol: input.protocol,
                base_url: input.base_url,
                preset_key: input.preset_key,
                channel: input.channel,
                models_source: input.models_source,
                static_models: input.static_models,
                api_key,
                auth_mode,
                use_proxy: input.use_proxy,
            })
            .await
    }

    pub async fn copy_provider(&self, id: &str) -> anyhow::Result<Provider> {
        let original = self.get_provider(id).await?;
        let name = self.next_provider_copy_name(&original.name).await?;
        let copied = self
            .create_provider(CreateProvider {
                name,
                vendor: original.vendor.clone(),
                protocol: original.protocol.clone(),
                base_url: original.base_url.clone(),
                preset_key: original.preset_key.clone(),
                channel: original.channel.clone(),
                models_source: original.models_source.clone(),
                static_models: original.static_models.clone(),
                api_key: original.api_key.clone(),
                auth_mode: original.auth_mode.clone(),
                use_proxy: original.use_proxy,
            })
            .await?;

        let copied = if original.effective_auth_mode() == "oauth" {
            match self
                .gw
                .storage
                .oauth_credentials()
                .get(&original.id)
                .await?
            {
                Some(credential) => {
                    let credential_input = upsert_credential_from_oauth(&credential);
                    let provisioned = async {
                        self.gw
                            .storage
                            .oauth_credentials()
                            .upsert(&copied.id, credential_input)
                            .await?;
                        let driver_key = credential.driver_key.clone();
                        let stored = stored_credential_from_oauth(&credential, &driver_key);
                        self.sync_provider_runtime_fields(&copied, &stored).await
                    }
                    .await;

                    match provisioned {
                        Ok(provider) => provider,
                        Err(error) => {
                            if let Err(cleanup_error) = self.delete_provider(&copied.id).await {
                                tracing::warn!(
                                    "failed to rollback copied oauth provider {} after provisioning error: {}",
                                    copied.id,
                                    cleanup_error
                                );
                            }
                            return Err(error.context("copy oauth provider"));
                        }
                    }
                }
                None => copied,
            }
        } else {
            copied
        };

        Ok(copied)
    }

    pub async fn update_provider(
        &self,
        id: &str,
        input: UpdateProvider,
    ) -> anyhow::Result<Provider> {
        let current = self.get_provider(id).await?;
        let current_base_url = current.base_url.clone();
        let models_source_input = input.models_source.map(|value| value.trim().to_string());

        let name = normalize_name(&input.name.unwrap_or(current.name), "provider name")?;
        self.ensure_provider_name_unique(Some(id), &name).await?;
        let vendor = if input.vendor.is_some() {
            normalize_vendor(input.vendor.as_deref())
        } else {
            normalize_vendor(current.vendor.as_deref())
        };
        let models_source = models_source_input
            .or_else(|| current.models_source.as_deref().map(ToString::to_string));
        let protocol = input.protocol.unwrap_or(current.protocol);
        let base_url = input.base_url.unwrap_or(current.base_url);
        let preset_key = input.preset_key.or(current.preset_key);
        let channel = input.channel.or(current.channel);
        let static_models = input.static_models.or(current.static_models);
        let api_key = input.api_key.unwrap_or(current.api_key);
        let auth_mode = resolve_preset_channel_auth_mode(preset_key.as_deref(), channel.as_deref())
            .or(input.auth_mode)
            .unwrap_or(current.auth_mode);
        let api_key = if auth_mode == "oauth" {
            Some(String::new())
        } else {
            Some(api_key)
        };
        let use_proxy = input.use_proxy.unwrap_or(current.use_proxy);
        let is_enabled = input.is_enabled.unwrap_or(current.is_enabled);
        let base_url_changed = base_url != current_base_url;

        let provider = self
            .gw
            .storage
            .providers()
            .update(
                id,
                UpdateProvider {
                    name: Some(name),
                    vendor,
                    protocol: Some(protocol),
                    base_url: Some(base_url),
                    preset_key,
                    channel,
                    models_source,
                    static_models,
                    api_key,
                    auth_mode: Some(auth_mode),
                    use_proxy: Some(use_proxy),
                    is_enabled: Some(is_enabled),
                },
            )
            .await?;

        if base_url_changed {
            self.gw.clear_ollama_capability_cache_for_provider(id).await;
        }

        Ok(provider)
    }

    pub async fn delete_provider(&self, id: &str) -> anyhow::Result<()> {
        self.gw.storage.providers().delete(id).await?;
        self.gw.clear_ollama_capability_cache_for_provider(id).await;
        Ok(())
    }

    pub async fn test_provider(&self, id: &str) -> anyhow::Result<TestResult> {
        let provider = self.get_provider(id).await?;
        self.gw
            .clear_ollama_capability_cache_for_provider(&provider.id)
            .await;
        let start = Instant::now();
        let protocol = provider.protocol.trim();
        let vertex_runtime = if vertexai::is_vertex_vendor(&provider) {
            Some(self.resolve_provider_runtime(&provider).await?)
        } else {
            None
        };
        let base_url_owned = vertex_runtime
            .as_ref()
            .and_then(|runtime| runtime.binding.base_url_override.as_deref())
            .map(str::to_string)
            .unwrap_or_else(|| provider.base_url.clone());
        let base_url = base_url_owned.trim();

        let result = if base_url.is_empty() {
            TestResult {
                success: false,
                latency_ms: 0,
                model: None,
                error: Some("Base URL is empty".to_string()),
            }
        } else {
            let mut failures: Vec<String> = Vec::new();
            if reqwest::Url::parse(base_url).is_err() {
                failures.push(format!("{protocol}: Base URL format is invalid"));
            } else {
                let mut request = self
                    .gw
                    .http_client
                    .get(base_url)
                    .timeout(Duration::from_secs(10));
                if let Some(runtime) = &vertex_runtime {
                    let mut headers = runtime_binding_headers(&runtime.binding)?;
                    if !runtime.binding.disable_default_auth {
                        headers.insert(
                            AUTHORIZATION,
                            HeaderValue::from_str(&format!("Bearer {}", runtime.access_token))?,
                        );
                    }
                    request = request.headers(headers);
                }
                if let Err(e) = request.send().await {
                    failures.push(format!("{protocol}: {}", format_connectivity_error(&e)));
                }
            }

            if failures.is_empty() {
                TestResult {
                    success: true,
                    latency_ms: start.elapsed().as_millis() as u64,
                    model: None,
                    error: None,
                }
            } else {
                TestResult {
                    success: false,
                    latency_ms: start.elapsed().as_millis() as u64,
                    model: None,
                    error: Some(format!(
                        "Connectivity check failed for provider endpoint: {}",
                        failures.join("; ")
                    )),
                }
            }
        };
        self.record_provider_test_result(&provider.id, &result)
            .await?;
        Ok(result)
    }

    async fn record_provider_test_result(
        &self,
        provider_id: &str,
        result: &TestResult,
    ) -> anyhow::Result<()> {
        self.gw
            .storage
            .providers()
            .record_test_result(
                provider_id,
                ProviderTestResult {
                    success: result.success,
                    tested_at: String::new(),
                },
            )
            .await
    }

    pub async fn test_provider_models(&self, id: &str) -> anyhow::Result<Vec<String>> {
        let provider = self.get_provider(id).await?;
        let runtime = self.resolve_provider_runtime(&provider).await?;
        let credential = runtime.access_token.clone();
        if let Some(static_list) = runtime.binding.static_models_override.as_deref() {
            let models: Vec<String> = static_list
                .iter()
                .map(|s| s.trim().to_string())
                .filter(|s| !s.is_empty())
                .collect();
            if !models.is_empty() {
                return Ok(models);
            }
        }
        let endpoint = runtime
            .binding
            .models_source_override
            .clone()
            .or_else(|| provider.effective_models_source().map(ToString::to_string))
            .map(|value| value.trim().to_string())
            .filter(|value| !value.is_empty())
            .ok_or_else(|| anyhow::anyhow!("Model Discovery URL is empty"))?;

        if let Some(models) = lookup_models_dev_models(&self.gw.config.data_dir, &endpoint)? {
            if models.is_empty() {
                anyhow::bail!("Model list format is invalid or empty");
            }
            return Ok(models);
        }

        let mut headers = if runtime.binding.disable_default_auth {
            HeaderMap::new()
        } else {
            build_model_headers(&provider.protocol, provider.vendor.as_deref(), &credential)?
        };
        headers.extend(runtime_binding_headers(&runtime.binding)?);
        let mut request = self
            .gw
            .http_client
            .get(&endpoint)
            .headers(headers)
            .timeout(Duration::from_secs(10));

        if provider.protocol == "gemini" && !runtime.binding.disable_default_auth {
            let separator = if endpoint.contains('?') { '&' } else { '?' };
            let mut headers =
                build_model_headers(&provider.protocol, provider.vendor.as_deref(), &credential)?;
            headers.extend(runtime_binding_headers(&runtime.binding)?);
            request = self
                .gw
                .http_client
                .get(format!("{endpoint}{separator}key={}", credential))
                .headers(headers)
                .timeout(Duration::from_secs(10));
        }

        let resp = request
            .send()
            .await
            .map_err(|e| anyhow::anyhow!(format_connectivity_error(&e)))?;
        if !resp.status().is_success() {
            let status = resp.status().as_u16();
            let body = resp.text().await.unwrap_or_default();
            let preview = body.chars().take(200).collect::<String>();
            anyhow::bail!("HTTP {status}: {preview}");
        }

        let json: Value = resp.json().await.unwrap_or_default();
        let models =
            extract_models_from_response(&provider.protocol, provider.vendor.as_deref(), &json);
        if models.is_empty() {
            anyhow::bail!("Model list format is invalid or empty");
        }

        Ok(models)
    }

    pub async fn get_provider_models(&self, id: &str) -> anyhow::Result<Vec<String>> {
        let provider = self.get_provider(id).await?;
        let runtime = self.resolve_provider_runtime(&provider).await?;
        let credential = runtime.access_token.clone();
        if let Some(static_list) = runtime.binding.static_models_override.as_deref() {
            let models: Vec<String> = static_list
                .iter()
                .map(|s| s.trim().to_string())
                .filter(|s| !s.is_empty())
                .collect();
            if !models.is_empty() {
                return Ok(models);
            }
        }

        if let Some(endpoint) = runtime
            .binding
            .models_source_override
            .clone()
            .or_else(|| resolve_models_endpoint(&provider))
        {
            if let Some(models) = lookup_models_dev_models(&self.gw.config.data_dir, &endpoint)?
                && !models.is_empty()
            {
                return Ok(models);
            }

            let mut headers = if runtime.binding.disable_default_auth {
                HeaderMap::new()
            } else {
                build_model_headers(&provider.protocol, provider.vendor.as_deref(), &credential)?
            };
            headers.extend(runtime_binding_headers(&runtime.binding)?);
            let mut request = self.gw.http_client.get(&endpoint).headers(headers);

            if provider.protocol == "gemini" && !runtime.binding.disable_default_auth {
                let separator = if endpoint.contains('?') { '&' } else { '?' };
                let mut headers = build_model_headers(
                    &provider.protocol,
                    provider.vendor.as_deref(),
                    &credential,
                )?;
                headers.extend(runtime_binding_headers(&runtime.binding)?);
                request = self
                    .gw
                    .http_client
                    .get(format!("{endpoint}{separator}key={}", credential))
                    .headers(headers);
            }

            if let Ok(resp) = request.send().await
                && resp.status().is_success()
            {
                let json: Value = resp.json().await.unwrap_or_default();
                let models = extract_models_from_response(
                    &provider.protocol,
                    provider.vendor.as_deref(),
                    &json,
                );
                if !models.is_empty() {
                    return Ok(models);
                }
            }
        }

        Ok(parse_static_models(provider.static_models.as_deref()))
    }

    pub async fn get_model_capabilities(
        &self,
        provider_id: &str,
        model: &str,
    ) -> anyhow::Result<ModelCapabilities> {
        let provider = self.get_provider(provider_id).await?;
        let trimmed_model = model.trim();
        if trimmed_model.is_empty() {
            anyhow::bail!("model cannot be empty");
        }
        self.resolve_provider_model_capabilities(&provider, trimmed_model)
            .await
    }

    pub async fn detect_embedding_dimensions(&self, embedding_route: &str) -> anyhow::Result<u64> {
        let route_name = embedding_route.trim();
        if route_name.is_empty() {
            anyhow::bail!("embedding_route cannot be empty");
        }

        let route = {
            let cache = self.gw.route_cache.read().await;
            cache.match_route(route_name).cloned()
        }
        .ok_or_else(|| anyhow::anyhow!("embedding route not found: {route_name}"))?;
        let targets = load_route_targets_for_probe(&self.gw, &route).await;
        if targets.is_empty() {
            anyhow::bail!("embedding route has no targets: {route_name}");
        }
        let ordered_targets = TargetSelector::select_ordered(&route.strategy, &targets);
        let mut missing_openai_endpoint = false;

        for target in ordered_targets {
            let provider = match self.gw.storage.providers().get(&target.provider_id).await? {
                Some(provider) if provider.is_enabled => provider,
                _ => continue,
            };
            let runtime = match self.resolve_provider_runtime(&provider).await {
                Ok(runtime) => runtime,
                Err(_) => continue,
            };
            let Some(openai_base_url) = runtime
                .binding
                .base_url_override
                .clone()
                .filter(|value| !value.trim().is_empty())
                .or_else(|| resolve_openai_base_url(&provider))
            else {
                missing_openai_endpoint = true;
                continue;
            };
            let actual_model = if target.model.is_empty() || target.model == "*" {
                route_name.to_string()
            } else {
                target.model.clone()
            };
            let extension = match VendorRegistry::global().resolve(&provider, OPENAI_EMBEDDINGS_V1)
            {
                Some(ext) => ext.clone(),
                None => continue,
            };
            let credential = runtime.access_token.clone();
            let upstream_url;
            let mut request_headers;
            {
                let ctx = VendorCtx {
                    provider: &provider,
                    protocol_id: OPENAI_EMBEDDINGS_V1,
                    api_key: &credential,
                    actual_model: &actual_model,
                    credential: None,
                };
                upstream_url = extension.build_url(&ctx, &openai_base_url, "/v1/embeddings");
                request_headers = match runtime_binding_headers(&runtime.binding) {
                    Ok(headers) => headers,
                    Err(_) => continue,
                };
                if !runtime.binding.disable_default_auth {
                    request_headers.extend(extension.auth_headers(&ctx));
                }
            }
            let client = match self.gw.http_client_for_provider(provider.use_proxy).await {
                Ok(http_client) => ProxyClient::new(http_client),
                Err(_) => continue,
            };
            let call = client
                .call_non_stream(
                    &upstream_url,
                    request_headers,
                    serde_json::json!({
                        "model": actual_model,
                        "input": "nyro.embedding.dimensions.probe",
                    }),
                )
                .await;
            if let Ok((payload, status, _)) = call
                && status < 400
                && let Some(dims) = parse_embedding_dimensions_from_payload(&payload)
            {
                return Ok(dims);
            }
        }

        if missing_openai_endpoint {
            anyhow::bail!("embedding route targets must expose openai protocol");
        }
        anyhow::bail!("failed to detect embedding dimensions for route: {route_name}")
    }

    async fn resolve_provider_model_capabilities(
        &self,
        provider: &Provider,
        model: &str,
    ) -> anyhow::Result<ModelCapabilities> {
        match preset_capabilities_source(provider) {
            CapabilitiesSource::ModelsDev(vendor_key) => {
                let matched =
                    lookup_models_dev_capability(&self.gw.config.data_dir, vendor_key, model);
                matched.ok_or_else(|| {
                    anyhow::anyhow!("no matched model capabilities found in models.dev")
                })
            }
            CapabilitiesSource::Http(url) => {
                if is_ollama_show_endpoint(url) {
                    self.query_ollama_show_capability(url, model).await
                } else {
                    self.query_http_capability(provider, url, model).await
                }
            }
            CapabilitiesSource::Auto => Ok(fuzzy_match_models_dev(&self.gw.config.data_dir, model)
                .ok_or_else(|| {
                    anyhow::anyhow!("no matched model capabilities found in auto mode")
                })?),
        }
    }

    async fn query_http_capability(
        &self,
        provider: &Provider,
        url: &str,
        model: &str,
    ) -> anyhow::Result<ModelCapabilities> {
        let runtime = self.resolve_provider_runtime(provider).await?;
        let credential = runtime.access_token;
        let mut headers = if runtime.binding.disable_default_auth {
            HeaderMap::new()
        } else {
            build_model_headers(&provider.protocol, provider.vendor.as_deref(), &credential)?
        };
        headers.extend(runtime_binding_headers(&runtime.binding)?);
        let mut request = self
            .gw
            .http_client
            .get(url)
            .headers(headers)
            .timeout(Duration::from_secs(10));

        if provider.protocol == "gemini" && !runtime.binding.disable_default_auth {
            let separator = if url.contains('?') { '&' } else { '?' };
            let mut headers =
                build_model_headers(&provider.protocol, provider.vendor.as_deref(), &credential)?;
            headers.extend(runtime_binding_headers(&runtime.binding)?);
            request = self
                .gw
                .http_client
                .get(format!("{url}{separator}key={}", credential))
                .headers(headers)
                .timeout(Duration::from_secs(10));
        }

        let resp = request
            .send()
            .await
            .map_err(|e| anyhow::anyhow!(format_connectivity_error(&e)))?;
        if !resp.status().is_success() {
            anyhow::bail!("capability source returned status {}", resp.status());
        }
        let json: Value = resp.json().await.unwrap_or_default();
        if let Some(cap) = parse_http_capability(&json, model) {
            return Ok(cap);
        }
        anyhow::bail!("no matched model capabilities found from capability source")
    }

    async fn query_ollama_show_capability(
        &self,
        url: &str,
        model: &str,
    ) -> anyhow::Result<ModelCapabilities> {
        let resp = self
            .gw
            .http_client
            .post(url)
            .json(&serde_json::json!({ "name": model }))
            .timeout(Duration::from_secs(10))
            .send()
            .await
            .map_err(|e| anyhow::anyhow!(format_connectivity_error(&e)))?;
        if !resp.status().is_success() {
            anyhow::bail!("ollama /api/show returned status {}", resp.status());
        }
        let json: Value = resp.json().await.unwrap_or_default();
        Ok(parse_ollama_capability(&json, model))
    }

    // ── Routes ──

    pub async fn list_routes(&self) -> anyhow::Result<Vec<Route>> {
        let mut routes = self.gw.storage.routes().list().await?;
        if let Some(store) = self.gw.storage.route_targets() {
            for route in &mut routes {
                route.targets = store.list_targets_by_route(&route.id).await?;
                route.cache = resolve_route_cache(route);
            }
        } else {
            for route in &mut routes {
                route.cache = resolve_route_cache(route);
            }
        }
        Ok(routes)
    }

    pub async fn create_route(&self, input: CreateRoute) -> anyhow::Result<Route> {
        let name = normalize_name(&input.name, "route name")?;
        self.ensure_route_name_unique(None, &name).await?;
        ensure_virtual_model(&input.virtual_model)?;
        self.ensure_route_unique(None, &input.virtual_model).await?;
        let strategy = normalize_route_strategy(input.strategy.as_deref())?;
        let targets = normalize_create_route_targets(&input)?;
        ensure_route_targets_valid(&targets)?;
        let primary_target = targets
            .first()
            .ok_or_else(|| anyhow::anyhow!("at least one route target is required"))?;
        let (cache_exact_ttl, cache_semantic_ttl, cache_semantic_threshold) =
            flatten_route_cache_columns(input.cache.as_ref());

        let route = self
            .gw
            .storage
            .routes()
            .create(CreateRoute {
                name,
                virtual_model: input.virtual_model,
                strategy: Some(strategy),
                target_provider: primary_target.provider_id.clone(),
                target_model: primary_target.model.clone(),
                targets: vec![],
                access_control: input.access_control,
                cache: None,
                cache_exact_ttl,
                cache_semantic_ttl,
                cache_semantic_threshold,
            })
            .await?;
        if let Some(store) = self.gw.storage.route_targets() {
            store.set_targets(&route.id, &targets).await?;
        }
        self.reload_route_cache().await?;
        self.get_route_by_id(&route.id).await
    }

    pub async fn update_route(&self, id: &str, input: UpdateRoute) -> anyhow::Result<Route> {
        let current = self.get_route_by_id(id).await?;

        let name = normalize_name(
            &input.name.clone().unwrap_or_else(|| current.name.clone()),
            "route name",
        )?;
        self.ensure_route_name_unique(Some(id), &name).await?;
        let virtual_model = input
            .virtual_model
            .clone()
            .unwrap_or_else(|| current.virtual_model.clone());
        let strategy =
            normalize_route_strategy(input.strategy.as_deref().or(Some(&current.strategy)))?;
        let targets = normalize_update_route_targets(&current, &input)?;
        ensure_route_targets_valid(&targets)?;
        let primary_target = targets
            .first()
            .ok_or_else(|| anyhow::anyhow!("at least one route target is required"))?;
        let access_control = input.access_control.unwrap_or(current.access_control);
        let is_enabled = input.is_enabled.unwrap_or(current.is_enabled);
        let (cache_exact_ttl, cache_semantic_ttl, cache_semantic_threshold) =
            if let Some(cache) = input.cache.as_ref() {
                flatten_route_cache_columns(Some(cache))
            } else {
                (
                    current.cache_exact_ttl,
                    current.cache_semantic_ttl,
                    current.cache_semantic_threshold,
                )
            };
        ensure_virtual_model(&virtual_model)?;
        self.ensure_route_unique(Some(id), &virtual_model).await?;

        self.gw
            .storage
            .routes()
            .update(
                id,
                UpdateRoute {
                    name: Some(name),
                    virtual_model: Some(virtual_model),
                    strategy: Some(strategy),
                    target_provider: Some(primary_target.provider_id.clone()),
                    target_model: Some(primary_target.model.clone()),
                    targets: None,
                    access_control: Some(access_control),
                    cache: None,
                    cache_exact_ttl,
                    cache_semantic_ttl,
                    cache_semantic_threshold,
                    is_enabled: Some(is_enabled),
                },
            )
            .await?;
        if let Some(store) = self.gw.storage.route_targets() {
            store.set_targets(id, &targets).await?;
        }
        self.reload_route_cache().await?;
        self.get_route_by_id(id).await
    }

    pub async fn delete_route(&self, id: &str) -> anyhow::Result<()> {
        if let Some(store) = self.gw.storage.route_targets() {
            store.delete_targets_by_route(id).await?;
        }
        self.gw.storage.routes().delete(id).await?;
        self.reload_route_cache().await?;
        Ok(())
    }

    // ── API Keys ──

    pub async fn list_api_keys(&self) -> anyhow::Result<Vec<ApiKeyWithBindings>> {
        self.api_keys_store()?.list().await
    }

    pub async fn get_api_key(&self, id: &str) -> anyhow::Result<ApiKeyWithBindings> {
        self.api_keys_store()?
            .get(id)
            .await?
            .context("api key not found")
    }

    pub async fn create_api_key(&self, input: CreateApiKey) -> anyhow::Result<ApiKeyWithBindings> {
        let name = normalize_name(&input.name, "api key name")?;
        self.ensure_api_key_name_unique(None, &name).await?;
        self.api_keys_store()?
            .create(CreateApiKey {
                name,
                rpm: input.rpm,
                rpd: input.rpd,
                tpm: input.tpm,
                tpd: input.tpd,
                expires_at: input.expires_at,
                route_ids: input.route_ids,
            })
            .await
    }

    pub async fn update_api_key(
        &self,
        id: &str,
        input: UpdateApiKey,
    ) -> anyhow::Result<ApiKeyWithBindings> {
        let current = self
            .api_keys_store()?
            .get(id)
            .await?
            .context("api key not found")?;

        let name = normalize_name(&input.name.unwrap_or(current.name), "api key name")?;
        self.ensure_api_key_name_unique(Some(id), &name).await?;
        let rpm = input.rpm.or(current.rpm);
        let rpd = input.rpd.or(current.rpd);
        let tpm = input.tpm.or(current.tpm);
        let tpd = input.tpd.or(current.tpd);
        let is_enabled = input.is_enabled.unwrap_or(current.is_enabled);
        let expires_at = input.expires_at.or(current.expires_at);

        self.api_keys_store()?
            .update(
                id,
                UpdateApiKey {
                    name: Some(name),
                    rpm,
                    rpd,
                    tpm,
                    tpd,
                    is_enabled: Some(is_enabled),
                    expires_at,
                    route_ids: input.route_ids,
                },
            )
            .await
    }

    pub async fn delete_api_key(&self, id: &str) -> anyhow::Result<()> {
        self.api_keys_store()?.delete(id).await?;
        Ok(())
    }

    // ── Logs ──

    pub async fn query_logs(&self, q: LogQuery) -> anyhow::Result<LogPage> {
        let mut q = q;
        q.limit = Some(q.limit.unwrap_or(50).min(500));
        q.offset = Some(q.offset.unwrap_or(0));
        self.gw.storage.logs().query(q).await
    }

    pub async fn get_log(&self, id: &str) -> anyhow::Result<Option<RequestLog>> {
        self.gw.storage.logs().find_by_id(id).await
    }

    // ── Stats ──

    fn normalize_hours(hours: Option<i32>) -> Option<i32> {
        hours.and_then(|value| (value > 0).then_some(value))
    }

    pub async fn get_stats_overview(&self, hours: Option<i32>) -> anyhow::Result<StatsOverview> {
        self.gw
            .storage
            .logs()
            .stats_overview(Self::normalize_hours(hours).map(i64::from))
            .await
    }

    pub async fn get_stats_hourly(&self, hours: i32) -> anyhow::Result<Vec<StatsHourly>> {
        self.gw
            .storage
            .logs()
            .stats_hourly(i64::from(hours.max(1)))
            .await
    }

    pub async fn get_stats_by_model(&self, hours: Option<i32>) -> anyhow::Result<Vec<ModelStats>> {
        self.gw
            .storage
            .logs()
            .stats_by_model(Self::normalize_hours(hours).map(i64::from))
            .await
    }

    pub async fn get_stats_by_provider(
        &self,
        hours: Option<i32>,
    ) -> anyhow::Result<Vec<ProviderStats>> {
        self.gw
            .storage
            .logs()
            .stats_by_provider(Self::normalize_hours(hours).map(i64::from))
            .await
    }

    // ── Settings ──

    pub async fn get_setting(&self, key: &str) -> anyhow::Result<Option<String>> {
        self.gw.storage.settings().get(key).await
    }

    pub async fn set_setting(&self, key: &str, value: &str) -> anyhow::Result<()> {
        self.gw.storage.settings().set(key, value).await
    }

    pub async fn get_cache_settings(&self) -> anyhow::Result<serde_json::Value> {
        let runtime = self.gw.effective_cache_config();
        Ok(runtime.to_admin_json())
    }

    pub async fn update_cache_settings(&self, input: serde_json::Value) -> anyhow::Result<()> {
        let parsed = crate::cache::CacheConfig::from_admin_json(&input)
            .ok_or_else(|| anyhow::anyhow!("invalid cache settings payload"))?;
        self.gw.reload_cache_runtime(parsed.clone()).await?;
        let raw = serde_json::to_string(&parsed.to_admin_json())?;
        self.gw.storage.settings().set("cache_settings", &raw).await
    }

    pub async fn flush_cache(&self) -> anyhow::Result<()> {
        let cache_backend = (**self.gw.cache_backend.load()).clone();
        if let Some(cache) = cache_backend {
            cache.flush().await?;
        }
        Ok(())
    }

    pub async fn delete_cache_key(&self, key: &str) -> anyhow::Result<()> {
        let cache_backend = (**self.gw.cache_backend.load()).clone();
        if let Some(cache) = cache_backend {
            cache.delete(key).await?;
        }
        Ok(())
    }

    pub async fn get_cache_stats(&self) -> anyhow::Result<serde_json::Value> {
        let runtime = self.gw.effective_cache_config();
        let cache_backend = (**self.gw.cache_backend.load()).clone();
        let vector_store = (**self.gw.vector_store.load()).clone();
        let healthy = if let Some(cache) = cache_backend.as_ref() {
            cache.ping().await.unwrap_or(false)
        } else {
            false
        };
        Ok(serde_json::json!({
            "exact_enabled": runtime.exact.enabled,
            "semantic_enabled": runtime.semantic.enabled,
            "backend": cache_backend.as_ref().map(|b| b.backend_name()).unwrap_or("disabled"),
            "vector_store": if vector_store.is_some() { "memory" } else { "disabled" },
            "healthy": healthy,
            "singleflight_in_flight": self.gw.cache_in_flight.len(),
        }))
    }

    // ── Config Import/Export ──

    pub async fn export_config(&self) -> anyhow::Result<ExportData> {
        let providers = self.list_providers().await?;
        let routes = self.list_routes().await?;
        let settings = self.gw.storage.settings().list_all().await?;

        Ok(ExportData {
            version: 1,
            providers: providers
                .into_iter()
                .map(|p| ExportProvider {
                    name: p.name,
                    vendor: p.vendor,
                    protocol: p.protocol,
                    base_url: p.base_url,
                    default_protocol: String::new(),
                    protocol_endpoints: String::new(),
                    preset_key: p.preset_key,
                    channel: p.channel,
                    models_source: p.models_source,
                    static_models: p.static_models,
                    api_key: p.api_key,
                    auth_mode: p.auth_mode,
                    use_proxy: p.use_proxy,
                    is_enabled: p.is_enabled,
                })
                .collect(),
            routes: routes
                .into_iter()
                .map(|r| ExportRoute {
                    name: r.name,
                    virtual_model: r.virtual_model,
                    target_model: r.target_model,
                    access_control: r.access_control,
                    is_enabled: r.is_enabled,
                })
                .collect(),
            settings: settings.into_iter().collect(),
        })
    }

    pub async fn import_config(&self, data: ExportData) -> anyhow::Result<ImportResult> {
        let mut providers_imported = 0u32;
        let mut routes_imported = 0u32;
        let mut settings_imported = 0u32;

        for p in &data.providers {
            let exists = self
                .gw
                .storage
                .providers()
                .exists_by_name(&p.name, None)
                .await
                .unwrap_or(false);

            if !exists
                && self
                    .create_provider(CreateProvider {
                        name: p.name.clone(),
                        vendor: p.vendor.clone(),
                        protocol: import_provider_protocol(p),
                        base_url: import_provider_base_url(p),
                        preset_key: p.preset_key.clone(),
                        channel: p.channel.clone(),
                        models_source: p.models_source.clone(),
                        static_models: p.static_models.clone(),
                        api_key: p.api_key.clone(),
                        auth_mode: p.auth_mode.clone(),
                        use_proxy: p.use_proxy,
                    })
                    .await
                    .is_ok()
            {
                providers_imported += 1;
            }
        }

        let fallback_provider_id = self
            .list_providers()
            .await?
            .into_iter()
            .next()
            .map(|provider| provider.id);

        for r in &data.routes {
            let exists = self
                .gw
                .storage
                .routes()
                .exists_by_name(&r.name, None)
                .await
                .unwrap_or(false);

            if !exists
                && let Some(pid) = fallback_provider_id.clone()
                && self
                    .create_route(CreateRoute {
                        name: r.name.clone(),
                        virtual_model: r.virtual_model.clone(),
                        strategy: Some("weighted".to_string()),
                        target_provider: pid,
                        target_model: r.target_model.clone(),
                        targets: vec![],
                        access_control: Some(r.access_control),
                        cache: None,
                        cache_exact_ttl: None,
                        cache_semantic_ttl: None,
                        cache_semantic_threshold: None,
                    })
                    .await
                    .is_ok()
            {
                routes_imported += 1;
            }
        }

        for (key, value) in &data.settings {
            self.set_setting(key, value).await?;
            settings_imported += 1;
        }

        Ok(ImportResult {
            providers_imported,
            routes_imported,
            settings_imported,
        })
    }

    async fn ensure_route_unique(
        &self,
        exclude_id: Option<&str>,
        virtual_model: &str,
    ) -> anyhow::Result<()> {
        if self
            .gw
            .storage
            .routes()
            .exists_by_virtual_model(virtual_model, exclude_id)
            .await?
        {
            let normalized_model = virtual_model.trim();
            anyhow::bail!("route already exists for model={normalized_model}");
        }
        Ok(())
    }

    async fn ensure_provider_name_unique(
        &self,
        exclude_id: Option<&str>,
        name: &str,
    ) -> anyhow::Result<()> {
        if self
            .gw
            .storage
            .providers()
            .exists_by_name(name, exclude_id)
            .await?
        {
            return Err(coded_error(
                "PROVIDER_NAME_CONFLICT",
                &format!("provider name already exists: {name}"),
                serde_json::json!({ "name": name }),
            ));
        }
        Ok(())
    }

    async fn next_provider_copy_name(&self, original_name: &str) -> anyhow::Result<String> {
        let base = format!("{}_Copy", normalize_name(original_name, "provider name")?);
        if !self
            .gw
            .storage
            .providers()
            .exists_by_name(&base, None)
            .await?
        {
            return Ok(base);
        }

        for index in 2.. {
            let candidate = format!("{base}{index}");
            if !self
                .gw
                .storage
                .providers()
                .exists_by_name(&candidate, None)
                .await?
            {
                return Ok(candidate);
            }
        }

        unreachable!("unbounded provider copy name search must return");
    }

    async fn ensure_route_name_unique(
        &self,
        exclude_id: Option<&str>,
        name: &str,
    ) -> anyhow::Result<()> {
        if self
            .gw
            .storage
            .routes()
            .exists_by_name(name, exclude_id)
            .await?
        {
            return Err(coded_error(
                "ROUTE_NAME_CONFLICT",
                &format!("route name already exists: {name}"),
                serde_json::json!({ "name": name }),
            ));
        }
        Ok(())
    }

    async fn ensure_api_key_name_unique(
        &self,
        exclude_id: Option<&str>,
        name: &str,
    ) -> anyhow::Result<()> {
        if self
            .api_keys_store()?
            .exists_by_name(name, exclude_id)
            .await?
        {
            return Err(coded_error(
                "API_KEY_NAME_CONFLICT",
                &format!("api key name already exists: {name}"),
                serde_json::json!({ "name": name }),
            ));
        }
        Ok(())
    }

    async fn get_route_by_id(&self, id: &str) -> anyhow::Result<Route> {
        let mut route = self
            .gw
            .storage
            .routes()
            .get(id)
            .await?
            .context("route not found")?;
        if let Some(store) = self.gw.storage.route_targets() {
            route.targets = store.list_targets_by_route(&route.id).await?;
        }
        route.cache = resolve_route_cache(&route);
        Ok(route)
    }

    async fn reload_route_cache(&self) -> anyhow::Result<()> {
        self.gw
            .route_cache
            .write()
            .await
            .reload(self.gw.storage.snapshots())
            .await
    }

    fn api_keys_store(&self) -> anyhow::Result<&dyn crate::storage::traits::ApiKeyStore> {
        self.gw
            .storage
            .api_keys()
            .context("selected storage backend does not support api key management")
    }

    async fn create_auth_session_record(
        &self,
        input: auth::CreateAuthSession,
    ) -> anyhow::Result<AuthSession> {
        let now = now_rfc3339();
        let session = AuthSession {
            id: uuid::Uuid::new_v4().to_string(),
            provider_id: input.provider_id,
            driver_key: input.driver_key,
            scheme: input.scheme,
            status: input.status,
            use_proxy: input.use_proxy,
            user_code: input.user_code,
            verification_uri: input.verification_uri,
            verification_uri_complete: input.verification_uri_complete,
            state_json: input.state_json,
            context_json: input.context_json,
            result_json: input.result_json,
            expires_at: input.expires_at,
            poll_interval_seconds: input.poll_interval_seconds,
            last_error: input.last_error,
            created_at: now.clone(),
            updated_at: now,
        };
        self.gw
            .auth_sessions
            .write()
            .await
            .insert(session.id.clone(), session.clone());
        Ok(session)
    }

    async fn get_auth_session_record(&self, id: &str) -> anyhow::Result<Option<AuthSession>> {
        Ok(self.gw.auth_sessions.read().await.get(id).cloned())
    }

    async fn take_ready_auth_session_record(&self, id: &str) -> anyhow::Result<AuthSession> {
        let mut sessions = self.gw.auth_sessions.write().await;
        let session = sessions
            .remove(id)
            .ok_or_else(|| anyhow::anyhow!("auth session not found: {id}"))?;
        if !session
            .status
            .eq_ignore_ascii_case(AuthSessionStatus::Ready.as_str())
        {
            sessions.insert(id.to_string(), session);
            anyhow::bail!("auth session is not ready");
        }
        Ok(session)
    }

    async fn update_auth_session_record(
        &self,
        id: &str,
        input: UpdateAuthSession,
    ) -> anyhow::Result<AuthSession> {
        let mut sessions = self.gw.auth_sessions.write().await;
        let current = sessions
            .get_mut(id)
            .ok_or_else(|| anyhow::anyhow!("auth session not found: {id}"))?;
        if let Some(value) = input.status {
            current.status = value;
        }
        if let Some(value) = input.user_code {
            current.user_code = Some(value);
        }
        if let Some(value) = input.verification_uri {
            current.verification_uri = Some(value);
        }
        if let Some(value) = input.verification_uri_complete {
            current.verification_uri_complete = Some(value);
        }
        if let Some(value) = input.state_json {
            current.state_json = Some(value);
        }
        if let Some(value) = input.context_json {
            current.context_json = Some(value);
        }
        if let Some(value) = input.result_json {
            current.result_json = Some(value);
        }
        if let Some(value) = input.expires_at {
            current.expires_at = Some(value);
        }
        if let Some(value) = input.poll_interval_seconds {
            current.poll_interval_seconds = Some(value);
        }
        if let Some(value) = input.last_error {
            current.last_error = Some(value);
        }
        current.updated_at = now_rfc3339();
        Ok(current.clone())
    }

    async fn delete_auth_session_record(&self, id: &str) -> anyhow::Result<()> {
        self.gw.auth_sessions.write().await.remove(id);
        Ok(())
    }

    async fn restore_auth_session_record(&self, mut session: AuthSession) -> anyhow::Result<()> {
        session.updated_at = now_rfc3339();
        self.gw
            .auth_sessions
            .write()
            .await
            .insert(session.id.clone(), session);
        Ok(())
    }

    pub(crate) async fn resolve_provider_runtime(
        &self,
        provider: &Provider,
    ) -> anyhow::Result<ResolvedProviderRuntime> {
        if provider.effective_auth_mode().trim() != "oauth" {
            let api_key = provider.api_key.trim().to_string();
            if api_key.is_empty() {
                anyhow::bail!("provider api key is empty");
            }
            if vertexai::is_vertex_vendor(provider) {
                let access_token = vertexai::vertex_access_token(&api_key).await?;
                let binding = RuntimeBinding {
                    base_url_override: Some(vertexai::expand_vertex_base_url(
                        &provider.base_url,
                        &api_key,
                    )),
                    ..RuntimeBinding::default()
                };
                return Ok(ResolvedProviderRuntime {
                    access_token,
                    binding,
                });
            }
            return Ok(ResolvedProviderRuntime {
                access_token: api_key,
                binding: RuntimeBinding::default(),
            });
        }

        let oauth_cred = self
            .gw
            .storage
            .oauth_credentials()
            .get(&provider.id)
            .await?;

        let oauth_cred = match oauth_cred {
            Some(c) => c,
            None => anyhow::bail!("provider oauth credential not found"),
        };

        let driver_key = if oauth_cred.driver_key.is_empty() {
            provider
                .vendor
                .as_deref()
                .map(auth::normalize_driver_key)
                .unwrap_or_default()
        } else {
            oauth_cred.driver_key.clone()
        };

        let credential = stored_credential_from_oauth(&oauth_cred, &driver_key);
        let access_token = credential
            .access_token
            .as_deref()
            .unwrap_or("")
            .trim()
            .to_string();

        if !access_token.is_empty() && !is_expired_at(credential.expires_at.as_deref()) {
            let binding = if let Some(driver) = auth::build_driver(&driver_key) {
                driver.bind_runtime(provider, &credential)?
            } else {
                RuntimeBinding::default()
            };
            return Ok(ResolvedProviderRuntime {
                access_token,
                binding,
            });
        }

        let refresh_token = credential
            .refresh_token
            .as_deref()
            .unwrap_or("")
            .trim()
            .to_string();
        if refresh_token.is_empty() {
            anyhow::bail!("provider oauth refresh token is missing");
        }

        let Some(driver) = auth::build_driver(&driver_key) else {
            anyhow::bail!("no auth driver found for key: {driver_key}");
        };

        // CAS lock: transition connected → refreshing to prevent concurrent refresh
        let oauth_store = self.gw.storage.oauth_credentials();
        let locked = oauth_store
            .try_begin_refresh(&provider.id, oauth_cred.status_version)
            .await?;
        if locked.is_none() {
            // Another caller is already refreshing — re-read and use whatever is there
            let refreshed = oauth_store.get(&provider.id).await?.ok_or_else(|| {
                anyhow::anyhow!("provider oauth credential disappeared during refresh")
            })?;
            let refreshed_token = refreshed.access_token.trim().to_string();
            if !refreshed_token.is_empty() {
                let cred = stored_credential_from_oauth(&refreshed, &driver_key);
                let binding = driver.bind_runtime(provider, &cred)?;
                return Ok(ResolvedProviderRuntime {
                    access_token: refreshed_token,
                    binding,
                });
            }
            anyhow::bail!("concurrent refresh in progress but no valid token available");
        }

        let client = self.gw.http_client_for_provider(provider.use_proxy).await?;
        let bundle = match driver
            .refresh(
                &credential,
                RefreshAuthContext {
                    use_proxy: provider.use_proxy,
                    http_client: Some(client),
                    ..Default::default()
                },
            )
            .await
        {
            Ok(bundle) => bundle,
            Err(error) => {
                let _ = oauth_store
                    .fail_refresh(&provider.id, &error.to_string())
                    .await;
                return Err(error.context("refresh oauth access token"));
            }
        };

        let refreshed_credential =
            stored_credential_from_bundle(&driver_key, driver.metadata().scheme.as_str(), &bundle);
        let credential_input =
            upsert_credential_from_bundle(&driver_key, driver.metadata().scheme.as_str(), &bundle);
        self.gw
            .storage
            .oauth_credentials()
            .complete_refresh(&provider.id, credential_input)
            .await?;
        let refreshed_provider = self
            .sync_provider_runtime_fields(provider, &refreshed_credential)
            .await?;
        let new_access_token = bundle
            .access_token
            .as_deref()
            .filter(|v| !v.trim().is_empty())
            .map(ToString::to_string)
            .ok_or_else(|| {
                anyhow::anyhow!("provider credential refresh returned empty access token")
            })?;

        Ok(ResolvedProviderRuntime {
            access_token: new_access_token,
            binding: driver.bind_runtime(&refreshed_provider, &refreshed_credential)?,
        })
    }

    async fn sync_provider_runtime_fields(
        &self,
        provider: &Provider,
        credential: &StoredCredential,
    ) -> anyhow::Result<Provider> {
        let Some(driver) = auth::build_driver(&credential.driver_key) else {
            return Ok(provider.clone());
        };

        let binding = driver.bind_runtime(provider, credential)?;
        let base_url = binding
            .base_url_override
            .clone()
            .filter(|value| !value.trim().is_empty())
            .unwrap_or_else(|| provider.base_url.clone());
        let models_source = binding
            .models_source_override
            .clone()
            .filter(|value| !value.trim().is_empty())
            .or_else(|| provider.models_source.clone());

        self.gw
            .storage
            .providers()
            .update(
                &provider.id,
                UpdateProvider {
                    base_url: Some(base_url),
                    models_source,
                    api_key: Some(String::new()),
                    auth_mode: Some("oauth".to_string()),
                    ..Default::default()
                },
            )
            .await
    }

    pub async fn refresh_oauth_providers(&self) -> anyhow::Result<usize> {
        let oauth_store = self.gw.storage.oauth_credentials();

        // Recover stale refreshing credentials (timeout = 60s)
        let recovered = oauth_store
            .recover_stale_refreshing(std::time::Duration::from_secs(60))
            .await
            .unwrap_or(0);
        if recovered > 0 {
            tracing::info!("recovered {recovered} stale refreshing oauth credentials");
        }

        // Find credentials expiring within 300 seconds
        let expiring = oauth_store
            .list_expiring(std::time::Duration::from_secs(300))
            .await?;

        let mut refreshed = 0usize;
        for cred in expiring {
            let has_refresh = cred
                .refresh_token
                .as_deref()
                .map(str::trim)
                .is_some_and(|value| !value.is_empty());
            if !has_refresh {
                continue;
            }

            let provider = match self.gw.storage.providers().get(&cred.provider_id).await? {
                Some(p) => p,
                None => continue,
            };

            match self.proactive_refresh_credential(&provider, &cred).await {
                Ok(_) => refreshed += 1,
                Err(error) => tracing::warn!(
                    "background oauth refresh failed for provider {} ({}): {}",
                    provider.id,
                    provider.name,
                    error
                ),
            }
        }

        Ok(refreshed)
    }

    /// Proactively refresh an OAuth credential that is approaching expiry.
    /// Unlike `resolve_provider_runtime` (which skips refresh if the token is
    /// still valid), this always attempts to obtain a new token.
    async fn proactive_refresh_credential(
        &self,
        provider: &Provider,
        cred: &OAuthCredential,
    ) -> anyhow::Result<()> {
        let driver_key = if cred.driver_key.is_empty() {
            provider
                .vendor
                .as_deref()
                .map(auth::normalize_driver_key)
                .unwrap_or_default()
        } else {
            cred.driver_key.clone()
        };

        let credential = stored_credential_from_oauth(cred, &driver_key);

        let Some(driver) = auth::build_driver(&driver_key) else {
            anyhow::bail!("no auth driver found for key: {driver_key}");
        };

        // CAS lock: transition connected → refreshing
        let oauth_store = self.gw.storage.oauth_credentials();
        let locked = oauth_store
            .try_begin_refresh(&provider.id, cred.status_version)
            .await?;
        if locked.is_none() {
            // Another caller is already refreshing — skip
            return Ok(());
        }

        let client = self.gw.http_client_for_provider(provider.use_proxy).await?;
        let bundle = match driver
            .refresh(
                &credential,
                RefreshAuthContext {
                    use_proxy: provider.use_proxy,
                    http_client: Some(client),
                    ..Default::default()
                },
            )
            .await
        {
            Ok(bundle) => bundle,
            Err(error) => {
                let _ = oauth_store
                    .fail_refresh(&provider.id, &error.to_string())
                    .await;
                return Err(error.context("proactive oauth refresh"));
            }
        };

        let refreshed_credential =
            stored_credential_from_bundle(&driver_key, driver.metadata().scheme.as_str(), &bundle);
        let credential_input =
            upsert_credential_from_bundle(&driver_key, driver.metadata().scheme.as_str(), &bundle);
        oauth_store
            .complete_refresh(&provider.id, credential_input)
            .await?;
        self.sync_provider_runtime_fields(provider, &refreshed_credential)
            .await?;
        Ok(())
    }

    pub(crate) async fn cleanup_auth_sessions(&self) -> anyhow::Result<usize> {
        let mut sessions = self.gw.auth_sessions.write().await;
        let before = sessions.len();
        sessions.retain(|_, session| {
            let terminal = matches!(session.status.as_str(), "error" | "cancelled");
            !terminal && !is_expired_at(session.expires_at.as_deref())
        });
        Ok(before.saturating_sub(sessions.len()))
    }
}

fn parse_auth_session_bundle(session: &AuthSession) -> anyhow::Result<CredentialBundle> {
    let raw = session
        .result_json
        .as_deref()
        .context("auth session is missing result_json")?;
    serde_json::from_str(raw).context("parse auth session credential bundle")
}

fn stored_credential_from_oauth(oauth: &OAuthCredential, driver_key: &str) -> StoredCredential {
    let scopes: Vec<String> = serde_json::from_str(&oauth.scopes).unwrap_or_default();
    let meta: Value = serde_json::from_str(&oauth.meta).unwrap_or(Value::Null);
    StoredCredential {
        driver_key: driver_key.to_string(),
        scheme: if oauth.scheme.is_empty() {
            AuthScheme::OAuthAuthCodePkce.as_str().to_string()
        } else {
            oauth.scheme.clone()
        },
        access_token: normalized_optional(Some(&oauth.access_token)),
        refresh_token: normalized_optional(oauth.refresh_token.as_deref()),
        expires_at: normalized_optional(oauth.expires_at.as_deref()),
        resource_url: normalized_optional(oauth.resource_url.as_deref()),
        subject_id: normalized_optional(oauth.subject_id.as_deref()),
        scopes,
        meta,
    }
}

fn upsert_credential_from_bundle(
    driver_key: &str,
    scheme: &str,
    bundle: &CredentialBundle,
) -> UpsertOAuthCredential {
    let scopes_json = serde_json::to_string(&bundle.scopes).unwrap_or_else(|_| "[]".to_string());
    let meta_json = serde_json::to_string(&bundle.raw).unwrap_or_else(|_| "{}".to_string());
    UpsertOAuthCredential {
        driver_key: driver_key.to_string(),
        scheme: scheme.to_string(),
        access_token: bundle.access_token.clone().unwrap_or_default(),
        refresh_token: bundle.refresh_token.clone(),
        expires_at: bundle.expires_at.clone(),
        resource_url: bundle.resource_url.clone(),
        subject_id: bundle.subject_id.clone(),
        scopes: Some(scopes_json),
        meta: Some(meta_json),
    }
}

fn upsert_credential_from_oauth(oauth: &OAuthCredential) -> UpsertOAuthCredential {
    UpsertOAuthCredential {
        driver_key: oauth.driver_key.clone(),
        scheme: oauth.scheme.clone(),
        access_token: oauth.access_token.clone(),
        refresh_token: oauth.refresh_token.clone(),
        expires_at: oauth.expires_at.clone(),
        resource_url: oauth.resource_url.clone(),
        subject_id: oauth.subject_id.clone(),
        scopes: Some(oauth.scopes.clone()),
        meta: Some(oauth.meta.clone()),
    }
}

fn stored_credential_from_bundle(
    driver_key: &str,
    scheme: &str,
    bundle: &CredentialBundle,
) -> StoredCredential {
    StoredCredential {
        driver_key: driver_key.to_string(),
        scheme: scheme.to_string(),
        access_token: normalized_optional(bundle.access_token.as_deref()),
        refresh_token: normalized_optional(bundle.refresh_token.as_deref()),
        expires_at: normalized_optional(bundle.expires_at.as_deref()),
        resource_url: normalized_optional(bundle.resource_url.as_deref()),
        subject_id: normalized_optional(bundle.subject_id.as_deref()),
        scopes: bundle.scopes.clone(),
        meta: bundle.raw.clone(),
    }
}

fn build_provider_oauth_status(
    provider: &Provider,
    driver_key: &str,
    status_override: Option<String>,
    fallback_error: Option<String>,
) -> ProviderOAuthStatusData {
    // This version is used when we don't have an OAuthCredential loaded.
    let status =
        status_override.unwrap_or_else(|| AuthBindingStatus::Disconnected.as_str().to_string());
    ProviderOAuthStatusData {
        provider_id: provider.id.clone(),
        provider_name: provider.name.clone(),
        driver_key: driver_key.to_string(),
        status,
        expires_at: None,
        resource_url: normalized_optional(Some(provider.base_url.as_str())),
        subject_id: None,
        last_error: fallback_error.filter(|value| !value.trim().is_empty()),
        updated_at: Some(provider.updated_at.clone()),
        has_refresh_token: false,
    }
}

fn build_provider_oauth_status_from_credential(
    provider: &Provider,
    driver_key: &str,
    oauth: &OAuthCredential,
) -> ProviderOAuthStatusData {
    let status = match oauth.status.as_str() {
        "connected" => AuthBindingStatus::Connected.as_str().to_string(),
        "refreshing" => AuthBindingStatus::Pending.as_str().to_string(),
        "error" => AuthBindingStatus::Error.as_str().to_string(),
        _ => AuthBindingStatus::Disconnected.as_str().to_string(),
    };
    let has_refresh_token = oauth
        .refresh_token
        .as_deref()
        .map(str::trim)
        .is_some_and(|value| !value.is_empty());
    ProviderOAuthStatusData {
        provider_id: provider.id.clone(),
        provider_name: provider.name.clone(),
        driver_key: driver_key.to_string(),
        status,
        expires_at: normalized_optional(oauth.expires_at.as_deref()),
        resource_url: normalized_optional(oauth.resource_url.as_deref())
            .or_else(|| normalized_optional(Some(provider.base_url.as_str()))),
        subject_id: normalized_optional(oauth.subject_id.as_deref()),
        last_error: oauth.last_error.clone(),
        updated_at: Some(oauth.updated_at.clone()),
        has_refresh_token,
    }
}

fn build_auth_session_init_data(session: &AuthSession) -> anyhow::Result<AuthSessionInitData> {
    Ok(AuthSessionInitData {
        session_id: session.id.clone(),
        vendor: session.driver_key.clone(),
        scheme: session.scheme.clone(),
        auth_url: session
            .verification_uri_complete
            .clone()
            .unwrap_or_default(),
        requires_manual_code: session.scheme == AuthScheme::OAuthAuthCodePkce.as_str()
            || session.scheme == AuthScheme::SetupToken.as_str(),
        user_code: session.user_code.clone().unwrap_or_default(),
        verification_uri: session.verification_uri.clone().unwrap_or_default(),
        verification_uri_complete: session
            .verification_uri_complete
            .clone()
            .unwrap_or_default(),
        expires_in: remaining_seconds_until(session.expires_at.as_deref()),
        interval: session.poll_interval_seconds.unwrap_or(2),
    })
}

fn build_auth_session_pending_data(session: &AuthSession) -> AuthSessionStatusData {
    AuthSessionStatusData::Pending {
        scheme: session.scheme.clone(),
        auth_url: session
            .verification_uri_complete
            .clone()
            .unwrap_or_default(),
        requires_manual_code: session.scheme == AuthScheme::OAuthAuthCodePkce.as_str()
            || session.scheme == AuthScheme::SetupToken.as_str(),
        expires_in: remaining_seconds_until(session.expires_at.as_deref()),
        interval: session.poll_interval_seconds.unwrap_or(2),
        user_code: session.user_code.clone().unwrap_or_default(),
        verification_uri_complete: session
            .verification_uri_complete
            .clone()
            .unwrap_or_default(),
    }
}

fn build_auth_session_ready_data(
    session: &AuthSession,
    bundle: &CredentialBundle,
) -> AuthSessionStatusData {
    AuthSessionStatusData::Ready {
        expires_in: remaining_seconds_until(
            bundle
                .expires_at
                .as_deref()
                .or(session.expires_at.as_deref()),
        ),
        resource_url: bundle.resource_url.clone(),
    }
}

fn remaining_seconds_until(expires_at: Option<&str>) -> i64 {
    expires_at
        .map(str::trim)
        .filter(|value| !value.is_empty())
        .and_then(parse_datetime_utc)
        .map(|value| (value - Utc::now()).num_seconds().max(0))
        .unwrap_or(0)
}

fn is_expired_at(expires_at: Option<&str>) -> bool {
    expires_at
        .map(str::trim)
        .filter(|value| !value.is_empty())
        .and_then(parse_datetime_utc)
        .map(|value| value <= Utc::now())
        .unwrap_or(false)
}

fn parse_datetime_utc(value: &str) -> Option<DateTime<Utc>> {
    chrono::DateTime::parse_from_rfc3339(value)
        .map(|value| value.with_timezone(&Utc))
        .ok()
        .or_else(|| {
            chrono::NaiveDateTime::parse_from_str(value, "%Y-%m-%d %H:%M:%S")
                .ok()
                .map(|value| DateTime::<Utc>::from_naive_utc_and_offset(value, Utc))
        })
}

fn now_rfc3339() -> String {
    Utc::now().to_rfc3339()
}

fn normalized_optional(value: Option<&str>) -> Option<String> {
    value
        .map(str::trim)
        .filter(|value| !value.is_empty())
        .map(ToString::to_string)
}

fn flatten_route_cache_columns(
    cache: Option<&RouteCacheConfig>,
) -> (Option<i64>, Option<i64>, Option<f64>) {
    let Some(cache) = cache else {
        return (None, None, None);
    };
    let exact_ttl = cache.exact.as_ref().map(|exact| exact.ttl.unwrap_or(0));
    let semantic_ttl = cache
        .semantic
        .as_ref()
        .map(|semantic| semantic.ttl.unwrap_or(0));
    let semantic_threshold = cache
        .semantic
        .as_ref()
        .and_then(|semantic| semantic.threshold);
    (exact_ttl, semantic_ttl, semantic_threshold)
}

fn resolve_route_cache(route: &Route) -> Option<RouteCacheConfig> {
    let exact = route.cache_exact_ttl.map(|ttl| RouteExactCacheConfig {
        ttl: if ttl > 0 { Some(ttl) } else { None },
    });
    let semantic = route
        .cache_semantic_ttl
        .map(|ttl| RouteSemanticCacheConfig {
            ttl: if ttl > 0 { Some(ttl) } else { None },
            threshold: route.cache_semantic_threshold,
        });
    if exact.is_none() && semantic.is_none() {
        None
    } else {
        Some(RouteCacheConfig { exact, semantic })
    }
}

fn format_connectivity_error(error: &reqwest::Error) -> String {
    if error.is_timeout() {
        return "Connection timeout (10s), please check Base URL or network settings".to_string();
    }
    if error.is_connect() {
        return "Unable to connect to the host, please check DNS/network settings".to_string();
    }
    error.to_string()
}

fn coded_error(code: &str, message: &str, params: Value) -> anyhow::Error {
    anyhow::anyhow!(
        "{}",
        serde_json::json!({
            "code": code,
            "message": message,
            "params": params,
        })
    )
}

fn ensure_virtual_model(model: &str) -> anyhow::Result<()> {
    if model.trim().is_empty() {
        anyhow::bail!("virtual_model cannot be empty");
    }
    Ok(())
}

fn normalize_route_strategy(strategy: Option<&str>) -> anyhow::Result<String> {
    let normalized = strategy.unwrap_or("weighted").trim().to_ascii_lowercase();
    match normalized.as_str() {
        "weighted" | "priority" => Ok(normalized),
        _ => anyhow::bail!("unsupported route strategy: {normalized}"),
    }
}

fn normalize_create_route_targets(input: &CreateRoute) -> anyhow::Result<Vec<CreateRouteTarget>> {
    if !input.targets.is_empty() {
        return Ok(input.targets.clone());
    }
    if !input.target_provider.trim().is_empty() && !input.target_model.trim().is_empty() {
        return Ok(vec![CreateRouteTarget {
            provider_id: input.target_provider.clone(),
            model: input.target_model.clone(),
            weight: Some(100),
            priority: Some(1),
        }]);
    }
    anyhow::bail!("at least one route target is required")
}

fn normalize_update_route_targets(
    current: &Route,
    input: &UpdateRoute,
) -> anyhow::Result<Vec<CreateRouteTarget>> {
    if let Some(targets) = &input.targets {
        let mapped = targets
            .iter()
            .map(|target| CreateRouteTarget {
                provider_id: target.provider_id.clone(),
                model: target.model.clone(),
                weight: target.weight,
                priority: target.priority,
            })
            .collect();
        return Ok(mapped);
    }

    let provider = input
        .target_provider
        .clone()
        .unwrap_or_else(|| current.target_provider.clone());
    let model = input
        .target_model
        .clone()
        .unwrap_or_else(|| current.target_model.clone());
    if provider.trim().is_empty() || model.trim().is_empty() {
        anyhow::bail!("route target cannot be empty");
    }
    Ok(vec![CreateRouteTarget {
        provider_id: provider,
        model,
        weight: Some(100),
        priority: Some(1),
    }])
}

fn ensure_route_targets_valid(targets: &[CreateRouteTarget]) -> anyhow::Result<()> {
    if targets.is_empty() {
        anyhow::bail!("at least one route target is required");
    }
    for target in targets {
        if target.provider_id.trim().is_empty() {
            anyhow::bail!("target provider_id cannot be empty");
        }
        if target.model.trim().is_empty() {
            anyhow::bail!("target model cannot be empty");
        }
        let weight = target.weight.unwrap_or(100);
        if weight < 0 {
            anyhow::bail!("target weight must be >= 0");
        }
        let priority = target.priority.unwrap_or(1);
        if !(1..=2).contains(&priority) {
            anyhow::bail!("target priority must be 1 or 2");
        }
    }
    Ok(())
}

fn normalize_name(name: &str, field: &str) -> anyhow::Result<String> {
    let trimmed = name.trim();
    if trimmed.is_empty() {
        anyhow::bail!("{field} cannot be empty");
    }
    Ok(trimmed.to_string())
}

fn normalize_vendor(vendor: Option<&str>) -> Option<String> {
    vendor
        .map(str::trim)
        .filter(|v| !v.is_empty() && *v != "custom")
        .map(|v| v.to_lowercase())
}

fn import_provider_protocol(provider: &ExportProvider) -> String {
    provider
        .default_protocol
        .trim()
        .is_empty()
        .then(|| provider.protocol.clone())
        .unwrap_or_else(|| provider.default_protocol.clone())
}

fn import_provider_base_url(provider: &ExportProvider) -> String {
    if !provider.base_url.trim().is_empty() {
        return provider.base_url.clone();
    }
    base_url_from_protocol_endpoints(
        &provider.protocol_endpoints,
        &import_provider_protocol(provider),
    )
    .unwrap_or_default()
}

fn base_url_from_protocol_endpoints(raw: &str, protocol: &str) -> Option<String> {
    let reg = crate::protocol::registry::ProtocolRegistry::global();
    let target = reg.parse_protocol(protocol)?;
    let value = serde_json::from_str::<serde_json::Value>(raw.trim()).ok()?;
    let obj = value.as_object()?;
    obj.iter().find_map(|(key, entry)| {
        let entry_protocol = reg.parse_protocol(key)?;
        if entry_protocol != target {
            return None;
        }
        entry
            .as_object()
            .and_then(|object| object.get("base_url"))
            .and_then(|value| value.as_str())
            .map(str::trim)
            .filter(|value| !value.is_empty())
            .map(ToString::to_string)
    })
}

fn resolve_models_endpoint(provider: &Provider) -> Option<String> {
    if let Some(endpoint) = provider.effective_models_source() {
        let trimmed = endpoint.trim();
        if !trimmed.is_empty() {
            return Some(trimmed.to_string());
        }
    }

    let base = provider.base_url.trim_end_matches('/');
    match provider.protocol.as_str() {
        "openai" | "openai-compat" | "openai-resps" | "anthropic" | "anthropic-msgs" => {
            let has_base_path = reqwest::Url::parse(base)
                .ok()
                .map(|url| {
                    let pathname = url.path().trim_end_matches('/');
                    !pathname.is_empty() && pathname != "/"
                })
                .unwrap_or(false);
            if has_base_path {
                Some(format!("{base}/models"))
            } else {
                Some(format!("{base}/v1/models"))
            }
        }
        "gemini" | "google-genai" => Some(format!("{base}/v1beta/models")),
        _ => None,
    }
}

fn resolve_openai_base_url(provider: &Provider) -> Option<String> {
    let protocols = ProviderProtocols::from_provider(provider);
    if !protocols.supports(OPENAI_CHAT_COMPLETIONS_V1) {
        return None;
    }
    let resolved = protocols.resolve_egress(OPENAI_CHAT_COMPLETIONS_V1);
    let trimmed = resolved.base_url.trim();
    if trimmed.is_empty() {
        return None;
    }
    Some(trimmed.to_string())
}

fn runtime_binding_headers(binding: &RuntimeBinding) -> anyhow::Result<HeaderMap> {
    let mut headers = HeaderMap::new();
    for (key, value) in &binding.extra_headers {
        headers.insert(
            reqwest::header::HeaderName::from_bytes(key.as_bytes())?,
            HeaderValue::from_str(value)?,
        );
    }
    Ok(headers)
}

fn build_model_headers(
    protocol: &str,
    vendor: Option<&str>,
    api_key: &str,
) -> anyhow::Result<HeaderMap> {
    let mut headers = HeaderMap::new();
    let is_google_vendor = vendor
        .map(str::trim)
        .is_some_and(|value| value.eq_ignore_ascii_case("google"));
    match protocol {
        "anthropic" => {
            headers.insert("x-api-key", HeaderValue::from_str(api_key)?);
            headers.insert("anthropic-version", HeaderValue::from_static("2023-06-01"));
        }
        "gemini" => {
            // Google providers may expose OpenAI-compatible /v1/models endpoints.
            // Add Bearer auth in addition to Gemini key query param.
            if is_google_vendor {
                headers.insert(
                    AUTHORIZATION,
                    HeaderValue::from_str(&format!("Bearer {api_key}"))?,
                );
            }
        }
        _ => {
            headers.insert(
                AUTHORIZATION,
                HeaderValue::from_str(&format!("Bearer {api_key}"))?,
            );
        }
    }
    Ok(headers)
}

fn extract_models_from_response(
    _protocol: &str,
    vendor: Option<&str>,
    json: &Value,
) -> Vec<String> {
    let is_google_vendor = vendor
        .map(str::trim)
        .is_some_and(|value| value.eq_ignore_ascii_case("google"));
    let mut models = json
        .get("data")
        .and_then(|value| value.as_array())
        .into_iter()
        .flatten()
        .filter_map(|item| item.get("id").and_then(|value| value.as_str()))
        .map(|id| {
            if is_google_vendor {
                id.strip_prefix("models/").unwrap_or(id).to_string()
            } else {
                id.to_string()
            }
        })
        .collect::<Vec<_>>();

    if models.is_empty() {
        models = json
            .get("models")
            .and_then(|value| value.as_array())
            .into_iter()
            .flatten()
            .filter_map(|item| {
                item.get("name")
                    .and_then(|value| value.as_str())
                    .or_else(|| item.get("slug").and_then(|value| value.as_str()))
                    .or_else(|| item.get("id").and_then(|value| value.as_str()))
            })
            .map(|name| {
                let normalized = name.rsplit('/').next().unwrap_or(name);
                if is_google_vendor {
                    normalized
                        .strip_prefix("models/")
                        .unwrap_or(normalized)
                        .to_string()
                } else {
                    normalized.to_string()
                }
            })
            .collect::<Vec<_>>();
    }

    models.sort();
    models.dedup();
    models
}

fn parse_static_models(raw: Option<&str>) -> Vec<String> {
    let mut models = raw
        .unwrap_or("")
        .lines()
        .flat_map(|line| line.split(','))
        .map(str::trim)
        .filter(|line| !line.is_empty())
        .map(ToString::to_string)
        .collect::<Vec<_>>();
    models.sort();
    models.dedup();
    models
}

/// Resolve the `CapabilitiesSource` strategy for a provider from its preset channel.
/// Falls back to `Auto` when no matching preset/channel is found.
fn preset_capabilities_source(provider: &Provider) -> CapabilitiesSource {
    let Some(ref preset_key) = provider.preset_key else {
        return CapabilitiesSource::Auto;
    };
    let Some(meta) = VendorRegistry::global().metadata(preset_key) else {
        return CapabilitiesSource::Auto;
    };
    let channel_id = provider.channel.as_deref().unwrap_or("default");
    let Some(ch) = meta.channels.iter().find(|c| c.id == channel_id) else {
        return CapabilitiesSource::Auto;
    };
    ch.capabilities_source
}

fn is_ollama_show_endpoint(url: &str) -> bool {
    url.trim_end_matches('/').ends_with("/api/show")
}

fn parse_ollama_capability(json: &Value, model: &str) -> ModelCapabilities {
    let model_info = json.get("model_info").and_then(Value::as_object);
    let caps = json
        .get("capabilities")
        .and_then(Value::as_array)
        .map(|arr| {
            arr.iter()
                .filter_map(Value::as_str)
                .map(ToString::to_string)
                .collect::<Vec<_>>()
        })
        .unwrap_or_default();
    let has_vision = caps.iter().any(|c| c.eq_ignore_ascii_case("vision"));
    let context_window = model_info
        .and_then(extract_ollama_context_window)
        .unwrap_or(8 * 1024);
    let embedding_length = model_info.and_then(extract_ollama_embedding_length);

    ModelCapabilities {
        provider: "ollama".to_string(),
        model_id: model.to_string(),
        context_window,
        embedding_length,
        output_max_tokens: None,
        tool_call: caps.iter().any(|c| c == "tools"),
        reasoning: caps.iter().any(|c| c == "thinking"),
        input_modalities: if has_vision {
            vec!["text".to_string(), "image".to_string()]
        } else {
            vec!["text".to_string()]
        },
        output_modalities: vec!["text".to_string()],
        input_cost: Some(0.0),
        output_cost: Some(0.0),
    }
}

fn extract_ollama_context_window(model_info: &serde_json::Map<String, Value>) -> Option<u64> {
    let arch = model_info.get("general.architecture")?.as_str()?;
    let key = format!("{arch}.context_length");
    model_info
        .get(&key)
        .and_then(Value::as_u64)
        .filter(|value| *value > 0)
}

fn extract_ollama_embedding_length(model_info: &serde_json::Map<String, Value>) -> Option<u64> {
    if let Some(arch) = model_info
        .get("general.architecture")
        .and_then(Value::as_str)
    {
        let key = format!("{arch}.embedding_length");
        if let Some(value) = model_info
            .get(&key)
            .and_then(Value::as_u64)
            .filter(|value| *value > 0)
        {
            return Some(value);
        }
    }
    model_info
        .get("embedding_length")
        .and_then(Value::as_u64)
        .or_else(|| {
            model_info
                .get("general.embedding_length")
                .and_then(Value::as_u64)
        })
        .filter(|value| *value > 0)
}

pub async fn refresh_models_dev_runtime_cache_if_stale(
    data_dir: PathBuf,
    http_client: reqwest::Client,
) {
    if let Err(err) = refresh_models_dev_runtime_cache_inner(&data_dir, &http_client, false).await {
        tracing::warn!("models.dev runtime refresh skipped: {err}");
    }
}

pub async fn refresh_models_dev_runtime_cache_on_startup(
    data_dir: PathBuf,
    http_client: reqwest::Client,
) {
    if let Err(err) = refresh_models_dev_runtime_cache_inner(&data_dir, &http_client, true).await {
        tracing::warn!(
            "models.dev startup refresh failed, fallback to local cache/snapshot: {err}"
        );
    }
}

fn models_dev_runtime_cache_path(data_dir: &Path) -> PathBuf {
    data_dir.join(MODELS_DEV_RUNTIME_FILE)
}

async fn refresh_models_dev_runtime_cache_inner(
    data_dir: &Path,
    http_client: &reqwest::Client,
    force_refresh: bool,
) -> anyhow::Result<()> {
    let cache_path = models_dev_runtime_cache_path(data_dir);
    if !force_refresh
        && let Ok(meta) = std::fs::metadata(&cache_path)
        && let Ok(modified_at) = meta.modified()
        && let Ok(elapsed) = modified_at.elapsed()
        && elapsed < MODELS_DEV_RUNTIME_TTL
    {
        return Ok(());
    }

    let resp = http_client
        .get(MODELS_DEV_SOURCE_URL)
        .timeout(Duration::from_secs(20))
        .send()
        .await
        .map_err(|e| anyhow::anyhow!("request models.dev failed: {e}"))?;
    if !resp.status().is_success() {
        anyhow::bail!("models.dev returned status {}", resp.status());
    }
    let body = resp
        .text()
        .await
        .map_err(|e| anyhow::anyhow!("read models.dev body failed: {e}"))?;

    // Validate payload shape before replacing local cache.
    let _: HashMap<String, ModelsDevVendor> = serde_json::from_str(&body)
        .map_err(|e| anyhow::anyhow!("invalid models.dev payload: {e}"))?;

    std::fs::create_dir_all(data_dir)?;
    let tmp_path = data_dir.join(format!("{MODELS_DEV_RUNTIME_FILE}.tmp"));
    std::fs::write(&tmp_path, body.as_bytes())?;
    std::fs::rename(&tmp_path, &cache_path)?;
    Ok(())
}

fn parse_provider_presets_snapshot() -> anyhow::Result<Vec<Value>> {
    Ok(VendorRegistry::global().list_metadata_legacy_json())
}

fn resolve_preset_channel_auth_mode(
    preset_key: Option<&str>,
    channel_id: Option<&str>,
) -> Option<String> {
    crate::db::models::resolve_preset_channel_auth_mode(preset_key, channel_id)
}

fn parse_models_dev_data(data_dir: &Path) -> anyhow::Result<HashMap<String, ModelsDevVendor>> {
    let cache_path = models_dev_runtime_cache_path(data_dir);
    if let Ok(content) = std::fs::read_to_string(&cache_path) {
        if let Ok(parsed) = serde_json::from_str::<HashMap<String, ModelsDevVendor>>(&content) {
            return Ok(parsed);
        }
        tracing::warn!(
            "invalid models.dev runtime cache at {}, fallback to embedded snapshot",
            cache_path.display()
        );
    }
    parse_models_dev_snapshot()
}

fn lookup_models_dev_models(data_dir: &Path, source: &str) -> anyhow::Result<Option<Vec<String>>> {
    let trimmed = source.trim();
    if trimmed.is_empty() {
        return Ok(None);
    }
    let vendor_key = if trimmed.eq_ignore_ascii_case("ai://models.dev") {
        ""
    } else if let Some(key) = trimmed.strip_prefix("ai://models.dev/") {
        key
    } else {
        return Ok(None);
    };
    let data = parse_models_dev_data(data_dir)?;
    if vendor_key.trim().is_empty() {
        let mut models = data
            .values()
            .flat_map(|vendor| vendor.models.keys().cloned())
            .collect::<Vec<_>>();
        models.sort();
        models.dedup();
        return Ok(Some(models));
    }
    let Some(vendor) = data.get(vendor_key) else {
        return Ok(Some(Vec::new()));
    };
    let mut models = vendor.models.keys().cloned().collect::<Vec<_>>();
    models.sort();
    Ok(Some(models))
}

fn lookup_models_dev_capability(
    data_dir: &Path,
    vendor_key: &str,
    model: &str,
) -> Option<ModelCapabilities> {
    let data = parse_models_dev_data(data_dir).ok()?;
    match_models_dev_capability(&data, vendor_key, model)
}

fn fuzzy_match_models_dev(data_dir: &Path, model: &str) -> Option<ModelCapabilities> {
    let data = parse_models_dev_data(data_dir).ok()?;
    match_models_dev_capability(&data, "", model)
}

fn match_models_dev_capability(
    data: &HashMap<String, ModelsDevVendor>,
    vendor_key: &str,
    model: &str,
) -> Option<ModelCapabilities> {
    let needle = model.trim().to_lowercase();
    if needle.is_empty() {
        return None;
    }

    if vendor_key.trim().is_empty() {
        for (vk, vendor) in data {
            for (model_id, entry) in &vendor.models {
                if model_id.eq_ignore_ascii_case(model) {
                    return Some(to_models_dev_capability(vk, entry));
                }
            }
        }
        let mut best: Option<(usize, ModelCapabilities)> = None;
        for (vk, vendor) in data {
            for (model_id, entry) in &vendor.models {
                if model_id.to_lowercase().contains(&needle) {
                    let cap = to_models_dev_capability(vk, entry);
                    let len = model_id.len();
                    let replace = best
                        .as_ref()
                        .map(|(prev_len, _)| len < *prev_len)
                        .unwrap_or(true);
                    if replace {
                        best = Some((len, cap));
                    }
                }
            }
        }
        return best.map(|(_, cap)| cap);
    }

    let vendor = data.get(vendor_key)?;
    for (model_id, entry) in &vendor.models {
        if model_id.eq_ignore_ascii_case(model) {
            return Some(to_models_dev_capability(vendor_key, entry));
        }
    }
    let mut best: Option<(usize, ModelCapabilities)> = None;
    for (model_id, entry) in &vendor.models {
        if model_id.to_lowercase().contains(&needle) {
            let cap = to_models_dev_capability(vendor_key, entry);
            let len = model_id.len();
            let replace = best
                .as_ref()
                .map(|(prev_len, _)| len < *prev_len)
                .unwrap_or(true);
            if replace {
                best = Some((len, cap));
            }
        }
    }
    best.map(|(_, cap)| cap)
}

fn parse_http_capability(json: &Value, model: &str) -> Option<ModelCapabilities> {
    let arr = json.get("data").and_then(Value::as_array)?;
    let item = arr.iter().find(|entry| {
        entry
            .get("id")
            .and_then(Value::as_str)
            .is_some_and(|id| id.eq_ignore_ascii_case(model))
    })?;

    let model_id = item.get("id").and_then(Value::as_str).unwrap_or(model);
    let context_window = item
        .get("context_length")
        .and_then(Value::as_u64)
        .filter(|v| *v > 0)
        .unwrap_or(128 * 1024);
    let output_max_tokens = item
        .get("top_provider")
        .and_then(Value::as_object)
        .and_then(|obj| obj.get("max_completion_tokens"))
        .and_then(Value::as_u64)
        .filter(|v| *v > 0);
    let supported_parameters = item
        .get("supported_parameters")
        .and_then(Value::as_array)
        .cloned()
        .unwrap_or_default();
    let input_modalities = item
        .get("architecture")
        .and_then(Value::as_object)
        .and_then(|obj| obj.get("input_modalities"))
        .and_then(Value::as_array)
        .map(|arr| {
            arr.iter()
                .filter_map(Value::as_str)
                .map(ToString::to_string)
                .collect::<Vec<_>>()
        })
        .unwrap_or_else(|| vec!["text".to_string()]);
    let output_modalities = item
        .get("architecture")
        .and_then(Value::as_object)
        .and_then(|obj| obj.get("output_modalities"))
        .and_then(Value::as_array)
        .map(|arr| {
            arr.iter()
                .filter_map(Value::as_str)
                .map(ToString::to_string)
                .collect::<Vec<_>>()
        })
        .unwrap_or_else(|| vec!["text".to_string()]);
    let input_cost = item
        .get("pricing")
        .and_then(Value::as_object)
        .and_then(|obj| obj.get("prompt"))
        .and_then(parse_maybe_price_per_token);
    let output_cost = item
        .get("pricing")
        .and_then(Value::as_object)
        .and_then(|obj| obj.get("completion"))
        .and_then(parse_maybe_price_per_token);
    let tool_call = supported_parameters
        .iter()
        .any(|v| v.as_str() == Some("tools"));
    let model_lower = model_id.to_lowercase();
    let reasoning = model_lower.contains("reason")
        || model_lower.contains("thinking")
        || model_lower.contains("o1")
        || model_lower.contains("o3")
        || model_lower.contains("o4");

    Some(ModelCapabilities {
        provider: "openrouter".to_string(),
        model_id: model_id.to_string(),
        context_window,
        embedding_length: None,
        output_max_tokens,
        tool_call,
        reasoning,
        input_modalities,
        output_modalities,
        input_cost,
        output_cost,
    })
}

fn parse_maybe_price_per_token(value: &Value) -> Option<f64> {
    let parsed = if let Some(v) = value.as_f64() {
        Some(v)
    } else if let Some(s) = value.as_str() {
        s.parse::<f64>().ok()
    } else {
        None
    }?;
    if parsed <= 0.0 {
        return None;
    }
    Some(parsed * 1_000_000.0)
}

async fn load_route_targets_for_probe(gw: &Gateway, route: &Route) -> Vec<RouteTarget> {
    if let Some(store) = gw.storage.route_targets()
        && let Ok(targets) = store.list_targets_by_route(&route.id).await
        && !targets.is_empty()
    {
        return targets;
    }
    if route.target_provider.trim().is_empty() {
        return vec![];
    }
    vec![RouteTarget {
        id: String::new(),
        route_id: route.id.clone(),
        provider_id: route.target_provider.clone(),
        model: route.target_model.clone(),
        weight: 100,
        priority: 1,
        created_at: String::new(),
    }]
}

fn parse_embedding_dimensions_from_payload(payload: &Value) -> Option<u64> {
    payload
        .get("data")
        .and_then(Value::as_array)?
        .first()?
        .get("embedding")
        .and_then(Value::as_array)
        .map(|embedding| embedding.len() as u64)
        .filter(|value| *value > 0)
}

#[derive(Debug, Clone, serde::Deserialize)]
struct ModelsDevVendor {
    #[serde(default)]
    models: HashMap<String, ModelsDevModelEntry>,
}

#[derive(Debug, Clone, serde::Deserialize)]
struct ModelsDevModelEntry {
    id: String,
    #[serde(default)]
    reasoning: bool,
    #[serde(default)]
    tool_call: bool,
    #[serde(default)]
    modalities: ModelsDevModalities,
    #[serde(default)]
    cost: ModelsDevCost,
    #[serde(default)]
    limit: ModelsDevLimit,
}

#[derive(Debug, Clone, serde::Deserialize, Default)]
struct ModelsDevModalities {
    #[serde(default)]
    input: Vec<String>,
    #[serde(default)]
    output: Vec<String>,
}

#[derive(Debug, Clone, serde::Deserialize, Default)]
struct ModelsDevCost {
    input: Option<f64>,
    output: Option<f64>,
}

#[derive(Debug, Clone, serde::Deserialize, Default)]
struct ModelsDevLimit {
    context: Option<u64>,
    output: Option<u64>,
}

fn parse_models_dev_snapshot() -> anyhow::Result<HashMap<String, ModelsDevVendor>> {
    let parsed = serde_json::from_str::<HashMap<String, ModelsDevVendor>>(MODELS_DEV_SNAPSHOT)
        .map_err(|e| anyhow::anyhow!("failed to parse models.dev snapshot: {e}"))?;
    Ok(parsed)
}

fn to_models_dev_capability(vendor_key: &str, model: &ModelsDevModelEntry) -> ModelCapabilities {
    let input_modalities = if model.modalities.input.is_empty() {
        vec!["text".to_string()]
    } else {
        model.modalities.input.clone()
    };
    let output_modalities = if model.modalities.output.is_empty() {
        vec!["text".to_string()]
    } else {
        model.modalities.output.clone()
    };

    ModelCapabilities {
        provider: vendor_key.to_string(),
        model_id: model.id.clone(),
        context_window: model.limit.context.filter(|v| *v > 0).unwrap_or(128 * 1024),
        embedding_length: None,
        output_max_tokens: model.limit.output.filter(|v| *v > 0),
        tool_call: model.tool_call,
        reasoning: model.reasoning,
        input_modalities,
        output_modalities,
        input_cost: model.cost.input,
        output_cost: model.cost.output,
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::auth::AuthExchangeInput;
    use crate::config::GatewayConfig;
    use serde_json::json;
    use std::path::PathBuf;
    use uuid::Uuid;

    const FAR_FUTURE_RFC3339: &str = "2099-01-01T00:00:00Z";
    const PAST_RFC3339: &str = "2000-01-01T00:00:00Z";
    const CODEX_RUNTIME_URL: &str = "https://chatgpt.com/backend-api/codex";

    #[tokio::test]
    async fn oauth_session_is_shared_across_admin_instances_and_cancel_deletes_it()
    -> anyhow::Result<()> {
        let gw = build_gateway().await?;

        let init = gw.admin().init_oauth_session("codex", false).await?;
        let status = gw
            .admin()
            .get_oauth_session_status(&init.session_id)
            .await?;
        assert!(matches!(status, AuthSessionStatusData::Pending { .. }));

        gw.admin().cancel_oauth_session(&init.session_id).await?;
        assert!(
            gw.admin()
                .get_auth_session_record(&init.session_id)
                .await?
                .is_none()
        );

        let err = gw
            .admin()
            .get_oauth_session_status(&init.session_id)
            .await
            .expect_err("cancelled session should be removed");
        assert!(err.to_string().contains("auth session not found"));

        Ok(())
    }

    #[tokio::test]
    async fn copy_provider_creates_provider_with_copy_suffix() -> anyhow::Result<()> {
        let gw = build_gateway().await?;
        let original = gw
            .admin()
            .create_provider(api_key_provider_input("source-provider"))
            .await?;

        let copied = gw.admin().copy_provider(&original.id).await?;

        assert_ne!(copied.id, original.id);
        assert_eq!(copied.name, "source-provider_Copy");
        assert_eq!(copied.vendor, original.vendor);
        assert_eq!(copied.protocol, original.protocol);
        assert_eq!(copied.base_url, original.base_url);
        assert_eq!(copied.preset_key, original.preset_key);
        assert_eq!(copied.channel, original.channel);
        assert_eq!(copied.models_source, original.models_source);
        assert_eq!(copied.static_models, original.static_models);
        assert_eq!(copied.api_key, original.api_key);
        assert_eq!(copied.auth_mode, original.auth_mode);
        assert_eq!(copied.use_proxy, original.use_proxy);
        assert_eq!(copied.is_enabled, original.is_enabled);

        Ok(())
    }

    #[tokio::test]
    async fn copy_provider_uses_numbered_suffix_when_copy_name_exists() -> anyhow::Result<()> {
        let gw = build_gateway().await?;
        let original = gw
            .admin()
            .create_provider(api_key_provider_input("source-provider"))
            .await?;
        gw.admin().copy_provider(&original.id).await?;

        let second_copy = gw.admin().copy_provider(&original.id).await?;

        assert_eq!(second_copy.name, "source-provider_Copy2");

        Ok(())
    }

    #[tokio::test]
    async fn copy_oauth_provider_copies_credential_binding() -> anyhow::Result<()> {
        let gw = build_gateway().await?;

        let init = gw.admin().init_oauth_session("codex", false).await?;
        seed_ready_session(
            &gw.admin(),
            &init.session_id,
            CredentialBundle {
                access_token: Some("copy-access-token".to_string()),
                refresh_token: Some("copy-refresh-token".to_string()),
                expires_at: Some(FAR_FUTURE_RFC3339.to_string()),
                resource_url: Some(CODEX_RUNTIME_URL.to_string()),
                subject_id: Some("acct_copy".to_string()),
                scopes: vec!["openid".to_string(), "offline_access".to_string()],
                raw: json!({ "access_token": "copy-access-token" }),
            },
        )
        .await?;
        let original = gw
            .admin()
            .create_provider_with_oauth_session(&init.session_id, oauth_provider_input())
            .await?;

        let copied = gw.admin().copy_provider(&original.id).await?;
        let copied_credential = gw.storage.oauth_credentials().get(&copied.id).await?;

        assert_eq!(copied.name, format!("{}_Copy", original.name));
        assert_eq!(copied.auth_mode, "oauth");
        assert!(copied.api_key.is_empty());
        assert_eq!(
            copied_credential
                .as_ref()
                .map(|cred| cred.access_token.as_str()),
            Some("copy-access-token"),
        );

        let runtime = gw.admin().resolve_provider_runtime(&copied).await?;
        assert_eq!(runtime.access_token, "copy-access-token");

        Ok(())
    }

    #[tokio::test]
    async fn failed_complete_deletes_session() -> anyhow::Result<()> {
        let gw = build_gateway().await?;

        let init = gw.admin().init_oauth_session("codex", false).await?;
        let err = gw
            .admin()
            .complete_oauth_session(
                &init.session_id,
                AuthExchangeInput {
                    code: None,
                    callback_url: Some(
                        "https://app.example/callback?code=test-code&state=wrong-state".to_string(),
                    ),
                    metadata: Value::Null,
                },
            )
            .await
            .expect_err("invalid callback state should fail the exchange");

        assert!(
            err.to_string().contains("state"),
            "unexpected complete error: {err:#}"
        );
        assert!(
            gw.admin()
                .get_auth_session_record(&init.session_id)
                .await?
                .is_none()
        );

        Ok(())
    }

    #[tokio::test]
    async fn timeout_and_cleanup_remove_expired_sessions() -> anyhow::Result<()> {
        let gw = build_gateway().await?;

        let timed_out = gw.admin().init_oauth_session("codex", false).await?;
        gw.admin()
            .update_auth_session_record(
                &timed_out.session_id,
                UpdateAuthSession {
                    expires_at: Some(PAST_RFC3339.to_string()),
                    ..Default::default()
                },
            )
            .await?;

        let status = gw
            .admin()
            .get_oauth_session_status(&timed_out.session_id)
            .await?;
        assert!(matches!(
            status,
            AuthSessionStatusData::Error { ref code, .. } if code == "AUTH_TIMEOUT"
        ));
        assert!(
            gw.admin()
                .get_auth_session_record(&timed_out.session_id)
                .await?
                .is_none()
        );

        let stale_ready = gw.admin().init_oauth_session("codex", false).await?;
        seed_ready_session(
            &gw.admin(),
            &stale_ready.session_id,
            CredentialBundle {
                access_token: Some("stale-access-token".to_string()),
                refresh_token: Some("stale-refresh-token".to_string()),
                expires_at: Some(PAST_RFC3339.to_string()),
                resource_url: None,
                subject_id: None,
                scopes: vec![],
                raw: json!({ "access_token": "stale-access-token" }),
            },
        )
        .await?;

        let removed = gw.admin().cleanup_auth_sessions().await?;
        assert_eq!(removed, 1);
        assert!(
            gw.admin()
                .get_auth_session_record(&stale_ready.session_id)
                .await?
                .is_none()
        );

        Ok(())
    }

    #[tokio::test]
    async fn logout_provider_oauth_preserves_oauth_mode_and_disconnects_binding()
    -> anyhow::Result<()> {
        let gw = build_gateway().await?;

        let init = gw.admin().init_oauth_session("codex", false).await?;
        seed_ready_session(
            &gw.admin(),
            &init.session_id,
            CredentialBundle {
                access_token: Some("test-access-token".to_string()),
                refresh_token: Some("test-refresh-token".to_string()),
                expires_at: Some(FAR_FUTURE_RFC3339.to_string()),
                resource_url: None,
                subject_id: Some("acct_test".to_string()),
                scopes: vec!["openid".to_string(), "offline_access".to_string()],
                raw: json!({ "access_token": "test-access-token" }),
            },
        )
        .await?;

        let provider = gw
            .admin()
            .create_provider_with_oauth_session(&init.session_id, oauth_provider_input())
            .await?;

        let status = gw.admin().logout_provider_oauth(&provider.id).await?;
        assert_eq!(status.status, AuthBindingStatus::Disconnected.as_str());

        let updated = gw.admin().get_provider(&provider.id).await?;
        assert_eq!(updated.effective_auth_mode(), "oauth");
        assert!(updated.api_key.is_empty());
        // After logout, OAuth credential row should be deleted
        let oauth_cred = gw.storage.oauth_credentials().get(&provider.id).await?;
        assert!(
            oauth_cred.is_none(),
            "oauth credential should be deleted after logout"
        );

        let runtime_err = match gw.admin().resolve_provider_runtime(&updated).await {
            Ok(_) => {
                anyhow::bail!("logged out oauth provider should not resolve runtime credentials")
            }
            Err(err) => err,
        };
        let runtime_err_message = runtime_err.to_string();
        assert!(
            runtime_err_message.contains("credential not found")
                || runtime_err_message.contains("access token")
                || runtime_err_message.contains("refresh token"),
            "unexpected runtime error: {runtime_err_message}"
        );

        Ok(())
    }

    #[tokio::test]
    async fn ready_session_is_single_use_and_provider_status_exposes_runtime_url()
    -> anyhow::Result<()> {
        let gw = build_gateway().await?;

        let init = gw.admin().init_oauth_session("codex", false).await?;
        seed_ready_session(
            &gw.admin(),
            &init.session_id,
            CredentialBundle {
                access_token: Some("test-access-token".to_string()),
                refresh_token: Some("test-refresh-token".to_string()),
                expires_at: Some(FAR_FUTURE_RFC3339.to_string()),
                resource_url: None,
                subject_id: Some("acct_test".to_string()),
                scopes: vec!["openid".to_string(), "offline_access".to_string()],
                raw: json!({ "access_token": "test-access-token" }),
            },
        )
        .await?;

        let provider = gw
            .admin()
            .create_provider_with_oauth_session(&init.session_id, oauth_provider_input())
            .await?;

        assert_eq!(provider.effective_auth_mode(), "oauth");
        assert_eq!(provider.base_url, CODEX_RUNTIME_URL);
        assert!(
            gw.admin()
                .get_auth_session_record(&init.session_id)
                .await?
                .is_none()
        );

        let err = gw
            .admin()
            .create_provider_with_oauth_session(&init.session_id, oauth_provider_input())
            .await
            .expect_err("consumed ready session should not be reusable");
        assert!(err.to_string().contains("auth session not found"));

        let status = gw.admin().get_provider_oauth_status(&provider.id).await?;
        assert_eq!(status.status, AuthBindingStatus::Connected.as_str());
        assert_eq!(status.resource_url.as_deref(), Some(CODEX_RUNTIME_URL));

        Ok(())
    }

    async fn build_gateway() -> anyhow::Result<Gateway> {
        let mut config = GatewayConfig::default();
        config.data_dir = test_data_dir();
        let (gw, _log_rx) = Gateway::new(config).await?;
        Ok(gw)
    }

    fn test_data_dir() -> PathBuf {
        std::env::temp_dir().join(format!("nyro-oauth-admin-tests-{}", Uuid::new_v4()))
    }

    fn oauth_provider_input() -> CreateProvider {
        CreateProvider {
            name: format!("oauth-provider-{}", Uuid::new_v4()),
            vendor: None,
            protocol: "openai".to_string(),
            base_url: "https://placeholder.invalid".to_string(),
            preset_key: Some("openai".to_string()),
            channel: Some("codex".to_string()),
            models_source: None,
            static_models: None,
            api_key: String::new(),
            auth_mode: "oauth".to_string(),
            use_proxy: false,
        }
    }

    fn api_key_provider_input(name: &str) -> CreateProvider {
        CreateProvider {
            name: name.to_string(),
            vendor: Some("openai".to_string()),
            protocol: "openai-compat".to_string(),
            base_url: "https://api.openai.com/v1".to_string(),
            preset_key: Some("openai".to_string()),
            channel: Some("default".to_string()),
            models_source: Some("https://api.openai.com/v1/models".to_string()),
            static_models: Some("gpt-test\ntext-test".to_string()),
            api_key: "sk-test".to_string(),
            auth_mode: "apikey".to_string(),
            use_proxy: true,
        }
    }

    async fn seed_ready_session(
        admin: &AdminService,
        session_id: &str,
        bundle: CredentialBundle,
    ) -> anyhow::Result<()> {
        admin
            .update_auth_session_record(
                session_id,
                UpdateAuthSession {
                    status: Some(AuthSessionStatus::Ready.as_str().to_string()),
                    result_json: Some(serde_json::to_string(&bundle)?),
                    expires_at: bundle.expires_at.clone(),
                    last_error: Some(String::new()),
                    ..Default::default()
                },
            )
            .await?;
        Ok(())
    }
}
