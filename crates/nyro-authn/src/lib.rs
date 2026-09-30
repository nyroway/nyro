//! Transport-neutral identity and key authentication.

use sha2::{Digest, Sha256};
use std::{
    collections::{HashMap, HashSet},
    time::{SystemTime, UNIX_EPOCH},
};
use thiserror::Error;

#[derive(Clone, serde::Serialize, serde::Deserialize)]
#[serde(deny_unknown_fields)]
pub struct KeyCredential {
    pub id: String,
    pub secret: String,
    #[serde(default = "default_enabled")]
    pub enabled: bool,
    /// Exclusive expiry, in whole seconds since the Unix epoch. None never expires.
    #[serde(default)]
    pub expires_at: Option<u64>,
}

const fn default_enabled() -> bool {
    true
}

impl std::fmt::Debug for KeyCredential {
    fn fmt(&self, formatter: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        formatter
            .debug_struct("KeyCredential")
            .field("id", &self.id)
            .field("secret", &"[REDACTED]")
            .field("enabled", &self.enabled)
            .field("expires_at", &self.expires_at)
            .finish()
    }
}

#[derive(Clone, Debug, Eq, PartialEq, serde::Serialize, serde::Deserialize)]
pub struct Identity {
    pub id: String,
}

/// Authentication implementations return a stable subject, never an upstream credential.
pub trait Authenticator: Send + Sync {
    fn authenticate(&self, secret: &str) -> Result<Identity, AuthenticationError>;
}

#[derive(Debug, Error)]
pub enum AuthenticationError {
    #[error("invalid API key configuration")]
    InvalidKeyCredential,
    #[error("authentication failed")]
    AuthenticationFailed,
}

struct KeyState {
    id: String,
    enabled: bool,
    expires_at: Option<u64>,
}

pub struct KeyAuth {
    identities: HashMap<[u8; 32], KeyState>,
}

impl KeyAuth {
    pub fn new(keys: Vec<KeyCredential>) -> Result<Self, AuthenticationError> {
        Self::for_subjects(keys.into_iter().map(|key| (key.id.clone(), key)).collect())
    }

    /// Several independently named credentials may resolve to one consumer identity.
    pub fn for_subjects(keys: Vec<(String, KeyCredential)>) -> Result<Self, AuthenticationError> {
        let mut ids = HashSet::with_capacity(keys.len());
        let mut identities = HashMap::with_capacity(keys.len());

        for (
            subject,
            KeyCredential {
                id,
                secret,
                enabled,
                expires_at,
            },
        ) in keys
        {
            if subject.is_empty() || id.is_empty() || secret.is_empty() || !ids.insert(id.clone()) {
                return Err(AuthenticationError::InvalidKeyCredential);
            }
            let hash: [u8; 32] = Sha256::digest(secret.as_bytes()).into();
            if identities
                .insert(
                    hash,
                    KeyState {
                        id: subject,
                        enabled,
                        expires_at,
                    },
                )
                .is_some()
            {
                return Err(AuthenticationError::InvalidKeyCredential);
            }
        }

        Ok(Self { identities })
    }

    /// Check eligibility for a new operation using the system wall clock.
    /// An issued Identity is a snapshot; expiry does not revoke work already admitted.
    pub fn authenticate(&self, secret: &str) -> Result<Identity, AuthenticationError> {
        self.authenticate_at(secret, SystemTime::now())
    }

    /// Authenticate against a caller-supplied wall-clock instant.
    /// Expiring keys fail closed if `now` precedes the Unix epoch.
    pub fn authenticate_at(
        &self,
        secret: &str,
        now: SystemTime,
    ) -> Result<Identity, AuthenticationError> {
        let hash: [u8; 32] = Sha256::digest(secret.as_bytes()).into();
        let key = self
            .identities
            .get(&hash)
            .ok_or(AuthenticationError::AuthenticationFailed)?;
        if !key.enabled
            || key.expires_at.is_some_and(|expiry| {
                now.duration_since(UNIX_EPOCH)
                    .map_or(true, |elapsed| elapsed.as_secs() >= expiry)
            })
        {
            return Err(AuthenticationError::AuthenticationFailed);
        }
        Ok(Identity { id: key.id.clone() })
    }
}

impl Authenticator for KeyAuth {
    fn authenticate(&self, secret: &str) -> Result<Identity, AuthenticationError> {
        KeyAuth::authenticate(self, secret)
    }
}

