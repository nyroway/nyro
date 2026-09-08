# 实验性 Rust 文件配置代理

[English](rust-proxy.md)

根 `nyro` 二进制已包含一个实验性的 LLM 数据面，目前需要从源码构建。它与 [README](README.md) 介绍的已发布 `nyro-server` Standalone 模式相互独立，YAML 格式也不兼容。

## 从源码运行

复制 [rust-proxy.yaml](rust-proxy.yaml)，替换示例中的 Provider 地址、模型名和密钥，然后运行：

```sh
cargo run -p nyro -- proxy --config docs/standalone/rust-proxy.yaml
```

程序在绑定监听端口前读取并校验一次文件。默认地址是 `127.0.0.1:19530`。

使用 `security.api_keys` 中配置的客户端凭据请求受保护的 Chat Completions 模型：

```sh
curl http://127.0.0.1:19530/v1/chat/completions \
  -H 'Authorization: Bearer replace-with-client-secret' \
  -H 'Content-Type: application/json' \
  -d '{"model":"chat-default","messages":[{"role":"user","content":"Hello"}]}'
```

当前入口实现 `POST /v1/chat/completions`（包括 `stream: true` 时的 OpenAI 兼容 SSE）和 `POST /v1/embeddings` 的强类型子集，不承诺完整兼容各厂商 API。未知或尚未支持的请求字段会返回 `400`，不会原样转发；未知或尚未支持的上游响应字段会在流开始前返回 `502`，如果出现在首帧校验后的 SSE 中则终止该流。配置中的模型名是公开别名：Nyro 向上游发送 `upstream_model`，并在已支持的响应中恢复公开模型名。

## 配置

所有配置结构都会拒绝未知字段。`kind` 必填，目前只接受 `openai`。Provider URL 必须使用 HTTP 或 HTTPS、包含主机，且不能包含用户信息、查询参数或 fragment。Nyro 在配置的基础路径后追加 `chat/completions` 或 `embeddings`。上游请求只使用 Provider 可选的 `api_key`，不会转发调用方的 Authorization 请求头；同时禁用重定向和环境变量配置的 HTTP 代理。

每个模型包含：

- `provider`：`llm.providers` 中的 ID。
- `upstream_model`：发送给上游的模型名。
- `workloads`：非空且不能重复，可包含 `chat`、`embedding` 或两者。
- `allow_anonymous`：可选，默认 `false`。
- `subjects`：允许调用受保护模型的客户端凭据 ID；只有匿名模型可以省略。

受保护模型的 `subjects` 不能为空，每一项都必须匹配 `security.api_keys` 中的 `id`。未提供 Bearer 凭据或凭据未知时返回 `401`；凭据已知但无权访问该模型时返回 `403`。匿名模型可以不带凭据访问；如果请求带了凭据，该凭据仍须有效，但任意已知凭据都可以访问匿名模型。

凭据 ID 和 secret 必须各自唯一且非空，ID 不能只含空白。客户端 secret 只能包含无空白的可见 ASCII 字符，才能作为 HTTP Bearer 凭据提交。配置错误和 Debug 输出不会暴露 secret。

| 配置项 | 默认值 | 作用 |
|---|---:|---|
| `server.listen` | `127.0.0.1:19530` | 监听地址 |
| `server.request_timeout_ms` | `120000` | 整个请求的期限，包括成功响应体或 SSE 流 |
| `server.max_body_bytes` | `1048576` | 缓冲的下游请求体上限 |
| `server.max_response_bytes` | `16777216` | 缓冲的非流式上游响应上限；不是 SSE 累计流量上限 |
| `server.max_frame_bytes` | `1048576` | 单个上游 SSE 帧的字节上限，包含 framing 字节 |
| `limit.concurrency` | `64` | 共享在途请求上限，超限返回 `429` |

所有数值限制都必须大于零，并发值还必须位于 Tokio 支持的 semaphore 容量内。并发许可会持有到响应体完成或被丢弃。SSE 受单帧上限和整个请求期限约束，没有累计流字节上限。

## 健康检查与当前范围

- HTTP 服务运行期间，`GET /healthz` 返回 `200`。
- 内核 Host 接受新的代际 lease 时，`GET /readyz` 返回 `200`；停止接受时返回 `503`。这个源码构建代理没有数据库就绪检查。

当前每个请求只进行一次 OpenAI 兼容上游尝试。尚未实现重试、故障转移、频率限制、额度、控制面、Admin API、WebUI、`nyro serve` 或 `nyro tool`。程序不会展开环境变量，不接受旧 Standalone YAML，不监听或热更新文件，也不会远程获取配置；修改后需要重启进程。

贡献者可以在不连接真实 Provider 的情况下验证根进程、健康检查、认证、Chat、Embedding、SSE、脱敏和正常退出：

```sh
cargo build -p nyro
python3 tests/proxy_smoke.py
```
