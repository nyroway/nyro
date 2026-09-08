//! nyro-security.

use sha2::{Digest, Sha256};
use std::collections::{HashMap, HashSet};
use thiserror::Error;

#[derive(Clone, serde::Serialize, serde::Deserialize)]
#[serde(deny_unknown_fields)]
pub struct ApiKey {
    pub id: String,
    pub secret: String,
}

impl std::fmt::Debug for ApiKey {
    fn fmt(&self, formatter: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        formatter
            .debug_struct("ApiKey")
            .field("id", &self.id)
            .field("secret", &"[REDACTED]")
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

pub struct ApiKeys {
    identities: HashMap<[u8; 32], String>,
}

impl ApiKeys {
    pub fn new(keys: Vec<ApiKey>) -> Result<Self, SecurityError> {
        let mut ids = HashSet::with_capacity(keys.len());
        let mut identities = HashMap::with_capacity(keys.len());

        for ApiKey { id, secret } in keys {
            if id.is_empty() || secret.is_empty() || !ids.insert(id.clone()) {
                return Err(SecurityError::InvalidApiKey);
            }
            let hash: [u8; 32] = Sha256::digest(secret.as_bytes()).into();
            if identities.insert(hash, id).is_some() {
                return Err(SecurityError::InvalidApiKey);
            }
        }

        Ok(Self { identities })
    }

    pub fn authenticate(&self, secret: &str) -> Result<Identity, SecurityError> {
        let hash: [u8; 32] = Sha256::digest(secret.as_bytes()).into();
        self.identities
            .get(&hash)
            .cloned()
            .map(|id| Identity { id })
            .ok_or(SecurityError::AuthenticationFailed)
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
