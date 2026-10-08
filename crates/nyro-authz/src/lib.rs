//! Exact subject/action/resource authorization.
use nyro_authn::Identity;

#[derive(Debug, thiserror::Error)]
#[error("authorization denied")]
pub struct AuthorizationDenied;

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
    ) -> Result<(), AuthorizationDenied> {
        self.grants
            .iter()
            .any(|grant| {
                grant.subject == identity.id && grant.action == action && grant.resource == resource
            })
            .then_some(())
            .ok_or(AuthorizationDenied)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
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
