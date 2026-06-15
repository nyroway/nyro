use anyhow::{Context, Result, anyhow, bail};
use base64::Engine;
use chrono::{Duration, Utc};
use rand::RngCore;
use reqwest::Url;
use serde::de::DeserializeOwned;
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};

use crate::auth::types::{AuthExchangeInput, AuthSession};

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct PkceAuthState {
    pub code_verifier: String,
    pub state: String,
    pub redirect_uri: String,
}

#[derive(Debug, Clone, Default)]
pub struct OAuthCallbackPayload {
    pub code: Option<String>,
    pub state: Option<String>,
}

pub fn expires_at_after(seconds: i64) -> String {
    (Utc::now() + Duration::seconds(seconds.max(1))).to_rfc3339()
}

pub fn encode_scopes(scope: Option<&str>) -> Vec<String> {
    scope
        .unwrap_or("")
        .split_whitespace()
        .map(str::trim)
        .filter(|value| !value.is_empty())
        .map(ToString::to_string)
        .collect()
}

pub fn generate_code_verifier() -> String {
    let mut bytes = [0u8; 32];
    rand::thread_rng().fill_bytes(&mut bytes);
    base64::engine::general_purpose::URL_SAFE_NO_PAD.encode(bytes)
}

pub fn generate_code_challenge(code_verifier: &str) -> String {
    let mut hasher = Sha256::new();
    hasher.update(code_verifier.as_bytes());
    base64::engine::general_purpose::URL_SAFE_NO_PAD.encode(hasher.finalize())
}

pub fn generate_state() -> String {
    let mut bytes = [0u8; 32];
    rand::thread_rng().fill_bytes(&mut bytes);
    base64::engine::general_purpose::URL_SAFE_NO_PAD.encode(bytes)
}

pub fn parse_session_state<T: DeserializeOwned>(session: &AuthSession) -> Result<T> {
    let raw = session
        .state_json
        .as_deref()
        .context("auth session missing state_json")?;
    serde_json::from_str(raw).context("parse auth session state")
}

pub fn parse_oauth_callback(input: &AuthExchangeInput) -> Result<OAuthCallbackPayload> {
    let mut payload = OAuthCallbackPayload::default();

    if let Some(raw_callback) = input
        .callback_url
        .as_deref()
        .map(str::trim)
        .filter(|v| !v.is_empty())
    {
        let parsed = parse_callback_like_value(raw_callback);
        if payload.code.is_none() {
            payload.code = parsed.code;
        }
        if payload.state.is_none() {
            payload.state = parsed.state;
        }
    }

    if let Some(raw_code) = input
        .code
        .as_deref()
        .map(str::trim)
        .filter(|v| !v.is_empty())
    {
        let parsed = parse_callback_like_value(raw_code);
        if payload.code.is_none() {
            payload.code = parsed.code.or_else(|| {
                Some(
                    raw_code
                        .split('#')
                        .next()
                        .unwrap_or(raw_code)
                        .trim()
                        .to_string(),
                )
            });
        }
        if payload.state.is_none() {
            // Claude's `code=true` flow shows the user a single `<code>#<state>`
            // string. When neither full URL nor `code=…&state=…` form is
            // present, also recover state from the suffix after `#`.
            payload.state = parsed.state.or_else(|| {
                raw_code
                    .split_once('#')
                    .map(|(_, rest)| rest.trim())
                    .filter(|rest| !rest.is_empty())
                    .map(ToString::to_string)
            });
        }
    }

    if payload
        .code
        .as_deref()
        .map(str::trim)
        .filter(|v| !v.is_empty())
        .is_none()
    {
        bail!("missing authorization code");
    }

    Ok(payload)
}

pub fn validate_callback_state(
    expected_state: &str,
    actual_state: Option<&str>,
    provider: &str,
) -> Result<()> {
    if expected_state.trim().is_empty() {
        return Ok(());
    }
    let Some(actual_state) = actual_state.map(str::trim).filter(|v| !v.is_empty()) else {
        bail!("{provider} OAuth state is missing");
    };
    if actual_state != expected_state {
        bail!("{provider} OAuth state mismatch");
    }
    Ok(())
}

