//! Validate schema-derived headers after the authorized upstream schema lookup.
//! rmcp's synchronous get_tool hook cannot perform that lookup, and its header
//! validator is crate-private. Never replace conflicting input with a repaired
//! upstream header: front proxies may already have authorized its original value.
use axum::http::HeaderMap;
use base64::{Engine, engine::general_purpose::STANDARD};
use rmcp::model::{ErrorData, JsonObject};
use serde_json::Value;

fn mismatch() -> ErrorData {
    ErrorData::header_mismatch("MCP parameter headers do not match tool arguments", None)
}
fn decode(raw: &str) -> Result<String, ErrorData> {
    if let Some(encoded) = raw
        .strip_prefix("=?base64?")
        .and_then(|s| s.strip_suffix("?="))
    {
        String::from_utf8(STANDARD.decode(encoded).map_err(|_| mismatch())?).map_err(|_| mismatch())
    } else {
        Ok(raw.to_owned())
    }
}
pub(crate) fn validate_params(
    headers: &HeaderMap,
    arguments: Option<&JsonObject>,
    schema: &JsonObject,
) -> Result<(), ErrorData> {
    for name in headers
        .keys()
        .filter(|name| name.as_str().starts_with("mcp-param-"))
    {
        if headers.get_all(name).iter().count() != 1 {
            return Err(mismatch());
        }
    }
    let Some(properties) = schema.get("properties").and_then(Value::as_object) else {
        return Ok(());
    };
    // The pinned SDK excludes tool definitions with nested/invalid annotations.
    // Its returned tools therefore contain only valid top-level annotations.
    for (property, definition) in properties {
        let Some(header) = definition.get("x-mcp-header").and_then(Value::as_str) else {
            continue;
        };
        let name = format!("mcp-param-{header}");
        let expected = match arguments.and_then(|args| args.get(property)) {
            None | Some(Value::Null) => None,
            Some(Value::String(s)) => Some(s.clone()),
            Some(value @ (Value::Bool(_) | Value::Number(_))) => Some(value.to_string()),
            Some(_) => {
                return Err(ErrorData::invalid_params(
                    "Header-annotated arguments must be primitive values",
                    None,
                ));
            }
        };
        let actual = headers
            .get(&name)
            .map(|v| v.to_str().map_err(|_| mismatch()).and_then(decode))
            .transpose()?;
        if actual != expected {
            return Err(mismatch());
        }
    }
    Ok(())
}
