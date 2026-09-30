//! HTTP is an adapter around the same Hub used by embedded data planes.
use crate::{Error, Hub, MAX_CONFIG_BYTES, Poll, Snapshot, fingerprint};
use axum::{
    Router,
    extract::{DefaultBodyLimit, State},
    http::{HeaderMap, StatusCode},
    response::{IntoResponse, Response},
    routing::post,
};
use futures::StreamExt;
use serde::{Serialize, de::DeserializeOwned};
use sha2::{Digest, Sha256};
use std::{marker::PhantomData, sync::Arc, time::Duration};

const MAX_MESSAGE_BYTES: usize = MAX_CONFIG_BYTES + 4096;
const ENDPOINT: &str = "/v1/config/sync";

fn secret_hash(token: &str) -> Result<[u8; 32], Error> {
    if !(16..=1024).contains(&token.len()) || !token.bytes().all(|b| b.is_ascii_graphic()) {
        return Err(Error::InvalidRequest);
    }
    Ok(Sha256::digest(token.as_bytes()).into())
}

struct HttpState<T> {
    hub: Hub<T>,
    secret: [u8; 32],
}

/// Mount behind a TLS listener or a trusted TLS terminator. The supplied token
/// belongs only to synchronization, never to a consumer or the administration API.
pub fn router<T: Serialize + Send + Sync + 'static>(
    hub: Hub<T>,
    token: &str,
) -> Result<Router, Error> {
    Ok(Router::new()
        .route(ENDPOINT, post(poll::<T>))
        .layer(DefaultBodyLimit::max(16 * 1024))
        .with_state(Arc::new(HttpState {
            hub,
            secret: secret_hash(token)?,
        })))
}

async fn poll<T: Serialize + Send + Sync + 'static>(
    State(state): State<Arc<HttpState<T>>>,
    headers: HeaderMap,
    body: axum::body::Bytes,
) -> Response {
    let mut auth = headers.get_all("authorization").iter();
    let hash = auth
        .next()
        .and_then(|v| v.to_str().ok())
        .and_then(|v| v.split_once(' '))
        .filter(|(scheme, _)| scheme.eq_ignore_ascii_case("bearer"))
        .and_then(|(_, secret)| secret_hash(secret).ok());
    let valid = hash.is_some_and(|hash| {
        hash.iter()
            .zip(state.secret)
            .fold(0u8, |diff, (a, b)| diff | (a ^ b))
            == 0
    });
    if !valid || auth.next().is_some() {
        return StatusCode::UNAUTHORIZED.into_response();
    }
    let request: Poll = match serde_json::from_slice(&body) {
        Ok(request) => request,
        Err(_) => return StatusCode::BAD_REQUEST.into_response(),
    };
    let response = match state.hub.poll(request, Duration::from_secs(25)).await {
        Ok(Some(snapshot)) => match serde_json::to_vec(&*snapshot) {
            Ok(bytes) => ([("content-type", "application/json")], bytes).into_response(),
            Err(_) => StatusCode::INTERNAL_SERVER_ERROR.into_response(),
        },
        Ok(None) => StatusCode::NO_CONTENT.into_response(),
        Err(Error::InvalidRequest) => StatusCode::BAD_REQUEST.into_response(),
        Err(Error::Capacity) => StatusCode::SERVICE_UNAVAILABLE.into_response(),
        Err(_) => StatusCode::INTERNAL_SERVER_ERROR.into_response(),
    };
    let mut response = response;
    response
        .headers_mut()
        .insert("cache-control", "no-store".parse().unwrap());
    response
}

/// One bounded long-poll exchange. The owning client loop controls cancellation,
/// application feedback, and retry backoff; transport errors never expose URLs.
pub struct HttpClient<T> {
    http: reqwest::Client,
    endpoint: reqwest::Url,
    token: reqwest::header::HeaderValue,
    payload: PhantomData<fn() -> T>,
}

