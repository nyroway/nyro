//! `SchemaObject` — unified tool-parameter JSON Schema representation.
//!
//! Stores the canonical lowercase-type form of a JSON Schema object.
//! The Google GenAI encoder is responsible for uppercasing `type` values on
//! the way out (`"string"` → `"STRING"`); all other encoders use lowercase as-is.

use serde::{Deserialize, Serialize};
use serde_json::Value;

/// A JSON Schema object for tool parameters, always stored with lowercase
/// `type` names (JSON Schema canonical form).
///
/// Use `to_google_wire()` to get an uppercase-type copy for the Gemini wire format.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct SchemaObject(pub Value);

impl SchemaObject {
    /// Create a new `SchemaObject`, normalizing any `type` values to lowercase.
    pub fn new(value: Value) -> Self {
        let mut obj = Self(value);
        obj.normalize_to_lowercase();
        obj
    }

    /// Recursively normalize all `type` string values to lowercase in-place.
    pub fn normalize_to_lowercase(&mut self) {
        normalize_type_strings(&mut self.0, |s| s.to_lowercase());
    }

    /// Return a clone with all `type` string values uppercased (Gemini wire format).
    pub fn to_google_wire(&self) -> Value {
        let mut v = self.0.clone();
        normalize_type_strings(&mut v, |s| s.to_uppercase());
        v
    }

    pub fn as_value(&self) -> &Value {
        &self.0
    }

    pub fn into_value(self) -> Value {
        self.0
    }
}

impl From<Value> for SchemaObject {
    fn from(v: Value) -> Self {
        Self::new(v)
    }
}

fn normalize_type_strings(v: &mut Value, f: impl Fn(&str) -> String + Copy) {
    match v {
        Value::Object(map) => {
            if let Some(Value::String(s)) = map.get_mut("type") {
                *s = f(s);
            }
            for val in map.values_mut() {
                normalize_type_strings(val, f);
            }
        }
        Value::Array(arr) => {
            for val in arr {
                normalize_type_strings(val, f);
            }
        }
        _ => {}
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn normalizes_google_uppercase_to_lowercase() {
        let schema = json!({
            "type": "OBJECT",
            "properties": {
                "name": { "type": "STRING" },
                "age":  { "type": "INTEGER" }
            }
        });
        let obj = SchemaObject::new(schema);
        assert_eq!(obj.as_value()["type"], "object");
        assert_eq!(obj.as_value()["properties"]["name"]["type"], "string");
        assert_eq!(obj.as_value()["properties"]["age"]["type"], "integer");
    }

    #[test]
    fn to_google_wire_uppercases() {
        let schema = json!({"type": "object", "properties": {"x": {"type": "string"}}});
        let obj = SchemaObject::new(schema);
        let wire = obj.to_google_wire();
        assert_eq!(wire["type"], "OBJECT");
        assert_eq!(wire["properties"]["x"]["type"], "STRING");
    }
}