pub fn build_authorize_url(base_url: &str, params: &[(&str, &str)]) -> Result<String> {
    let mut url =
        Url::parse(base_url).with_context(|| format!("parse authorize url: {base_url}"))?;
    {
        let mut pairs = url.query_pairs_mut();
        for (key, value) in params {
            pairs.append_pair(key, value);
        }
    }
    Ok(url.to_string())
}

fn parse_callback_like_value(raw: &str) -> OAuthCallbackPayload {
    if let Ok(url) = Url::parse(raw) {
        let mut payload = OAuthCallbackPayload::default();
        for (key, value) in url.query_pairs() {
            match key.as_ref() {
                "code" if payload.code.is_none() => payload.code = Some(value.to_string()),
                "state" if payload.state.is_none() => payload.state = Some(value.to_string()),
                _ => {}
            }
        }
        if let Some(fragment) = url.fragment() {
            let fragment_url = format!("https://callback.local/?{fragment}");
            if let Ok(fragment_parsed) = Url::parse(&fragment_url) {
                for (key, value) in fragment_parsed.query_pairs() {
                    match key.as_ref() {
                        "code" if payload.code.is_none() => payload.code = Some(value.to_string()),
                        "state" if payload.state.is_none() => {
                            payload.state = Some(value.to_string())
                        }
                        _ => {}
                    }
                }
            }
        }
        return payload;
    }

    if raw.contains("code=") || raw.contains("state=") {
        let normalized = if raw.starts_with('?') || raw.starts_with('#') {
            format!("https://callback.local/{raw}")
        } else {
            format!("https://callback.local/?{raw}")
        };
        if let Ok(url) = Url::parse(&normalized) {
            let mut payload = OAuthCallbackPayload::default();
            for (key, value) in url.query_pairs() {
                match key.as_ref() {
                    "code" if payload.code.is_none() => payload.code = Some(value.to_string()),
                    "state" if payload.state.is_none() => payload.state = Some(value.to_string()),
                    _ => {}
                }
            }
            return payload;
        }
    }

    // Claude's `code=true` flow displays the auth result as a bare
    // `<code>#<state>` string; recognize it here so it works whether pasted
    // into the callback-URL field or the code field.
    if !raw.contains(' ')
        && !raw.contains('?')
        && let Some((code_part, state_part)) = raw.split_once('#')
    {
        let code = code_part.trim();
        let state = state_part.trim();
        if !code.is_empty() && !state.is_empty() {
            return OAuthCallbackPayload {
                code: Some(code.to_string()),
                state: Some(state.to_string()),
            };
        }
    }

    OAuthCallbackPayload::default()
}

pub fn required_http_client(client: Option<reqwest::Client>) -> Result<reqwest::Client> {
    client.ok_or_else(|| anyhow!("missing auth http client"))
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::auth::types::AuthExchangeInput;

    fn code_input(code: &str) -> AuthExchangeInput {
        AuthExchangeInput {
            callback_url: None,
            code: Some(code.to_string()),
            metadata: serde_json::Value::Null,
        }
    }

    fn callback_input(callback: &str) -> AuthExchangeInput {
        AuthExchangeInput {
            callback_url: Some(callback.to_string()),
            code: None,
            metadata: serde_json::Value::Null,
        }
    }

    #[test]
    fn parse_code_hash_state_form_in_code_field() {
        let payload = parse_oauth_callback(&code_input("auth_abc123#state_xyz789")).unwrap();
        assert_eq!(payload.code.as_deref(), Some("auth_abc123"));
        assert_eq!(payload.state.as_deref(), Some("state_xyz789"));
    }

    #[test]
    fn parse_code_hash_state_form_in_callback_field() {
        let payload = parse_oauth_callback(&callback_input("auth_abc123#state_xyz789")).unwrap();
        assert_eq!(payload.code.as_deref(), Some("auth_abc123"));
        assert_eq!(payload.state.as_deref(), Some("state_xyz789"));
    }

    #[test]
    fn parse_callback_url_form_still_works() {
        let payload =
            parse_oauth_callback(&callback_input("https://example.com/cb?code=abc&state=xyz"))
                .unwrap();
        assert_eq!(payload.code.as_deref(), Some("abc"));
        assert_eq!(payload.state.as_deref(), Some("xyz"));
    }

    #[test]
    fn validate_state_rejects_missing() {
        assert!(validate_callback_state("expected", None, "claude").is_err());
    }
}
