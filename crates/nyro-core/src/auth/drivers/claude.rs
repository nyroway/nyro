use std::collections::HashMap;

use anyhow::{Context, Result, anyhow, bail};
use async_trait::async_trait;
use reqwest::header::{ACCEPT, CONTENT_TYPE};
use serde::Deserialize;

use super::shared::{
    PkceAuthState, build_authorize_url, encode_scopes, expires_at_after, generate_code_challenge,
    generate_code_verifier, generate_state, parse_oauth_callback, parse_session_state,
    required_http_client, validate_callback_state,
};
use crate::auth::types::{
    AuthDriver, AuthDriverMetadata, AuthExchangeInput, AuthScheme, AuthSession, CreateAuthSession,
    CredentialBundle, ExchangeAuthContext, RefreshAuthContext, RuntimeBinding, StartAuthContext,
    StoredCredential,
};
use crate::db::models::Provider;
use crate::provider::OAuthConfig;
use crate::provider::VendorRegistry;

const ANTHROPIC_PRESET_ID: &str = "anthropic";
const CLAUDE_CODE_CHANNEL_ID: &str = "claude-code";
const ANTHROPIC_PROTOCOL_ID: &str = "anthropic-messages";

/// User-Agent the official `claude` CLI sends; mirrored on both the
/// OAuth token endpoint and the inference runtime. **Bump this** when
/// upgrading Claude Code CLI compatibility — Anthropic has at points
/// gated the OAuth surface on the CLI version string.
const CLAUDE_CLI_USER_AGENT: &str = "claude-cli/1.0.98 (external, cli)";

/// Anthropic OAuth-beta header the runtime + token endpoint require.
/// Bump (or drop) once the OAuth flow GAs.
const ANTHROPIC_OAUTH_BETA: &str = "oauth-2025-04-20";

#[derive(Debug, Clone, Copy)]
struct ClaudeCodeConfig {
    oauth: &'static OAuthConfig,
    api_base_url: &'static str,
    static_models: &'static [&'static str],
}

#[derive(Debug, Default)]
pub struct ClaudeOAuthDriver;

#[derive(Debug, Deserialize)]
struct ClaudeTokenResponse {
    access_token: Option<String>,
    refresh_token: Option<String>,
    expires_in: Option<i64>,
    scope: Option<String>,
}

#[derive(Debug, Deserialize)]
struct ClaudeErrorResponse {
    error: Option<String>,
    error_description: Option<String>,
}

impl ClaudeOAuthDriver {
    fn claude_code_config() -> Result<ClaudeCodeConfig> {
        let metadata = VendorRegistry::global()
            .metadata(ANTHROPIC_PRESET_ID)
            .ok_or_else(|| anyhow!("missing provider preset: {ANTHROPIC_PRESET_ID}"))?;
        let channel = metadata
            .channels
            .iter()
            .find(|c| c.id == CLAUDE_CODE_CHANNEL_ID)
            .ok_or_else(|| {
                anyhow!("missing provider channel: {ANTHROPIC_PRESET_ID}/{CLAUDE_CODE_CHANNEL_ID}")
            })?;
        let api_base_url = channel
            .base_urls
            .iter()
            .find(|entry| entry.protocol == ANTHROPIC_PROTOCOL_ID)
            .map(|entry| entry.base_url)
            .ok_or_else(|| {
                anyhow!(
                    "missing base url for protocol {ANTHROPIC_PROTOCOL_ID} in \
                     {ANTHROPIC_PRESET_ID}/{CLAUDE_CODE_CHANNEL_ID}"
                )
            })?;
        Ok(ClaudeCodeConfig {
            oauth: channel.oauth.as_ref().ok_or_else(|| {
                anyhow!("missing oauth config for {ANTHROPIC_PRESET_ID}/{CLAUDE_CODE_CHANNEL_ID}")
            })?,
            api_base_url,
            static_models: channel.static_models,
        })
    }

