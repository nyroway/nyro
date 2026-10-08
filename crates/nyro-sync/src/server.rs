use crate::{ApplicationResult, Error, NodeStatus, Poll, Snapshot, Version, fingerprint};
use serde::Serialize;
use std::{
    collections::HashMap,
    sync::{Arc, Mutex},
    time::{Duration, Instant},
};
use tokio::sync::watch;

struct Node {
    status: NodeStatus,
    seen: Instant,
}

struct Shared<T> {
    snapshots: watch::Sender<Arc<Snapshot<T>>>,
    publishing: Mutex<()>,
    nodes: Mutex<HashMap<String, Node>>,
    closed: tokio_util::sync::CancellationToken,
}

/// In-process configuration source. The HTTP adapter uses the same poll operation.
pub struct Hub<T> {
    shared: Arc<Shared<T>>,
}

impl<T> Clone for Hub<T> {
    fn clone(&self) -> Self {
        Self {
            shared: self.shared.clone(),
        }
    }
}

impl<T: Serialize> Hub<T> {
    pub fn new(config: T) -> Result<Self, Error> {
        let snapshot = Snapshot {
            fingerprint: fingerprint(&config)?,
            version: Version {
                epoch: uuid::Uuid::new_v4().to_string(),
                sequence: 1,
            },
            config,
        };
        let (snapshots, _) = watch::channel(Arc::new(snapshot));
        Ok(Self {
            shared: Arc::new(Shared {
                snapshots,
                publishing: Mutex::new(()),
                nodes: Mutex::new(HashMap::new()),
                closed: tokio_util::sync::CancellationToken::new(),
            }),
        })
    }

    pub fn current(&self) -> Arc<Snapshot<T>> {
        self.shared.snapshots.borrow().clone()
    }

    /// Stop serving snapshots and wake outstanding polls during shutdown.
    pub fn close(&self) {
        let _guard = self.shared.publishing.lock().unwrap();
        self.shared.closed.cancel();
    }

    pub fn publish(&self, config: T) -> Result<Arc<Snapshot<T>>, Error> {
        let fingerprint = fingerprint(&config)?;
        let _guard = self.shared.publishing.lock().unwrap();
        if self.shared.closed.is_cancelled() {
            return Err(Error::Unavailable);
        }
        let current = self.current();
        if current.fingerprint == fingerprint {
            return Ok(current);
        }
        let next = Arc::new(Snapshot {
            version: Version {
                epoch: current.version.epoch.clone(),
                sequence: current
                    .version
                    .sequence
                    .checked_add(1)
                    .ok_or(Error::Capacity)?,
            },
            fingerprint,
            config,
        });
        self.shared.snapshots.send_replace(next.clone());
        Ok(next)
    }

    pub fn node(&self, id: &str) -> Option<NodeStatus> {
        self.shared
            .nodes
            .lock()
            .unwrap()
            .get(id)
            .map(|node| node.status.clone())
    }

