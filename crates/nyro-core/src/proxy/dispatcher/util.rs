//! Pure utility helpers for the proxy dispatcher.
//!
//! Covers: model-backend loading, retryability, runtime-binding headers,
//! and safe client header forwarding.

use reqwest::header::{
    HeaderMap as ReqwestHeaderMap, HeaderName as ReqwestHeaderName,
    HeaderValue as ReqwestHeaderValue,
};

use crate::Gateway;
use crate::db::models::{Model, ModelBackend};

// ── Model backend loading ────────────────────────────────────────────────────────

pub(super) async fn load_model_backends(gw: &Gateway, model: &Model) -> Vec<ModelBackend> {
    if let Some(store) = gw.storage.model_backends()
        && let Ok(backends) = store.list_backends_by_model(&model.id).await
        && !backends.is_empty()
    {
        return backends;
    }
    // Fallback: synthesize a single backend from the legacy
    // `model.target_provider` / `model.target_model` columns.
    if model.target_provider.trim().is_empty() {
        return Vec::new();
    }
    vec![ModelBackend {
        id: String::new(),
        model_id: model.id.clone(),
        provider_id: model.target_provider.clone(),
        model: model.target_model.clone(),
        weight: 100,
        priority: 1,
        created_at: String::new(),
    }]
}

// ── Retry ─────────────────────────────────────────────────────────────────────

pub(super) fn is_retryable(status: u16) -> bool {
    matches!(status, 408 | 429 | 500 | 502 | 503 | 529)
}

// ── Runtime-binding extra headers ─────────────────────────────────────────────

pub(super) fn runtime_binding_headers(
    binding: &crate::auth::RuntimeBinding,
) -> anyhow::Result<ReqwestHeaderMap> {
    let mut headers = ReqwestHeaderMap::new();
    for (key, value) in &binding.extra_headers {
        headers.insert(
            reqwest::header::HeaderName::from_bytes(key.as_bytes())?,
            ReqwestHeaderValue::from_str(value)?,
        );
    }
    Ok(headers)
}

/// Convert client-supplied request headers into the safe subset that may be
/// forwarded upstream.
///
/// Authentication, API-key, cookie, hop-by-hop, proxy, and client network
/// identity headers are intentionally dropped so Nyro's local credentials and
/// caller IP/host metadata never leak to providers. Provider/runtime headers
/// are merged elsewhere after this function, so internal credentials still win.
pub(super) fn forwarded_client_headers(headers: &axum::http::HeaderMap) -> ReqwestHeaderMap {
    let mut forwarded = ReqwestHeaderMap::new();
    for (name, value) in headers {
        if !should_forward_client_header(name.as_str()) {
            continue;
        }
        if let (Ok(name), Ok(value)) = (
            ReqwestHeaderName::from_bytes(name.as_str().as_bytes()),
            ReqwestHeaderValue::from_bytes(value.as_bytes()),
        ) {
            forwarded.append(name, value);
        }
    }
    forwarded
}

fn should_forward_client_header(name: &str) -> bool {
    let name = name.trim().to_ascii_lowercase();
    if name.is_empty() {
        return false;
    }

    let denied = matches!(
        name.as_str(),
        // Local/proxy credentials and cookies.
        "authorization"
            | "proxy-authorization"
            | "www-authenticate"
            | "proxy-authenticate"
            | "x-api-key"
            | "x-goog-api-key"
            | "api-key"
            | "x-auth-token"
            | "x-access-token"
            | "x-refresh-token"
            | "access-token"
            | "refresh-token"
            | "cookie"
            | "set-cookie"
            // Hop-by-hop / transport-managed headers.
            | "connection"
            | "keep-alive"
            | "te"
            | "trailer"
            | "transfer-encoding"
            | "upgrade"
            | "host"
            | "content-length"
            | "accept-encoding"
            | "content-encoding"
            // Client network identity / local origin metadata.
            | "forwarded"
            | "x-forwarded-for"
            | "x-forwarded-host"
            | "x-forwarded-proto"
            | "x-forwarded-port"
            | "x-forwarded-server"
            | "x-original-forwarded-for"
            | "x-real-ip"
            | "x-client-ip"
            | "x-cluster-client-ip"
            | "x-remote-ip"
            | "x-remote-addr"
            | "remote-host"
            | "remote-addr"
            | "cf-connecting-ip"
            | "true-client-ip"
            | "fastly-client-ip"
            | "via"
            | "origin"
            | "referer"
    ) || name.ends_with("-api-key")
        || name.starts_with("sec-")
        || name.starts_with("proxy-")
        || name.starts_with("cf-");

    !denied
}

#[cfg(test)]
mod tests {
    use axum::http::{HeaderMap, HeaderValue};

    use super::*;

    #[test]
    fn forwarded_client_headers_keep_cache_hints() {
        let mut headers = HeaderMap::new();
        headers.insert("anthropic-beta", HeaderValue::from_static("prompt-caching"));
        headers.insert("openai-beta", HeaderValue::from_static("responses=v1"));
        headers.insert("idempotency-key", HeaderValue::from_static("request-123"));

        let forwarded = forwarded_client_headers(&headers);

        assert_eq!(forwarded.get("anthropic-beta").unwrap(), "prompt-caching");
        assert_eq!(forwarded.get("openai-beta").unwrap(), "responses=v1");
        assert_eq!(forwarded.get("idempotency-key").unwrap(), "request-123");
    }

    #[test]
    fn forwarded_client_headers_drop_keys_and_sensitive_network_headers() {
        let mut headers = HeaderMap::new();
        headers.insert("authorization", HeaderValue::from_static("Bearer nyro-key"));
        headers.insert("x-api-key", HeaderValue::from_static("nyro-key"));
        headers.insert("x-goog-api-key", HeaderValue::from_static("nyro-key"));
        headers.insert(
            "proxy-authorization",
            HeaderValue::from_static("Basic secret"),
        );
        headers.insert("cookie", HeaderValue::from_static("session=secret"));
        headers.insert("x-forwarded-for", HeaderValue::from_static("10.0.0.1"));
        headers.insert("x-real-ip", HeaderValue::from_static("10.0.0.2"));
        headers.insert("remote-host", HeaderValue::from_static("client.local"));
        headers.insert("connection", HeaderValue::from_static("keep-alive"));
        headers.insert("anthropic-beta", HeaderValue::from_static("prompt-caching"));

        let forwarded = forwarded_client_headers(&headers);

        assert!(forwarded.get("authorization").is_none());
        assert!(forwarded.get("x-api-key").is_none());
        assert!(forwarded.get("x-goog-api-key").is_none());
        assert!(forwarded.get("proxy-authorization").is_none());
        assert!(forwarded.get("cookie").is_none());
        assert!(forwarded.get("x-forwarded-for").is_none());
        assert!(forwarded.get("x-real-ip").is_none());
        assert!(forwarded.get("remote-host").is_none());
        assert!(forwarded.get("connection").is_none());
        assert_eq!(forwarded.get("anthropic-beta").unwrap(), "prompt-caching");
    }

    #[test]
    fn forwarded_client_headers_drop_client_encoding_negotiation() {
        let mut headers = HeaderMap::new();
        headers.insert("accept-encoding", HeaderValue::from_static("gzip"));

        let forwarded = forwarded_client_headers(&headers);

        assert!(
            forwarded.get("accept-encoding").is_none(),
            "reqwest must own upstream response decompression; client encoding hints are only for the Nyro response"
        );
    }
}