    fn normalize_token_response(
        body: &str,
        fallback_refresh_token: Option<&str>,
        config: ClaudeCodeConfig,
    ) -> Result<CredentialBundle> {
        let token: ClaudeTokenResponse =
            serde_json::from_str(body).context("parse claude oauth token response")?;
        let access_token = token
            .access_token
            .filter(|value| !value.trim().is_empty())
            .ok_or_else(|| anyhow!("claude oauth token response missing access_token"))?;
        let expires_in = token.expires_in.unwrap_or(3600).max(1);

        Ok(CredentialBundle {
            access_token: Some(access_token),
            refresh_token: token
                .refresh_token
                .filter(|value| !value.trim().is_empty())
                .or_else(|| fallback_refresh_token.map(ToString::to_string)),
            expires_at: Some(expires_at_after(expires_in)),
            resource_url: Some(config.api_base_url.to_string()),
            subject_id: None,
            scopes: encode_scopes(token.scope.as_deref()),
            raw: serde_json::from_str(body).unwrap_or(serde_json::Value::Null),
        })
    }

    fn parse_error(body: &str) -> Option<String> {
        let parsed: ClaudeErrorResponse = serde_json::from_str(body).ok()?;
        parsed
            .error_description
            .filter(|value| !value.trim().is_empty())
            .or_else(|| parsed.error.filter(|value| !value.trim().is_empty()))
    }

    /// Headers the official `claude` CLI sends on **both** the OAuth
    /// token endpoint and the inference runtime. The token endpoint
    /// rejects requests without `anthropic-beta: oauth-2025-04-20` with
    /// HTTP 400 `invalid_request_error: "Invalid request format"`, and
    /// has at points enforced the CLI-shaped UA/Origin/Referer too.
    /// See `CLAUDE_CLI_USER_AGENT` / `ANTHROPIC_OAUTH_BETA` for the
    /// values and their maintenance notes.
    fn claude_cli_headers() -> Vec<(&'static str, &'static str)> {
        vec![
            ("User-Agent", CLAUDE_CLI_USER_AGENT),
            ("Referer", "https://claude.ai/"),
            ("Origin", "https://claude.ai"),
            ("anthropic-beta", ANTHROPIC_OAUTH_BETA),
        ]
    }
}

#[async_trait]
impl AuthDriver for ClaudeOAuthDriver {
    fn metadata(&self) -> AuthDriverMetadata {
        AuthDriverMetadata {
            key: "claude-code",
            label: "Claude Code",
            scheme: AuthScheme::OAuthAuthCodePkce,
            supports_new_provider: true,
            supports_existing_provider: true,
        }
    }

    async fn start(&self, ctx: StartAuthContext) -> Result<CreateAuthSession> {
        let config = Self::claude_code_config()?;
        let code_verifier = generate_code_verifier();
        let code_challenge = generate_code_challenge(&code_verifier);
        let state = generate_state();
        let redirect_uri = ctx
            .redirect_uri
            .as_deref()
            .filter(|v| !v.trim().is_empty())
            .unwrap_or(config.oauth.redirect_uri);
        let auth_url = build_authorize_url(
            config.oauth.authorize_url,
            &[
                ("code", "true"),
                ("client_id", config.oauth.client_id),
                ("response_type", "code"),
                ("redirect_uri", redirect_uri),
                ("scope", config.oauth.scope),
                ("code_challenge", &code_challenge),
                ("code_challenge_method", "S256"),
                ("state", &state),
            ],
        )?;
        let session_state = serde_json::to_string(&PkceAuthState {
            code_verifier,
            state,
            redirect_uri: redirect_uri.to_string(),
        })?;

        Ok(CreateAuthSession {
            provider_id: ctx.provider_id,
            driver_key: self.metadata().key.to_string(),
            scheme: self.metadata().scheme.as_str().to_string(),
            status: "pending".to_string(),
            use_proxy: ctx.use_proxy,
            user_code: None,
            verification_uri: Some(config.oauth.auth_base_url.to_string()),
            verification_uri_complete: Some(auth_url),
            state_json: Some(session_state),
            context_json: None,
            result_json: None,
            expires_at: Some(expires_at_after(10 * 60)),
            poll_interval_seconds: Some(2),
            last_error: None,
        })
    }

