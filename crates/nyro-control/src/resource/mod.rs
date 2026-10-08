//! Resource CRUD. Public IDs are editable; internal identities survive renames.
mod database;
mod postgres;
use crate::Error;
pub use database::{POSTGRES_SCHEMA, Store};
use nyro_config::{compile::Snapshot, resources::Resources};
use serde::{Deserialize, Serialize};
use serde_json::Value;

#[derive(Clone, Copy, Debug, Eq, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum Kind {
    Upstreams,
    Models,
    Mcps,
    Consumers,
}
impl Kind {
    pub fn parse(name: &str) -> Result<Self, Error> {
        match name {
            "upstreams" => Ok(Self::Upstreams),
            "models" => Ok(Self::Models),
            "mcps" => Ok(Self::Mcps),
            "consumers" => Ok(Self::Consumers),
            _ => Err(Error::NotFound),
        }
    }
    pub fn name(self) -> &'static str {
        match self {
            Self::Upstreams => "upstreams",
            Self::Models => "models",
            Self::Mcps => "mcps",
            Self::Consumers => "consumers",
        }
    }
    pub(super) fn identities(
        self,
        snapshot: &mut Snapshot,
    ) -> &mut std::collections::BTreeMap<String, String> {
        match self {
            Self::Upstreams => &mut snapshot.identities.upstreams,
            Self::Models => &mut snapshot.identities.models,
            Self::Mcps => &mut snapshot.identities.mcps,
            Self::Consumers => &mut snapshot.identities.consumers,
        }
    }
}
/// PUT replaces a resource. Omitted secret keeps the matching old credential; a supplied secret rotates it.
/// Deleting a referenced upstream fails; renaming rewrites references atomically.
pub fn edit(
    current: &Snapshot,
    kind: Kind,
    id: Option<&str>,
    mut value: Option<Value>,
) -> Result<Snapshot, Error> {
    let mut snapshot = current.clone();
    let mut resources = serde_json::to_value(&snapshot.resources).map_err(|_| Error::Invalid)?;
    let items = resources[kind.name()]
        .as_array_mut()
        .ok_or(Error::Invalid)?;
    let existing = id
        .map(|id| {
            items
                .iter()
                .position(|item| item["id"] == id)
                .ok_or(Error::NotFound)
        })
        .transpose()?;
    if let (Some(index), Some(value)) = (existing, value.as_mut()) {
        restore_secrets(kind, &items[index], value)?;
    }
    let new_id = value
        .as_ref()
        .map(|value| {
            value["id"]
                .as_str()
                .filter(|id| !id.is_empty())
                .map(str::to_owned)
                .ok_or(Error::Invalid)
        })
        .transpose()?;
    if let Some(new_id) = &new_id
        && items
            .iter()
            .enumerate()
            .any(|(i, v)| Some(i) != existing && v["id"] == *new_id)
    {
        return Err(Error::AlreadyExists);
    }
    match (existing, value) {
        (Some(index), Some(value)) => {
            items[index] = value;
        }
        (Some(index), None) => {
            items.remove(index);
        }
        (None, Some(value)) => items.push(value),
        (None, None) => return Err(Error::NotFound),
    }
    let identities = kind.identities(&mut snapshot);
    let uid = match id {
        Some(id) => identities.remove(id).ok_or(Error::Schema)?,
        None => uuid::Uuid::new_v4().to_string(),
    };
    if let Some(new_id) = &new_id {
        identities.insert(new_id.clone(), uid);
    }
    if let Some(old_id) = id {
        match kind {
            Kind::Upstreams => {
                for key in ["models", "mcps"] {
                    for resource in resources[key].as_array_mut().ok_or(Error::Invalid)? {
                        if resource["upstream"] == old_id {
                            resource["upstream"] =
                                Value::String(new_id.clone().ok_or(Error::Referenced)?);
                        }
                    }
                }
            }
            Kind::Models | Kind::Mcps => {
                for consumer in resources["consumers"]
                    .as_array_mut()
                    .ok_or(Error::Invalid)?
                {
                    if let Some(grants) = consumer["grants"][kind.name()].as_array_mut() {
                        if let Some(new_id) = &new_id {
                            for grant in grants {
                                if grant == old_id {
                                    *grant = Value::String(new_id.clone());
                                }
                            }
                        } else {
                            grants.retain(|v| v != old_id);
                        }
                    }
                }
            }
            Kind::Consumers => {}
        }
    }
    snapshot.resources = serde_json::from_value(resources).map_err(|_| Error::Invalid)?;
    validate(&snapshot)?;
    Ok(snapshot)
}
fn restore_secrets(kind: Kind, old: &Value, new: &mut Value) -> Result<(), Error> {
    let key = match kind {
        Kind::Upstreams => "targets",
        Kind::Consumers => "credentials",
        _ => return Ok(()),
    };
    if let Some(items) = new.get_mut(key).and_then(Value::as_array_mut) {
        for item in items {
            if !item.is_object() {
                return Err(Error::Invalid);
            }
            let previous = old[key]
                .as_array()
                .and_then(|items| items.iter().find(|p| p["id"] == item["id"]));
            if kind == Kind::Upstreams
                && let Some(egress) = item.get_mut("egress").and_then(Value::as_object_mut)
            {
                if let Some(flag) = egress.remove("has_proxy_url")
                    && !flag.is_boolean()
                {
                    return Err(Error::Invalid);
                }
                match egress.get("proxy_url") {
                    Some(Value::Null) => {
                        egress.remove("proxy_url");
                    }
                    None => {
                        if let Some(url) = previous
                            .and_then(|p| p.get("egress"))
                            .and_then(|p| p.get("proxy_url"))
                        {
                            egress.insert("proxy_url".into(), url.clone());
                        }
                    }
                    _ => {}
                }
            }
            let (destination, source) = if kind == Kind::Upstreams {
                (&mut item["auth"], previous.map(|p| &p["auth"]))
            } else {
                (item, previous)
            };
            if let Some(object) = destination.as_object_mut() {
                if let Some(flag) = object.remove("has_secret")
                    && !flag.is_boolean()
                {
                    return Err(Error::Invalid);
                }
                if !object.contains_key("secret")
                    && let Some(secret) = source.and_then(|s| s.get("secret"))
                {
                    object.insert("secret".into(), secret.clone());
                }
            }
        }
    }
    Ok(())
}
pub fn redacted(resources: &Resources) -> Value {
    let mut value = serde_json::to_value(resources).expect("resources serialize");
    for key in ["upstreams", "consumers"] {
        for resource in value[key].as_array_mut().unwrap() {
            let items = resource[if key == "upstreams" {
                "targets"
            } else {
                "credentials"
            }]
            .as_array_mut()
            .unwrap();
            for item in items {
                if key == "upstreams"
                    && let Some(egress) = item.get_mut("egress").and_then(Value::as_object_mut)
                    && egress.remove("proxy_url").is_some()
                {
                    egress.insert("has_proxy_url".into(), Value::Bool(true));
                }
                let auth = if key == "upstreams" {
                    &mut item["auth"]
                } else {
                    item
                };
                if let Some(auth) = auth.as_object_mut()
                    && auth.remove("secret").is_some()
                {
                    auth.insert("has_secret".into(), Value::Bool(true));
                }
            }
        }
    }
    value
}
pub(super) fn validate(snapshot: &Snapshot) -> Result<(), Error> {
    snapshot
        .compile(&Default::default(), Default::default())
        .map_err(|_| Error::Invalid)?;
    if serde_json::to_vec(snapshot)
        .map_err(|_| Error::Invalid)?
        .len()
        > crate::MAX_CONFIG_BYTES
    {
        return Err(Error::Invalid);
    }
    Ok(())
}