#[cfg(test)]
mod tests {
    use super::{KeyAuth, KeyCredential};

    #[test]
    fn rotated_credentials_resolve_to_the_same_consumer() {
        let keys = KeyAuth::for_subjects(vec![
            ("consumer-uid".into(), key("old-key", "old-secret")),
            ("consumer-uid".into(), key("new-key", "new-secret")),
        ])
        .unwrap();
        assert_eq!(keys.authenticate("old-secret").unwrap().id, "consumer-uid");
        assert_eq!(keys.authenticate("new-secret").unwrap().id, "consumer-uid");
        assert!(KeyAuth::for_subjects(vec![(String::new(), key("key", "secret"))]).is_err());
    }

    fn key(id: &str, secret: &str) -> KeyCredential {
        KeyCredential {
            id: id.into(),
            secret: secret.into(),
            enabled: true,
            expires_at: None,
        }
    }

    #[test]
    fn rejects_empty_or_duplicate_credentials() {
        assert!(KeyAuth::new(vec![key("", "secret")]).is_err());
        assert!(KeyAuth::new(vec![key("id", "")]).is_err());
        assert!(KeyAuth::new(vec![key("id", "a"), key("id", "b")]).is_err());
        assert!(KeyAuth::new(vec![key("one", "secret"), key("two", "secret")]).is_err());
    }

    #[test]
    fn authenticates_only_known_credentials() {
        let keys = KeyAuth::new(vec![key("deploy", "correct")]).unwrap();

        assert_eq!(keys.authenticate("correct").unwrap().id, "deploy");
        assert!(keys.authenticate("wrong").is_err());
        assert!(keys.authenticate("").is_err());
    }

    #[test]
    fn disabled_and_expired_credentials_fail_authentication() {
        let mut disabled = key("disabled", "disabled-secret");
        disabled.enabled = false;
        let mut expired = key("expired", "expired-secret");
        expired.expires_at = Some(0);
        let keys = KeyAuth::new(vec![disabled, expired, key("active", "active-secret")]).unwrap();
        assert!(keys.authenticate("disabled-secret").is_err());
        assert!(keys.authenticate("expired-secret").is_err());
        assert!(keys.authenticate("active-secret").is_ok());
    }

    #[test]
    fn expiration_is_exclusive_and_rechecked_without_rebuilding_keys() {
        use std::time::{Duration, UNIX_EPOCH};
        let mut expiring = key("deploy", "secret");
        expiring.expires_at = Some(10);
        let keys = KeyAuth::new(vec![expiring]).unwrap();
        let identity = keys
            .authenticate_at("secret", UNIX_EPOCH + Duration::from_millis(9999))
            .unwrap();
        assert!(
            keys.authenticate_at("secret", UNIX_EPOCH + Duration::from_secs(10))
                .is_err()
        );
        assert!(
            keys.authenticate_at("secret", UNIX_EPOCH + Duration::from_secs(11))
                .is_err()
        );
        assert!(
            keys.authenticate_at("secret", UNIX_EPOCH - Duration::from_secs(1))
                .is_err()
        );
        // Authentication does not mutate existing identities or establish a sticky
        // revocation cache. A corrected wall clock is used on the next admission.
        assert_eq!(identity.id, "deploy");
        assert!(keys.authenticate_at("secret", UNIX_EPOCH).is_ok());
        let mut far_future = key("far", "future");
        far_future.expires_at = Some(u64::MAX);
        assert!(
            KeyAuth::new(vec![far_future])
                .unwrap()
                .authenticate("future")
                .is_ok()
        );
    }

    #[test]
    fn inactive_credentials_still_participate_in_duplicate_validation() {
        for expired in [false, true] {
            let mut inactive = key("one", "secret");
            inactive.enabled = expired;
            inactive.expires_at = expired.then_some(0);
            assert!(KeyAuth::new(vec![inactive.clone(), key("one", "other")]).is_err());
            assert!(KeyAuth::new(vec![inactive, key("two", "secret")]).is_err());
        }
    }

    #[test]
    fn api_key_debug_redacts_its_secret() {
        let rendered = format!("{:?}", key("deploy", "very-secret-value"));

        assert!(rendered.contains("deploy"));
        assert!(!rendered.contains("very-secret-value"));
    }
}

pub mod outbound;