impl<T: Serialize + DeserializeOwned> HttpClient<T> {
    pub fn new(server: &str, token: &str) -> Result<Self, Error> {
        secret_hash(token)?;
        let mut endpoint = reqwest::Url::parse(server).map_err(|_| Error::InvalidRequest)?;
        let loopback = endpoint
            .host_str()
            .and_then(|s| s.trim_matches(['[', ']']).parse::<std::net::IpAddr>().ok())
            .is_some_and(|ip| ip.is_loopback());
        if !(endpoint.scheme() == "https" || endpoint.scheme() == "http" && loopback)
            || endpoint.host_str().is_none()
            || !endpoint.username().is_empty()
            || endpoint.password().is_some()
            || endpoint.query().is_some()
            || endpoint.fragment().is_some()
            || endpoint.path() != "/"
        {
            return Err(Error::InvalidRequest);
        }
        endpoint.set_path(ENDPOINT);
        let mut token = reqwest::header::HeaderValue::from_str(&format!("Bearer {token}"))
            .map_err(|_| Error::InvalidRequest)?;
        token.set_sensitive(true);
        let http = reqwest::Client::builder()
            .no_proxy()
            .redirect(reqwest::redirect::Policy::none())
            .retry(reqwest::retry::never())
            .connect_timeout(Duration::from_secs(10))
            .timeout(Duration::from_secs(35))
            .build()
            .map_err(|_| Error::Unavailable)?;
        Ok(Self {
            http,
            endpoint,
            token,
            payload: PhantomData,
        })
    }

    pub async fn poll(&self, poll: &Poll) -> Result<Option<Snapshot<T>>, Error> {
        let response = self
            .http
            .post(self.endpoint.clone())
            .header("authorization", self.token.clone())
            .json(poll)
            .send()
            .await
            .map_err(|_| Error::Unavailable)?;
        if response.status() == reqwest::StatusCode::NO_CONTENT {
            return Ok(None);
        }
        if response.status() != reqwest::StatusCode::OK {
            return Err(Error::Unavailable);
        }
        if response
            .content_length()
            .is_some_and(|n| n > MAX_MESSAGE_BYTES as u64)
        {
            return Err(Error::TooLarge);
        }
        let mut stream = response.bytes_stream();
        let mut bytes = Vec::new();
        while let Some(chunk) = stream.next().await {
            let chunk = chunk.map_err(|_| Error::Unavailable)?;
            if chunk.len() > MAX_MESSAGE_BYTES.saturating_sub(bytes.len()) {
                return Err(Error::TooLarge);
            }
            bytes.extend_from_slice(&chunk);
        }
        let snapshot: Snapshot<T> =
            serde_json::from_slice(&bytes).map_err(|_| Error::Serialization)?;
        if snapshot.version.epoch.is_empty()
            || snapshot.version.epoch.len() > 128
            || snapshot.version.sequence == 0
            || snapshot.fingerprint != fingerprint(&snapshot.config)?
        {
            return Err(Error::InvalidRequest);
        }
        Ok(Some(snapshot))
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::collections::HashMap;

    #[tokio::test]
    async fn unordered_maps_survive_a_real_http_roundtrip() {
        let payload: HashMap<_, _> = (0..50).map(|i| (format!("key-{i}"), i)).collect();
        let hub = Hub::new(payload.clone()).unwrap();
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let url = format!("http://{}", listener.local_addr().unwrap());
        let app = router(hub, "a-dedicated-sync-token").unwrap();
        let server = tokio::spawn(async move { axum::serve(listener, app).await.unwrap() });
        let client =
            HttpClient::<HashMap<String, i32>>::new(&url, "a-dedicated-sync-token").unwrap();
        assert_eq!(
            client
                .poll(&Poll::new("node"))
                .await
                .unwrap()
                .unwrap()
                .config,
            payload
        );
        server.abort();
        let _ = server.await;
    }

    #[tokio::test]
    async fn chunked_response_size_is_bounded_without_content_length() {
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let url = format!("http://{}", listener.local_addr().unwrap());
        let app = Router::new().route(
            ENDPOINT,
            post(|| async {
                axum::body::Body::from_stream(futures::stream::iter([
                    Ok::<_, std::io::Error>(vec![b'x'; MAX_MESSAGE_BYTES]),
                    Ok(vec![b'x'; 1]),
                ]))
            }),
        );
        let server = tokio::spawn(async move { axum::serve(listener, app).await.unwrap() });
        let client = HttpClient::<serde_json::Value>::new(&url, "a-dedicated-sync-token").unwrap();
        assert!(matches!(
            client.poll(&Poll::new("node")).await,
            Err(Error::TooLarge)
        ));
        server.abort();
        let _ = server.await;
    }
}
