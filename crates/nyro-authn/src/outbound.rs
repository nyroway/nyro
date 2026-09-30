//! Key credentials applied by HTTP adapters. Debug output never exposes the secret.
use http::{
    HeaderMap,
    header::{HeaderName, HeaderValue},
};
use serde::{Deserialize, Serialize};

#[derive(Clone, Debug, Serialize, Deserialize)]
pub enum Kind {
    #[serde(rename = "key-auth")]
    KeyAuth,
}
#[derive(Clone, Copy, Debug, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum Location {
    Header,
    Query,
}
#[derive(Clone, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct KeyAuth {
    #[serde(rename = "type")]
    pub kind: Kind,
    #[serde(rename = "in")]
    pub location: Location,
    pub name: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub prefix: Option<String>,
    pub secret: String,
}
impl std::fmt::Debug for KeyAuth {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("KeyAuth")
            .field("in", &self.location)
            .field("name", &self.name)
            .field("secret", &"[REDACTED]")
            .finish()
    }
}
#[derive(Debug, thiserror::Error)]
#[error("invalid outgoing key authentication")]
pub struct Error;
impl KeyAuth {
    pub fn validate(&self) -> Result<(), Error> {
        if self.name.is_empty() || self.secret.is_empty() {
            return Err(Error);
        }
        match self.location {
            Location::Header => {
                HeaderName::from_bytes(self.name.as_bytes()).map_err(|_| Error)?;
                if self
                    .prefix
                    .as_ref()
                    .is_some_and(|p| p.bytes().any(|b| !b.is_ascii_graphic()))
                {
                    return Err(Error);
                }
                self.header_value()?;
            }
            Location::Query => {
                if self.prefix.is_some() || self.name.chars().any(char::is_control) {
                    return Err(Error);
                }
            }
        }
        Ok(())
    }
    fn header_value(&self) -> Result<HeaderValue, Error> {
        let value = match self.prefix.as_deref().filter(|p| !p.is_empty()) {
            Some(prefix) => format!("{prefix} {}", self.secret),
            None => self.secret.clone(),
        };
        let mut value = HeaderValue::from_str(&value).map_err(|_| Error)?;
        value.set_sensitive(true);
        Ok(value)
    }
    pub fn apply(&self, url: &mut url::Url, headers: &mut HeaderMap) -> Result<(), Error> {
        self.validate()?;
        match self.location {
            Location::Header => {
                headers.insert(
                    HeaderName::from_bytes(self.name.as_bytes()).map_err(|_| Error)?,
                    self.header_value()?,
                );
            }
            Location::Query => {
                let pairs: Vec<_> = url
                    .query_pairs()
                    .filter(|(k, _)| k != &self.name)
                    .map(|(k, v)| (k.into_owned(), v.into_owned()))
                    .collect();
                url.set_query(None);
                url.query_pairs_mut()
                    .extend_pairs(pairs)
                    .append_pair(&self.name, &self.secret);
            }
        }
        Ok(())
    }
}
#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn replaces_query_and_inserts_one_space_without_double_encoding() {
        let mut auth = KeyAuth {
            kind: Kind::KeyAuth,
            location: Location::Query,
            name: "key".into(),
            prefix: None,
            secret: "a+b /&".into(),
        };
        let mut url = url::Url::parse("https://example.test/?key=old&x=1&key=old2").unwrap();
        let mut headers = HeaderMap::new();
        auth.apply(&mut url, &mut headers).unwrap();
        assert_eq!(
            url.query_pairs()
                .filter(|(k, _)| k == "key")
                .collect::<Vec<_>>(),
            vec![("key".into(), "a+b /&".into())]
        );
        auth.location = Location::Header;
        auth.name = "authorization".into();
        auth.prefix = Some("Bearer".into());
        auth.secret = "secret".into();
        auth.apply(&mut url, &mut headers).unwrap();
        assert_eq!(headers["authorization"], "Bearer secret");
        assert!(headers["authorization"].is_sensitive());
    }
}
