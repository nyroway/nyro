use crate::{
    config::{Config, Server},
    upstream::{self, Operation},
};
use rmcp::{RoleServer, ServerHandler, model::*, service::RequestContext};
use std::{borrow::Cow, sync::Arc};
use tokio::time::Instant;

#[derive(Clone)]
pub(crate) struct Handler {
    pub http: reqwest::Client,
    pub config: Arc<Config>,
    pub server: Server,
    pub deadline: Instant,
}
impl ServerHandler for Handler {
    fn supported_protocol_versions(&self) -> Cow<'static, [ProtocolVersion]> {
        Cow::Owned(vec![ProtocolVersion::V_2026_07_28])
    }
    fn get_info(&self) -> ServerConfig {
        ServerConfig::new(ServerCapabilities::builder().enable_tools().build())
    }
    async fn list_tools(
        &self,
        params: Option<PaginatedRequestParams>,
        ctx: RequestContext<RoleServer>,
    ) -> Result<ListToolsResult, ErrorData> {
        match upstream::run(
            self.http.clone(),
            self.config.clone(),
            self.server.clone(),
            Operation::List(params),
            ctx.peer,
            ctx.ct,
            self.deadline,
        )
        .await?
        {
            ServerResult::ListToolsResult(result) => Ok(result),
            _ => unreachable!(),
        }
    }
    async fn call_tool(
        &self,
        mut params: CallToolRequestParams,
        ctx: RequestContext<RoleServer>,
    ) -> Result<CallToolResponse, ErrorData> {
        let headers = ctx
            .extensions
            .get::<axum::http::request::Parts>()
            .map(|parts| parts.headers.clone())
            .unwrap_or_default();
        params.meta = Some(ctx.meta);
        match upstream::run(
            self.http.clone(),
            self.config.clone(),
            self.server.clone(),
            Operation::Call(params, headers),
            ctx.peer,
            ctx.ct,
            self.deadline,
        )
        .await?
        {
            ServerResult::CallToolResult(result) => Ok(CallToolResponse::Complete(result)),
            _ => unreachable!(),
        }
    }
}
