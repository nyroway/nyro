# Rust MCP 网关

[English](rust-mcp.md)

LLM 和 MCP 由同一个根 `nyro` host 组装。可通过 `nyro proxy --config` 加载 [MCP YAML](rust-mcp.yaml)，也可通过[控制 API](rust-serve_CN.md) 保存相同资源对象。

`id: tools` 的 MCP 资源固定暴露 **`/mcp/tools`**。ID 长度为 1–64，仅支持 ASCII 字母、数字、中划线和下划线，不单独配置入口路径。`upstream` 引用 `kind: mcp` 上游池；target 的 `url` 为完整 MCP 端点，`transport` 为 `streamable-http`。池支持 `weighted-roundrobin` 和 `weighted-random`，引用同一个池的 MCP 资源共享选择状态。

Consumer、`key-auth`、访问模式和请求滚动窗口遵循[公共资源约定](rust-proxy_CN.md)。每个 MCP 必须配置非空、精确的 `allowed_tools`，不接受通配符。Consumer 的授权引用 MCP ID；资源限制和 Consumer 的 `limits.mcp` 同时生效。MCP 没有 token 限制。

当前使用已锁定 SDK 的无状态 MCP 协议版本 `2026-07-28`，支持 `server/discover`、`tools/list`、`tools/call`，HTTP 头和 JSON-RPC 元数据须符合该 SDK 契约。不代理旧会话：拒绝会话 ID、续传 ID、浏览器 Origin、tasks 和 continuation 响应，不合并多个 target 的工具目录。

一次操作固定选择一个 target，出站认证与调用方凭证隔离。工具调用不会自动重试或故障转移；连接或状态错误不足以证明副作用操作可以重放。响应和 SSE 帧有大小限制，取消会关闭上游请求，代际租约持续到响应交付结束。

默认超时为 30 秒，body、response、frame 上限分别为 1 MiB、16 MiB、1 MiB。可在资源 `execution` 下覆盖，`request_timeout` 使用数值秒；MCP 不接受 `max_attempts`。
