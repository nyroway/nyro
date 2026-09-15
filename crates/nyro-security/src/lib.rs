//! nyro-security.

use sha2::{Digest, Sha256};
use std::{
    collections::{HashMap, HashSet},
    time::{SystemTime, UNIX_EPOCH},
};
use thiserror::Error;

#[derive(Clone, serde::Serialize, serde::Deserialize)]
#[serde(deny_unknown_fields)]
pub struct ApiKey {
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

impl std::fmt::Debug for ApiKey {
    fn fmt(&self, formatter: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        formatter
            .debug_struct("ApiKey")
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

#[derive(Debug, Error)]
pub enum SecurityError {
    #[error("invalid API key configuration")]
    InvalidApiKey,
    #[error("authentication failed")]
    AuthenticationFailed,
    #[error("authorization denied")]
    AuthorizationDenied,
}

struct KeyState {
    id: String,
    enabled: bool,
    expires_at: Option<u64>,
}

pub struct ApiKeys {
    identities: HashMap<[u8; 32], KeyState>,
}

impl ApiKeys {
    pub fn new(keys: Vec<ApiKey>) -> Result<Self, SecurityError> {
        let mut ids = HashSet::with_capacity(keys.len());
        let mut identities = HashMap::with_capacity(keys.len());

        for ApiKey {
            id,
            secret,
            enabled,
            expires_at,
        } in keys
        {
            if id.is_empty() || secret.is_empty() || !ids.insert(id.clone()) {
                return Err(SecurityError::InvalidApiKey);
            }
            let hash: [u8; 32] = Sha256::digest(secret.as_bytes()).into();
            if identities
                .insert(
                    hash,
                    KeyState {
                        id,
                        enabled,
                        expires_at,
                    },
                )
                .is_some()
            {
                return Err(SecurityError::InvalidApiKey);
            }
        }

        Ok(Self { identities })
    }

    /// Check eligibility for a new operation using the system wall clock.
    /// An issued Identity is a snapshot; expiry does not revoke work already admitted.
    pub fn authenticate(&self, secret: &str) -> Result<Identity, SecurityError> {
        self.authenticate_at(secret, SystemTime::now())
    }

    /// Authenticate against a caller-supplied wall-clock instant.
    /// Expiring keys fail closed if `now` precedes the Unix epoch.
    pub fn authenticate_at(
        &self,
        secret: &str,
        now: SystemTime,
    ) -> Result<Identity, SecurityError> {
        let hash: [u8; 32] = Sha256::digest(secret.as_bytes()).into();
        let key = self
            .identities
            .get(&hash)
            .ok_or(SecurityError::AuthenticationFailed)?;
        if !key.enabled
            || key.expires_at.is_some_and(|expiry| {
                now.duration_since(UNIX_EPOCH)
                    .map_or(true, |elapsed| elapsed.as_secs() >= expiry)
            })
        {
            return Err(SecurityError::AuthenticationFailed);
        }
        Ok(Identity { id: key.id.clone() })
    }
}

#[derive(Clone, Debug, Eq, PartialEq, serde::Serialize, serde::Deserialize)]
pub struct Grant {
    pub subject: String,
    pub action: String,
    pub resource: String,
}

pub struct Authorizer {
    grants: Vec<Grant>,
}

impl Authorizer {
    pub fn new(grants: Vec<Grant>) -> Self {
        Self { grants }
    }

    pub fn authorize(
        &self,
        identity: &Identity,
        action: &str,
        resource: &str,
    ) -> Result<(), SecurityError> {
        self.grants
            .iter()
            .any(|grant| {
                grant.subject == identity.id && grant.action == action && grant.resource == resource
            })
            .then_some(())
            .ok_or(SecurityError::AuthorizationDenied)
    }
}

#[cfg(test)]
mod tests {
    use super::{ApiKey, ApiKeys, Authorizer, Grant, Identity};

    fn key(id: &str, secret: &str) -> ApiKey {
        ApiKey {
            id: id.into(),
            secret: secret.into(),
            enabled: true,
            expires_at: None,
        }
    }

    #[test]
    fn rejects_empty_or_duplicate_credentials() {
        assert!(ApiKeys::new(vec![key("", "secret")]).is_err());
        assert!(ApiKeys::new(vec![key("id", "")]).is_err());
        assert!(ApiKeys::new(vec![key("id", "a"), key("id", "b")]).is_err());
        assert!(ApiKeys::new(vec![key("one", "secret"), key("two", "secret")]).is_err());
    }

    #[test]
    fn authenticates_only_known_credentials() {
        let keys = ApiKeys::new(vec![key("deploy", "correct")]).unwrap();

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
        let keys = ApiKeys::new(vec![disabled, expired, key("active", "active-secret")]).unwrap();
        assert!(keys.authenticate("disabled-secret").is_err());
        assert!(keys.authenticate("expired-secret").is_err());
        assert!(keys.authenticate("active-secret").is_ok());
    }

    #[test]
    fn expiration_is_exclusive_and_rechecked_without_rebuilding_keys() {
        use std::time::{Duration, UNIX_EPOCH};
        let mut expiring = key("deploy", "secret");
        expiring.expires_at = Some(10);
        let keys = ApiKeys::new(vec![expiring]).unwrap();
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
            ApiKeys::new(vec![far_future])
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
            assert!(ApiKeys::new(vec![inactive.clone(), key("one", "other")]).is_err());
            assert!(ApiKeys::new(vec![inactive, key("two", "secret")]).is_err());
        }
    }

    #[test]
    fn api_key_debug_redacts_its_secret() {
        let rendered = format!("{:?}", key("deploy", "very-secret-value"));

        assert!(rendered.contains("deploy"));
        assert!(!rendered.contains("very-secret-value"));
    }

    #[test]
    fn grants_require_matching_subject_action_and_resource() {
        let authorizer = Authorizer::new(vec![Grant {
            subject: "deploy".into(),
            action: "invoke".into(),
            resource: "model:chat".into(),
        }]);
        let identity = Identity {
            id: "deploy".into(),
        };

        assert!(
            authorizer
                .authorize(&identity, "invoke", "model:chat")
                .is_ok()
        );
        assert!(
            authorizer
                .authorize(&identity, "read", "model:chat")
                .is_err()
        );
        assert!(
            authorizer
                .authorize(&identity, "invoke", "model:embed")
                .is_err()
        );
        assert!(
            authorizer
                .authorize(&Identity { id: "other".into() }, "invoke", "model:chat")
                .is_err()
        );
    }
}
