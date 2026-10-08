//! Concrete application composition. Kernel generations remain workload-neutral.
use axum::{
    body::Body,
    http::{Request, Response},
};
use std::time::Duration;
use tokio_util::sync::CancellationToken;

pub(crate) struct GatewayRuntime {
    pub(crate) llm: nyro_llm::Runtime,
    pub(crate) mcp: nyro_mcp::Runtime,
}
pub(crate) fn is_mcp_path(path: &str) -> bool {
    path == "/mcp" || path.starts_with("/mcp/")
}
impl GatewayRuntime {
    pub(crate) fn request_timeout(&self, path: &str) -> Duration {
        if is_mcp_path(path) {
            self.mcp.request_timeout()
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
            self.mcp.handle(request, cancel).await
        } else {
            self.llm.handle(request, cancel).await
        }
    }
}
