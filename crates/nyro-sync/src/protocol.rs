use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};

pub const MAX_CONFIG_BYTES: usize = 1_048_576;

#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Version {
    pub epoch: String,
    pub sequence: u64,
}

/// Payloads may contain credentials. Deliberately does not implement Debug.
#[derive(Clone, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Snapshot<T> {
    pub version: Version,
    pub fingerprint: String,
    pub config: T,
}

pub fn fingerprint<T: Serialize>(config: &T) -> Result<String, Error> {
    let mut value = serde_json::to_value(config).map_err(|_| Error::Serialization)?;
    canonicalize(&mut value);
    let bytes = serde_json::to_vec(&value).map_err(|_| Error::Serialization)?;
    if bytes.len() > MAX_CONFIG_BYTES {
        return Err(Error::TooLarge);
    }
    Ok(format!("{:x}", Sha256::digest(bytes)))
}

fn canonicalize(value: &mut serde_json::Value) {
    match value {
        serde_json::Value::Object(map) => {
            let mut fields: Vec<_> = std::mem::take(map).into_iter().collect();
            fields.sort_by(|a, b| a.0.cmp(&b.0));
            for (key, mut value) in fields {
                canonicalize(&mut value);
                map.insert(key, value);
            }
        }
        serde_json::Value::Array(values) => values.iter_mut().for_each(canonicalize),
        _ => {}
    }
}

#[derive(Clone, Debug, Serialize, Deserialize)]
#[serde(tag = "status", rename_all = "kebab-case", deny_unknown_fields)]
pub enum ApplicationResult {
    Applied { version: Version },
    Rejected { version: Version, code: String },
}

impl ApplicationResult {
    pub fn version(&self) -> &Version {
        match self {
            Self::Applied { version } | Self::Rejected { version, .. } => version,
        }
    }
}

#[derive(Clone, Debug, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Poll {
    pub node_id: String,
    pub received: Option<Version>,
    pub result: Option<ApplicationResult>,
}

impl Poll {
    pub fn new(node_id: impl Into<String>) -> Self {
        Self {
            node_id: node_id.into(),
            received: None,
            result: None,
        }
    }
}

#[derive(Clone, Debug, Default, Serialize)]
pub struct NodeStatus {
    pub sent: Option<Version>,
    pub applied: Option<Version>,
    pub rejected: Option<Version>,
    pub error_code: Option<String>,
}

#[derive(Debug, thiserror::Error)]
pub enum Error {
    #[error("configuration serialization failed")]
    Serialization,
    #[error("configuration exceeds size limit")]
    TooLarge,
    #[error("invalid configuration synchronization request")]
    InvalidRequest,
    #[error("configuration synchronization capacity exceeded")]
    Capacity,
    #[error("configuration synchronization unavailable")]
    Unavailable,
}

#[cfg(test)]
mod tests {
    use super::*;
    struct Ordered(Vec<(&'static str, u32)>);
    impl Serialize for Ordered {
        fn serialize<S: serde::Serializer>(&self, serializer: S) -> Result<S::Ok, S::Error> {
            use serde::ser::SerializeMap;
            let mut map = serializer.serialize_map(Some(self.0.len()))?;
            for (key, value) in &self.0 {
                map.serialize_entry(key, value)?;
            }
            map.end()
        }
    }
    #[test]
    fn object_order_is_not_configuration_identity() {
        let left = Ordered(vec![("a", 1), ("b", 2)]);
        let right = Ordered(vec![("b", 2), ("a", 1)]);
        assert_eq!(fingerprint(&left).unwrap(), fingerprint(&right).unwrap());
    }
}
