# Rust 数据面

[English](rust-proxy.md)

使用 `cargo build -p nyro` 构建根二进制。这个源码入口与已发布的桌面应用及 `nyro-server` 独立。

```sh
export UPSTREAM_MODEL=your-backend-model
export UPSTREAM_TOKEN=your-upstream-secret
export NYRO_CLIENT_TOKEN=your-client-secret
cargo run -p nyro -- proxy --config docs/standalone/rust-proxy.yaml
```

[配置示例](rust-proxy.yaml) 顶层包含 `version: 1` 和 `upstreams`、`models`、`mcps`、`consumers` 四类资源；省略的集合视为空。启动设置使用 CLI 或环境变量：`--listen` / `NYRO_LISTEN` 默认 `127.0.0.1:19530`；`--concurrency` / `NYRO_CONCURRENCY` 默认 64；`--config` / `NYRO_CONFIG` 指定资源文件。文件仅在启动时读取一次，无文件监听或 SIGHUP 重载；修改文件后需重启。

所有标量值均可使用 `${VARIABLE}`。缺失变量报错；`$${VARIABLE}` 表示字面量。替换发生在解析后的标量内，不会注入 YAML 对象或列表，也不会递归展开变量。数字设置接受环境变量中的数字字符串。配置和同步载荷最大为 1 MiB。

## 资源

模型 `id` 是客户端使用的模型名称；`name` 是可选显示名称。`capability` 为 `chat` 或 `embedding`，`upstream` 引用 LLM 上游池。每个 target 定义实际后端 `model`、`base_url`、`protocol`、可选 `auth`、`weight`（默认 1）及 `priority`（默认 0，越小越优先）。LLM 的 `base_url` 是 API 基础地址，由适配器追加接口路径。协议标识为：

- `openai/chat-completions`
- `openai/responses`
- `openai/embeddings`
- `anthropic/messages`
- `gemini/generate-content`

同协议 Chat 流量保留支持的原生字段；跨协议流量经过类型化转换，无法保真表示时拒绝请求。具体边界由协议回归测试覆盖。本轮不增加 Gemini Interactions、图像／音频／视频生成或 OAuth。

`balance` 默认 `weighted-roundrobin`，实现平滑加权轮询；LLM 还支持 `weighted-random`、`least-recent` 和 `latency-aware`。引用同一池的模型共享选择历史。权重为零的 target 不参与选择。LLM 的 `execution.max_attempts` 包含首次请求，是否可以重试仍由协议运行时决定。

MCP 复用上游和 Consumer 概念，见 [MCP 文档](rust-mcp_CN.md)。

## 认证与授权

`access.mode` 默认 `restricted`：

| 模式 | 行为 |
|---|---|
| `anonymous` | 忽略所有入站凭证，包括错误凭证；不计入 Consumer 限制。 |
| `authenticated` | 必须提供有效 Consumer 凭证。 |
| `restricted` | 必须提供有效凭证，且 Consumer 获得资源授权。 |

Consumer 可配置多个 `{id, type: key-auth, secret}`，密钥轮换共享身份和限额。`grants.models` / `grants.mcps` 引用公开资源 ID；空列表不授予任何受限资源权限。没有 `enabled` 字段，也不接受 `api-key` / `apikey` 认证类型别名。

出站认证独立配置：

```yaml
auth:
  type: key-auth
  in: header
  name: Authorization
  prefix: Bearer
  secret: ${UPSTREAM_TOKEN}
```

`prefix` 可省略；非空时程序在前缀与密钥之间加一个空格。查询参数认证使用 `in: query`、参数 `name` 和 `secret`，不配置 `prefix`。适配器负责编码，并替换同名旧参数。客户端凭证及任意入站头不会透传给上游。

## 执行与限制

`execution.request_timeout` 使用正数秒，支持 `1.5` 等小数；LLM 默认 120 秒，MCP 默认 30 秒。`max_body_bytes`、`max_response_bytes`、`max_frame_bytes` 可选，默认分别为 1 MiB、16 MiB、1 MiB。 模型从解析后的请求中确定；此前读取上传正文使用应用级大小与超时上限。确定模型后，资源期限仍从请求到达时计算，并在分发前检查资源的正文大小限制。

`limits.request` 和 LLM 的 `limits.token` 均为 `{limit, window}` 列表；窗口支持 `ms`、`s`、`m`、`h`、`d`。资源限制与 Consumer 的 `limits.llm` / `limits.mcp` 同时生效。MCP 仅支持请求次数限制。

每个已准入的逻辑请求计数一次；重试不重复计请求数，后续失败不退还计数。每次上游尝试仅累计已知实际 token，不预占、不估算。重复的累计 usage 只结算一次；缺失 usage 记零；中断流仅结算最后一次有效已知用量，并标记用量不完整。在途请求可能导致超限。

计数仅保存在单节点内存，不跨副本共享、不跨进程重启持久化。控制面改名或轮换密钥保留身份和历史；同窗口调阈值保留历史，新窗口从生效后开始统计。本轮不支持金额 quota、资源级 rate 桶或 concurrency 策略；保留进程级并发保护。

## 远程配置

```sh
nyro proxy --server https://control.example.com \
  --sync-token-file /run/secrets/nyro-sync --node-id edge-1
```

`--config` 与 `--server` 二选一；远程数据面不连接数据库。对应环境变量为 `NYRO_SERVER`、`NYRO_SYNC_TOKEN_FILE`、`NYRO_NODE_ID`。同步使用带认证的 HTTP 长轮询和完整快照。远程必须使用 HTTPS；仅 IP 回环地址允许 HTTP 测试。

`/healthz` 检查进程；首次有效快照激活之前 `/readyz` 不就绪。合法空配置可就绪，但没有业务路由。断连或更新失败时继续使用上一个内存代际，没有磁盘离线缓存。部署方式见 [控制面文档](rust-serve_CN.md)。
