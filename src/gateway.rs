//! Concrete application composition. Kernel generations remain workload-neutral.
use axum::{
    body::Body,
    http::{Request, Response, StatusCode},
};
use std::time::Duration;
use tokio_util::sync::CancellationToken;

pub(crate) struct GatewayRuntime {
    pub(crate) llm: nyro_llm::runtime::Runtime,
    pub(crate) mcp: Option<nyro_mcp::Runtime>,
}
pub(crate) fn is_mcp_path(path: &str) -> bool {
    path == "/mcp" || path.starts_with("/mcp/")
}
impl GatewayRuntime {
    pub(crate) fn request_timeout(&self, path: &str) -> Duration {
        if is_mcp_path(path) {
            self.mcp
                .as_ref()
                .map_or(Duration::from_secs(5), nyro_mcp::Runtime::request_timeout)
        } else {
            self.llm.request_timeout()
        }
    }
    pub(crate) async fn handle(
        &self,
        request: Request<Body>,
        cancel: CancellationToken,
    ) -> Response<Body> {
        if is_mcp_path(request.uri().path()) {
            match &self.mcp {
                Some(mcp)=>mcp.handle(request,cancel).await,
                None=>Response::builder().status(StatusCode::NOT_FOUND).header("content-type","application/json")
                    .body(Body::from(r#"{"jsonrpc":"2.0","error":{"code":-32601,"message":"MCP is not configured"}}"#)).unwrap(),
            }
        } else {
            self.llm.handle(request, cancel).await
        }
    }
}