    async fn exchange(
        &self,
        session: &AuthSession,
        input: AuthExchangeInput,
        ctx: ExchangeAuthContext,
    ) -> Result<CredentialBundle> {
        let config = Self::claude_code_config()?;
        let state: PkceAuthState = parse_session_state(session)?;
        let callback = parse_oauth_callback(&input)?;
        validate_callback_state(&state.state, callback.state.as_deref(), "claude")?;
        let code = callback
            .code
            .as_deref()
            .map(str::trim)
            .filter(|value| !value.is_empty())
            .ok_or_else(|| anyhow!("missing authorization code"))?;

        let client = required_http_client(ctx.http_client)?;
        // NOTE: Anthropic's token endpoint diverges from RFC 6749 — it
        // requires `state` to be echoed back in the JSON body, otherwise
        // it returns 400 "Invalid request format". Keep this field.
        let token_body = serde_json::json!({
            "grant_type": "authorization_code",
            "client_id": config.oauth.client_id,
            "code": code,
            "redirect_uri": state.redirect_uri,
            "code_verifier": state.code_verifier,
            "state": state.state,
        });

        let mut request = client
            .post(config.oauth.token_url)
            .header(CONTENT_TYPE, "application/json")
            .header(ACCEPT, "application/json");
        for (key, value) in Self::claude_cli_headers() {
            request = request.header(key, value);
        }

        let response = request
            .json(&token_body)
            .send()
            .await
            .context("exchange claude authorization code")?;

        let status = response.status();
        let body = response.text().await.unwrap_or_default();
        if !status.is_success() {
            let detail = Self::parse_error(&body).unwrap_or(body);
            bail!("claude oauth token exchange failed: HTTP {status} {detail}");
        }

        Self::normalize_token_response(&body, None, config)
    }

    async fn refresh(
        &self,
        credential: &StoredCredential,
        ctx: RefreshAuthContext,
    ) -> Result<CredentialBundle> {
        let config = Self::claude_code_config()?;
        let refresh_token = credential
            .refresh_token
            .as_deref()
            .map(str::trim)
            .filter(|value| !value.is_empty())
            .ok_or_else(|| anyhow!("claude oauth refresh token is missing"))?;
        let client = required_http_client(ctx.http_client)?;

        let token_body = serde_json::json!({
            "grant_type": "refresh_token",
            "client_id": config.oauth.client_id,
            "refresh_token": refresh_token,
        });

        let mut request = client
            .post(config.oauth.token_url)
            .header(CONTENT_TYPE, "application/json")
            .header(ACCEPT, "application/json");
        for (key, value) in Self::claude_cli_headers() {
            request = request.header(key, value);
        }

        let response = request
            .json(&token_body)
            .send()
            .await
            .context("refresh claude oauth token")?;

        let status = response.status();
        let body = response.text().await.unwrap_or_default();
        if !status.is_success() {
            let detail = Self::parse_error(&body).unwrap_or(body);
            bail!("claude oauth token refresh failed: HTTP {status} {detail}");
        }

        Self::normalize_token_response(&body, Some(refresh_token), config)
    }