    pub async fn poll(
        &self,
        request: Poll,
        wait: Duration,
    ) -> Result<Option<Arc<Snapshot<T>>>, Error> {
        if self.shared.closed.is_cancelled() {
            return Err(Error::Unavailable);
        }
        if request.node_id.is_empty()
            || request.node_id.len() > 128
            || !request
                .node_id
                .bytes()
                .all(|b| b.is_ascii_alphanumeric() || b"-_.".contains(&b))
        {
            return Err(Error::InvalidRequest);
        }
        // Cap wait even for the in-process caller. Node expiry always exceeds this interval.
        let wait = wait.min(Duration::from_secs(30));
        let fresh_node = {
            let mut nodes = self.shared.nodes.lock().unwrap();
            nodes.retain(|_, node| node.seen.elapsed() < Duration::from_secs(300));
            if nodes.len() >= 10_000 && !nodes.contains_key(&request.node_id) {
                return Err(Error::Capacity);
            }
            let node = nodes
                .entry(request.node_id.clone())
                .or_insert_with(|| Node {
                    status: NodeStatus::default(),
                    seen: Instant::now(),
                });
            let fresh_node = node.status.sent.is_none();
            node.seen = Instant::now();
            if let Some(result) = request.result {
                // Ignore late feedback after reconnect/another response, rather than relabeling
                // an unobserved version as applied. Repeated feedback is idempotent.
                if node.status.sent.as_ref() == Some(result.version()) {
                    match result {
                        ApplicationResult::Applied { version } => {
                            node.status.applied = Some(version);
                            node.status.rejected = None;
                            node.status.error_code = None;
                        }
                        ApplicationResult::Rejected { version, code } => {
                            if code.is_empty()
                                || code.len() > 64
                                || !code.bytes().all(|b| b.is_ascii_lowercase() || b == b'_')
                            {
                                return Err(Error::InvalidRequest);
                            }
                            node.status.rejected = Some(version);
                            node.status.error_code = Some(code);
                        }
                    }
                }
            }
            fresh_node
        };
        let mut receiver = self.shared.snapshots.subscribe();
        let receive = async {
            loop {
                let snapshot = receiver.borrow_and_update().clone();
                if fresh_node || request.received.as_ref() != Some(&snapshot.version) {
                    return Ok(snapshot);
                }
                receiver.changed().await.map_err(|_| Error::Unavailable)?;
            }
        };
        let outcome = tokio::select! {
            biased;
            _ = self.shared.closed.cancelled() => return Err(Error::Unavailable),
            outcome = tokio::time::timeout(wait, receive) => outcome,
        };
        match outcome {
            Ok(result) => {
                let snapshot = result?;
                if let Some(node) = self.shared.nodes.lock().unwrap().get_mut(&request.node_id) {
                    node.status.sent = Some(snapshot.version.clone());
                }
                Ok(Some(snapshot))
            }
            Err(_) => Ok(None),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    #[tokio::test]
    async fn closing_hub_interrupts_idle_poll() {
        let hub = Hub::new(serde_json::json!({})).unwrap();
        let first = hub
            .poll(Poll::new("node"), Duration::ZERO)
            .await
            .unwrap()
            .unwrap();
        let waiter = hub.clone();
        let pending = tokio::spawn(async move {
            waiter
                .poll(
                    Poll {
                        node_id: "node".into(),
                        received: Some(first.version.clone()),
                        result: None,
                    },
                    Duration::from_secs(25),
                )
                .await
        });
        tokio::task::yield_now().await;
        hub.close();
        assert!(matches!(
            tokio::time::timeout(Duration::from_millis(100), pending)
                .await
                .unwrap()
                .unwrap(),
            Err(Error::Unavailable)
        ));
        assert!(matches!(
            hub.publish(serde_json::json!({})),
            Err(Error::Unavailable)
        ));
    }
    #[tokio::test]
    async fn stale_feedback_cannot_mark_a_newer_snapshot_as_applied() {
        let hub = Hub::new(serde_json::json!({})).unwrap();
        let first = hub
            .poll(Poll::new("node"), Duration::ZERO)
            .await
            .unwrap()
            .unwrap();
        let next = hub.publish(serde_json::json!({"new":true})).unwrap();
        hub.poll(Poll::new("node"), Duration::ZERO).await.unwrap();
        hub.poll(
            Poll {
                node_id: "node".into(),
                received: Some(next.version.clone()),
                result: Some(ApplicationResult::Applied {
                    version: first.version.clone(),
                }),
            },
            Duration::ZERO,
        )
        .await
        .unwrap();
        let status = hub.node("node").unwrap();
        assert_eq!(status.sent, Some(next.version.clone()));
        assert_eq!(status.applied, None);
    }
    #[tokio::test]
    async fn expired_node_gets_a_fresh_exchange_even_for_an_unchanged_configuration() {
        let hub = Hub::new(serde_json::json!({})).unwrap();
        let mut poll = Poll::new("node");
        let first = hub
            .poll(poll.clone(), Duration::ZERO)
            .await
            .unwrap()
            .unwrap();
        poll.received = Some(first.version.clone());
        poll.result = Some(ApplicationResult::Applied {
            version: first.version.clone(),
        });
        hub.poll(poll.clone(), Duration::ZERO).await.unwrap();
        hub.shared
            .nodes
            .lock()
            .unwrap()
            .get_mut("node")
            .unwrap()
            .seen = Instant::now() - Duration::from_secs(301);
        assert!(
            hub.poll(poll.clone(), Duration::ZERO)
                .await
                .unwrap()
                .is_some()
        );
        hub.poll(poll, Duration::ZERO).await.unwrap();
        assert_eq!(
            hub.node("node").unwrap().applied,
            Some(first.version.clone())
        );
    }
}
