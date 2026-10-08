//! Configuration snapshots and application acknowledgements, independent of storage and runtimes.
mod client;
mod http;
mod protocol;
mod server;
pub use client::{ApplyError, Source, run};
pub use http::{HttpClient, router as http_router};
pub use protocol::*;
pub use server::Hub;

#[cfg(test)]
mod tests {
    use super::*;
    use std::time::Duration;
    #[tokio::test]
    async fn memory_delivers_updates_and_keeps_received_separate_from_applied() {
        let hub = Hub::new(serde_json::json!({"version":1})).unwrap();
        let mut request = Poll::new("node");
        let first = hub
            .poll(request.clone(), Duration::ZERO)
            .await
            .unwrap()
            .unwrap();
        request.received = Some(first.version.clone());
        request.result = Some(ApplicationResult::Rejected {
            version: first.version.clone(),
            code: "invalid_configuration".into(),
        });
        assert!(
            hub.poll(request.clone(), Duration::ZERO)
                .await
                .unwrap()
                .is_none()
        );
        assert!(hub.node("node").unwrap().applied.is_none());
        let second = hub
            .publish(serde_json::json!({"version":1,"models":[]}))
            .unwrap();
        let delivered = hub
            .poll(request.clone(), Duration::ZERO)
            .await
            .unwrap()
            .unwrap();
        assert_eq!(second.version, delivered.version);
        request.received = Some(second.version.clone());
        request.result = Some(ApplicationResult::Applied {
            version: second.version.clone(),
        });
        hub.poll(request, Duration::ZERO).await.unwrap();
        assert_eq!(
            hub.node("node").unwrap().applied,
            Some(second.version.clone())
        );
    }
    #[tokio::test]
    async fn duplicates_do_not_publish_and_pending_waiters_do_not_miss_updates() {
        let hub = Hub::new(serde_json::json!({})).unwrap();
        let first = hub.current();
        assert_eq!(
            hub.publish(serde_json::json!({})).unwrap().version,
            first.version
        );
        let mut poll = Poll::new("node");
        poll.received = Some(first.version.clone());
        let reader = hub.clone();
        let waiting = tokio::spawn(async move {
            reader
                .poll(poll, Duration::from_secs(2))
                .await
                .unwrap()
                .unwrap()
        });
        let next = hub.publish(serde_json::json!({"changed":true})).unwrap();
        assert_eq!(waiting.await.unwrap().version, next.version);
    }

    #[tokio::test]
    async fn http_uses_the_same_snapshot_and_feedback_contract_and_requires_authentication() {
        let hub = Hub::new(serde_json::json!({"version":1})).unwrap();
        let app = http_router(hub.clone(), "a-dedicated-sync-token").unwrap();
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let url = format!("http://{}", listener.local_addr().unwrap());
        let server = tokio::spawn(async move { axum::serve(listener, app).await.unwrap() });
        let client = HttpClient::<serde_json::Value>::new(&url, "a-dedicated-sync-token").unwrap();
        let snapshot = client.poll(&Poll::new("node")).await.unwrap().unwrap();
        assert_eq!(snapshot.version, hub.current().version);
        let wrong = HttpClient::<serde_json::Value>::new(&url, "a-wrong-sync-token").unwrap();
        assert!(wrong.poll(&Poll::new("other")).await.is_err());
        let no_auth = reqwest::Client::new()
            .post(format!("{url}/v1/config/sync"))
            .json(&Poll::new("other"))
            .send()
            .await
            .unwrap();
        assert_eq!(no_auth.status(), reqwest::StatusCode::UNAUTHORIZED);
        assert!(hub.node("other").is_none());
        assert!(
            HttpClient::<serde_json::Value>::new("http://example.com", "a-dedicated-sync-token")
                .is_err()
        );
        server.abort();
        let _ = server.await;
    }

    #[tokio::test]
    async fn application_loop_reports_success_and_cancels_an_idle_subscription() {
        let hub = Hub::new(serde_json::json!({})).unwrap();
        let stop = tokio_util::sync::CancellationToken::new();
        let reader = hub.clone();
        let cancel = stop.clone();
        let (applied, mut observed) = tokio::sync::mpsc::channel(2);
        let task = tokio::spawn(async move {
            run(&reader, "node", cancel, |snapshot| {
                let applied = applied.clone();
                async move {
                    applied.send(snapshot.version.clone()).await.unwrap();
                    Ok(())
                }
            })
            .await
        });
        let first = observed.recv().await.unwrap();
        tokio::task::yield_now().await;
        assert_eq!(hub.node("node").unwrap().applied, Some(first));
        hub.publish(serde_json::json!({"new":true})).unwrap();
        observed.recv().await.unwrap();
        stop.cancel();
        tokio::time::timeout(Duration::from_secs(1), task)
            .await
            .unwrap()
            .unwrap()
            .unwrap();
    }
}