    fn bind_runtime(
        &self,
        _provider: &Provider,
        credential: &StoredCredential,
    ) -> Result<RuntimeBinding> {
        let config = Self::claude_code_config()?;
        let access_token = credential
            .access_token
            .as_deref()
            .map(str::trim)
            .filter(|value| !value.is_empty())
            .ok_or_else(|| anyhow!("claude oauth access token is empty in bind_runtime"))?;

        let mut extra_headers = HashMap::new();
        extra_headers.insert(
            "authorization".to_string(),
            format!("Bearer {access_token}"),
        );
        extra_headers.insert("anthropic-version".to_string(), "2023-06-01".to_string());
        // Mirror the CLI-shaped headers token-exchange uses (User-Agent,
        // Referer, Origin, anthropic-beta) so a future runtime-gating
        // change at Anthropic does not silently 4xx mid-session. Single
        // source of truth keeps the version string in
        // `CLAUDE_CLI_USER_AGENT` / `ANTHROPIC_OAUTH_BETA`.
        for (key, value) in Self::claude_cli_headers() {
            extra_headers.insert(key.to_ascii_lowercase(), value.to_string());
        }

        let base_url_override = credential
            .resource_url
            .clone()
            .filter(|value| !value.trim().is_empty())
            .or_else(|| Some(config.api_base_url.to_string()));
        // The OAuth Bearer fundamentally cannot call Anthropic's
        // `/v1/models` (that endpoint requires `x-api-key`). Even after
        // sidestepping that and reading from the models.dev cache, the
        // full Anthropic catalog is misleading: only a curated subset
        // is actually reachable through the Claude Code OAuth
        // subscription. Plumb the channel-level `static_models` through
        // `static_models_override` so admin discovery returns exactly
        // what the official `claude` CLI / claude-relay-service treats
        // as supported.
        let static_models_override: Option<Vec<String>> = if config.static_models.is_empty() {
            None
        } else {
            Some(config.static_models.iter().map(|s| s.to_string()).collect())
        };
        let models_source_override = Some("ai://models.dev/anthropic".to_string());

        Ok(RuntimeBinding {
            base_url_override,
            extra_headers,
            model_aliases: HashMap::new(),
            models_source_override,
            disable_default_auth: true,
            static_models_override,
        })
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn test_provider() -> Provider {
        Provider {
            id: "test".into(),
            name: "test".into(),
            vendor: Some("anthropic".into()),
            protocol: "anthropic-messages".into(),
            base_url: String::new(),
            preset_key: Some("anthropic".into()),
            channel: Some("claude-code".into()),
            models_source: None,
            static_models: None,
            api_key: String::new(),
            auth_mode: "oauth".into(),
            use_proxy: false,
            last_test_success: None,
            last_test_at: None,
            is_enabled: true,
            created_at: String::new(),
            updated_at: String::new(),
        }
    }

    #[test]
    fn config_loads_from_vendor_registry() {
        let config = ClaudeOAuthDriver::claude_code_config().unwrap();
        assert_eq!(config.oauth.auth_base_url, "https://claude.ai");
        assert!(config.oauth.authorize_url.contains("claude.ai"));
        assert!(config.oauth.token_url.contains("anthropic.com"));
        assert_eq!(config.api_base_url, "https://api.anthropic.com");
    }

    #[test]
    fn normalize_token_response_uses_claude_runtime_base_url() {
        let body = r#"{"access_token":"tok_abc","refresh_token":"ref_xyz","expires_in":7200,"scope":"user:inference user:profile"}"#;
        let config = ClaudeOAuthDriver::claude_code_config().unwrap();
        let bundle = ClaudeOAuthDriver::normalize_token_response(body, None, config).unwrap();
        assert_eq!(bundle.access_token.as_deref(), Some("tok_abc"));
        assert_eq!(bundle.refresh_token.as_deref(), Some("ref_xyz"));
        assert_eq!(bundle.scopes, vec!["user:inference", "user:profile"]);
        assert_eq!(
            bundle.resource_url.as_deref(),
            Some("https://api.anthropic.com")
        );
        assert!(bundle.expires_at.is_some());
    }

    #[test]
    fn parse_error_prefers_description() {
        let body = r#"{"error":"invalid_grant","error_description":"code expired"}"#;
        assert_eq!(
            ClaudeOAuthDriver::parse_error(body).as_deref(),
            Some("code expired")
        );
    }

    #[test]
    fn bind_runtime_sets_bearer_oauth_headers_and_disables_default_auth() {
        let provider = test_provider();
        let credential = StoredCredential {
            access_token: Some("my_token".into()),
            ..Default::default()
        };
        let binding = ClaudeOAuthDriver
            .bind_runtime(&provider, &credential)
            .unwrap();
        assert_eq!(
            binding.extra_headers.get("authorization").unwrap(),
            "Bearer my_token"
        );
        assert_eq!(
            binding.extra_headers.get("anthropic-beta").unwrap(),
            "oauth-2025-04-20"
        );
        assert_eq!(
            binding.extra_headers.get("user-agent").unwrap(),
            "claude-cli/1.0.98 (external, cli)"
        );
        assert!(binding.disable_default_auth);
        assert_eq!(
            binding.base_url_override.as_deref(),
            Some("https://api.anthropic.com")
        );
        assert_eq!(
            binding.models_source_override.as_deref(),
            Some("ai://models.dev/anthropic"),
        );
        let static_models = binding
            .static_models_override
            .as_deref()
            .expect("claude-code channel must ship a curated static model list");
        for expected in [
            "claude-opus-4-6",
            "claude-sonnet-4-6",
            "claude-opus-4-5-20251101",
            "claude-sonnet-4-5-20250929",
            "claude-haiku-4-5-20251001",
        ] {
            assert!(
                static_models.iter().any(|m| m == expected),
                "expected canonical Claude Code OAuth model {expected:?} in: {static_models:?}",
            );
        }
    }
}
