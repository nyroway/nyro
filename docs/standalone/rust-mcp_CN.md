# 实验性 Rust MCP 工具网关

[English](rust-mcp.md)

从源码构建的根 `nyro` 二进制可以在同一个监听器上处理 LLM 和 MCP 请求。复制 [rust-mcp.yaml](rust-mcp.yaml)，替换示例地址和凭据后运行：

```sh
cargo run -p nyro -- proxy --config docs/standalone/rust-mcp.yaml
```

当前仍要求 `llm` 区块。省略 `mcp` 即不启用 MCP，原有 LLM 配置行为不变。`nyro serve` 使用同一份完整配置初始化；后续通过[草稿保存与发布 API](rust-serve_CN.md) 更新。

## 协议范围

上下游均使用 MCP **2026-07-28**，官方 Rust SDK 固定为 `rmcp 3.4.1`。每个服务有独立的 `/mcp/{server_id}` 端点，支持 `server/discover`、`tools/list`、`tools/call`；发现只声明工具能力。支持 JSON 和请求级 SSE，以及进度通知。工具 schema、描述、注解、结构化结果和 `isError` 保持 MCP 语义，不经过 LLM IR。

首版不支持旧版 `initialize` 会话、GET 事件流、DELETE、流恢复、stdio、OAuth、Resources、Prompts、订阅、后台任务、MRTR、LLM 自动执行工具或聚合工具目录。不支持的续传结果明确失败。客户端必须使用 POST，提供 Host 或 HTTP/2 authority、同时接受 JSON/SSE，并携带一致的协议元数据。包含 `Origin` 的请求返回 403，当前不开放浏览器访问。

列出 `client-a` 可见的工具：

```sh
curl http://127.0.0.1:19530/mcp/knowledge \
  -H 'Authorization: Bearer replace-with-client-secret' \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -H 'MCP-Protocol-Version: 2026-07-28' \
  -H 'Mcp-Method: tools/list' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"example-client","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}}}'
```

调用工具时保留上述元数据，将方法改为 `tools/call`，增加与 `params.name` 一致的 `Mcp-Name`，并提供 `arguments`。带 `x-mcp-header` schema 注解的工具需要按协议携带对应的参数 Header；建议使用支持该版本的 SDK。

## 配置与权限

出现 `mcp` 时，`servers` 必须非空。服务 ID 由 1–64 个 ASCII 字母、数字、`-`、`_` 组成。`transport: http` 必填。`url` 是明确的 HTTP(S) 端点，不得包含用户信息、query、fragment 或空白，Nyro 不追加路径；允许管理员配置本地或内网端点。未知字段、显式 null 对象均拒绝。

每个服务必须声明非空、无重复、无通配符的 `subjects` 和 `allowed_tools`。主体引用 `security.api_keys[].id`；禁用或过期 Key 仍可作为配置引用，但不能认证。所有操作要求唯一 Bearer Authorization Header 和服务访问权限，不接受 query 凭据。列表过滤和直接工具调用都执行精确名称白名单。

一次列表请求只取上游一页，过滤后保留 `nextCursor`，即使这一页为空。调用前先发现上游并按页查找工具 schema，使 SDK 能构造协议要求的参数 Header。这些只读操作与调用共用期限和响应字节预算，重复 cursor 会失败；工具本身最多执行一次。

可选的上游 `bearer_token` 与入站 Key 分离。Nyro 不透传客户端 Authorization，不接受客户端覆盖目标地址，不继承系统代理，也不跟随重定向。普通管理查询仅返回 `has_bearer_token`，受保护的完整导出保留凭据供重新导入。被 LLM 或 MCP 引用的 Key 不能删除。

## 限制、重载与失败语义

示例中的 MCP 限制值也是默认值，独立于 LLM 的 `server` 请求限制。期限覆盖上传、发现、schema 查询、工具执行和响应交付；响应预算累计限制发现及操作的原始上游字节，SSE 单帧限制在解析前生效。MCP 与 LLM 共用 `limit.concurrency`，MCP 不消耗 LLM RPM/TPM/token quota。

不自动重试工具、不降级协议、不恢复旧会话、不做故障转移。请求关闭或超时会停止本地等待并关闭关联流，但上游可能已经产生副作用，取消不能保证撤销。

候选构建只进行本地校验，不连接上游；MCP 上游不可用不会阻止 LLM 启动。Unix SIGHUP 和控制面发布把两个应用作为同一代际替换。新请求使用新授权快照，在途请求保留原配置直到完成或期限到达。无效候选保留当前代际。`/readyz` 表示网关接受请求，不表示上游可达。

Nyro 记录 MCP 请求状态和耗时，不记录工具参数或结果。根二进制即使收到启用 `rmcp` 的 `RUST_LOG`，也会关闭 SDK 的报文日志；其他项目引用 `nyro-mcp` 时，应在自己的 tracing 配置中通过独立目标过滤器排除 `rmcp` 事件，不能只依赖可被更具体配置覆盖的 EnvFilter 指令。

协议参考：[MCP Streamable HTTP 2026-07-28](https://modelcontextprotocol.io/specification/2026-07-28/basic/transports/streamable-http)。本地验收：`cargo test -p nyro-mcp`，随后运行 `cargo build -p nyro && python3 tests/mcp_gateway_smoke.py`。
