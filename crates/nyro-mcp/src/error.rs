use axum::{
    body::Body,
    http::{Response, StatusCode},
};
use rmcp::model::ErrorData;
use serde_json::{Value, json};

pub(crate) fn response(status: StatusCode, id: Option<Value>, error: ErrorData) -> Response<Body> {
    let mut value = json!({"jsonrpc":"2.0","error":error});
    if let Some(id) = id {
        value["id"] = id;
    }
    let mut response = Response::builder()
        .status(status)
        .header("content-type", "application/json")
        .body(Body::from(value.to_string()))
        .unwrap();
    if status == StatusCode::UNAUTHORIZED {
        response
            .headers_mut()
            .insert("www-authenticate", "Bearer".parse().unwrap());
    }
    response
}
pub(crate) fn reject(status: StatusCode, message: &'static str) -> Response<Body> {
    response(status, None, ErrorData::invalid_request(message, None))
}
pub(crate) fn upstream() -> ErrorData {
    ErrorData::internal_error("MCP upstream request failed", None)
}

pub(crate) fn service(error: rmcp::service::ServiceError) -> ErrorData {
    match error {
        rmcp::service::ServiceError::McpError(error) => {
            ErrorData::new(error.code, "Upstream MCP protocol error", None)
        }
        _ => upstream(),
    }
}
