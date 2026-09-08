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

当前入口实现 OpenAI Chat/Embedding、无状态 Responses、Anthropic Messages 和 Gemini generateContent 的强类型子集。Chat 支持四种已支持 Chat API 格式之间的文本、函数调用／结果及 SSE 转换，不承诺完整兼容各厂商 API。未知或尚未支持的请求字段会返回 `400`，不会原样转发；尚未支持的上游响应语义会在流开始前返回 `502`，如果出现在首帧校验后的 SSE 中则终止该流。配置中的模型名是公开别名：Nyro 向上游发送所选 backend 的 `upstream_model`，并在已支持的响应中恢复公开模型名。

## 配置

所有配置结构都会拒绝未知字段。`kind` 必填，支持 `openai`、`anthropic`、`gemini`。OpenAI Provider 可选 `api: chat_completions`（默认）或 `api: responses`；其他 kind 拒绝 `api` 字段。Provider URL 必须使用 HTTP 或 HTTPS、包含主机，且不能包含用户信息、查询参数或 fragment。Nyro 在基础路径后追加原生端点。上游只使用 Provider 配置的可选 `api_key`，不会转发调用方凭据；同时禁用重定向和环境变量配置的 HTTP 代理。

| Provider kind | 基础地址示例 | 追加端点 | 上游凭据 |
|---|---|---|---|
| `openai`，默认 API | `https://api.example.com/v1` | `chat/completions` 或 `embeddings` | Bearer Authorization |
| `openai`，`api: responses` | `https://api.example.com/v1` | `responses` | Bearer Authorization |
| `anthropic` | `https://api.anthropic.com/v1` | `messages` | `x-api-key`；固定 `anthropic-version: 2023-06-01` |
| `gemini` | `https://generativelanguage.googleapis.com/v1beta` | `models/{upstream_model}:generateContent` 或 `:streamGenerateContent?alt=sse` | `x-goog-api-key` |

Gemini 上游模型名只接受由 ASCII 字母、数字、`-_.` 构成的单段名称，可带 `models/` 前缀；拒绝仅含路径点段的名称。

每个模型包含：

- `backends`：非空上游列表。每项包含模型范围内唯一且非空白的 `id`、`llm.providers` 中的 `provider` ID、`upstream_model`，以及可选整数 `weight`（默认 `100`）。
- `workloads`：非空且不能重复；所有 backend 都使用 OpenAI Chat Completions 时可包含 `chat`、`embedding` 或两者；Responses／Anthropic／Gemini 只支持 `chat`。每个 backend（包括禁用项）都必须支持模型声明的 workload，无效组合会在启动时被拒绝。
- `allow_anonymous`：可选，默认 `false`。
- `subjects`：允许调用受保护模型的客户端凭据 ID；只有匿名模型可以省略。

Backend 权重范围是 `0` 到 `4294967295`；`0` 表示不参与选择，模型的全部权重为零时启动失败。Backend ID 独立于 Provider 和上游模型名，在所属公开模型内唯一；更改 ID 会改变有效配置身份。列表顺序不表示优先级，也不会改变配置指纹。

旧模型顶层的 `provider` 和 `upstream_model` 写法继续兼容，在内存中归一化为 `id: default`、`weight: 100` 的单个 backend，与显式声明的等价配置具有相同指纹。拒绝新旧写法混用、缺少任一旧字段、显式 null 路由字段及未知字段。配置序列化输出归一化后的 `backends` 形式，不会改写源文件。

```yaml
chat-default:
  backends:
    - id: primary
      provider: example
      upstream_model: example-chat-model
      weight: 80
    - id: secondary
      provider: responses-example
      upstream_model: example-responses-model
      weight: 20
  workloads: [chat]
  subjects: [local-client]
```

认证、授权和共享准入针对公开模型执行一次。随后 Nyro 使用各 backend 对应的 codec 在本地准备请求，排除无法表达当前请求的项，并按剩余权重随机选择一个。权重作用于可用候选集合，不保证每一批请求都严格按比例分配。准备阶段不发送网络请求；没有可表达请求的 backend 时返回 `400`。例如请求未提供 token 上限时，Anthropic backend 不参与选择，而兼容的 OpenAI backend 仍可参与。准备阶段不能确定 Provider 的实际可用性，也不能保证其后续响应可转换。

每次调用只向选定上游尝试一次。上游错误、响应格式错误或断流不会触发其他 backend。请求日志会同时记录公开模型与所选 backend ID。优先级、重试／故障转移及基于健康状态的选择属于后续工作。本地路由回归：`cargo test -p nyro-llm --test routing_runtime`。

受保护模型的 `subjects` 不能为空，每一项都必须匹配 `security.api_keys` 中的 `id`。未提供支持的请求头凭据或凭据未知时返回 `401`；凭据已知但无权访问该模型时返回 `403`。匿名模型可以不带凭据访问；如果请求带了凭据，该凭据仍须有效，但任意已知凭据都可以访问匿名模型。

凭据 ID 和 secret 必须各自唯一且非空，ID 不能只含空白。客户端 secret 只能包含无空白的可见 ASCII 字符，才能作为 HTTP Bearer 凭据提交。配置错误和 Debug 输出不会暴露 secret。

| 配置项 | 默认值 | 作用 |
|---|---:|---|
| `server.listen` | `127.0.0.1:19530` | 监听地址 |
| `server.request_timeout_ms` | `120000` | 整个请求的期限，包括成功响应体或 SSE 流 |
| `server.max_body_bytes` | `1048576` | 缓冲的下游请求体上限 |
| `server.max_response_bytes` | `16777216` | 缓冲的非流式上游响应上限；不是 SSE 累计流量上限 |
| `server.max_frame_bytes` | `1048576` | 上游 SSE 帧、转换输出批次、累计工具／Responses 快照状态的字节上限 |
| `limit.concurrency` | `64` | 共享在途请求上限，超限返回 `429` |

所有数值限制都必须大于零，并发值还必须位于 Tokio 支持的 semaphore 容量内。并发许可会持有到响应体完成或被丢弃。SSE 受单帧上限和整个请求期限约束，没有累计传输字节上限。Responses 还需在 `max_frame_bytes` 内保留完整输出快照（包括生成文本），因此即使每个 delta 很小，长 Responses 流也可能触及上限。工具参数片段可能缓冲到完整后输出，累计状态受 `max_frame_bytes` 约束。只有校验协议终止信号后才输出成功终止；格式错误和断流会失败，不重试。

## 原生客户端与转换限制

| 客户端 API | 端点 | 凭据 |
|---|---|---|
| OpenAI | `POST /v1/chat/completions`、`POST /v1/responses`、`POST /v1/embeddings` | `Authorization: Bearer …` |
| Anthropic | `POST /v1/messages` | `x-api-key: …` 或 Bearer |
| Gemini | `POST /v1beta/models/{alias}:generateContent`、`:streamGenerateContent?alt=sse` | `x-goog-api-key: …` 或 Bearer |

Gemini 也接受 `/v1/models/…`。只允许提供一个凭据头，重复或冲突的凭据来源返回 `401`。拒绝 URL 查询参数中的凭据；只支持 Gemini 流式请求的 `alt=sse` 查询选项。Gemini 公开别名必须是由 ASCII 字母、数字、`-_.` 构成的单段名称。

```sh
curl http://127.0.0.1:19530/v1/messages \
  -H 'x-api-key: replace-with-client-secret' \
  -H 'Content-Type: application/json' \
  -d '{"model":"claude-default","max_tokens":256,"messages":[{"role":"user","content":"Hello"}]}'

curl http://127.0.0.1:19530/v1beta/models/gemini-default:generateContent \
  -H 'x-goog-api-key: replace-with-client-secret' \
  -H 'Content-Type: application/json' \
  -d '{"contents":[{"role":"user","parts":[{"text":"Hello"}]}],"generationConfig":{"maxOutputTokens":256}}'
```

模型别名选择上游，与客户端协议独立。Anthropic backend 只有在请求显式提供 token 上限时才参与选择（`max_tokens` 或能转换的等价字段），Nyro 不自行设置默认值。原生客户端使用 OpenAI 流式上游时会自动请求用量；OpenAI Chat Completions 客户端只有设置 `stream_options.include_usage: true` 才接收用量。

这是实验性的文本／函数 Chat 子集。新原生 codec 拒绝图片、音频、视频、thinking／签名块、无法保留的内容顺序、多候选以及不支持的厂商选项或诊断字段。例如 Anthropic 缓存／用量扩展和命中 stop sequence 的响应；Gemini 安全设置、safety ratings、grounding／引用、prompt feedback 和结构化输出设置；以及无法在原生输出中表示的 OpenAI fingerprint／service-tier／logprob 元数据。某协议已支持的字段不一定能转换到另一协议：不可转换的请求在发往上游前失败；不可转换的上游响应返回 `502` 或终止 SSE 流。原生 Embedding API 和完整 SDK／厂商功能对齐属于后续工作。

协议参考：[Anthropic streaming](https://platform.claude.com/docs/en/build-with-claude/streaming)、[Gemini generateContent](https://ai.google.dev/api/generate-content)。本地矩阵回归：`cargo test -p nyro-llm --test protocol_matrix`。

## Responses 子集

`POST /v1/responses` 接受字符串输入或强类型 message／function items、instructions、通用生成参数及客户端函数工具／结果，可使用任意已配置的 Chat 上游。原生 Responses 函数定义必须显式设置 `strict`：跨协议非严格子集使用 `false`，`true` 需要上游能够保留该语义。反向也支持所有已实现的 Chat 入口使用 `api: responses` 上游，例如：

```sh
curl http://127.0.0.1:19530/v1/responses \
  -H 'Authorization: Bearer replace-with-client-secret' \
  -H 'Content-Type: application/json' \
  -d '{"model":"responses-default","input":"Hello","max_output_tokens":256,"store":false,"stream":true}'
```

函数结果当前要求 `function_call_output.output` 为字符串，数组形式会被拒绝。

本阶段仅支持无状态调用：发往 Responses 上游时固定 `store:false`；Responses 入口转为 Chat Completions 上游时也显式禁用存储。暂不支持服务端会话（`conversation`、`previous_response_id`）、item 引用、`store:true`、`background:true`、内置工具、reasoning items、多媒体及响应查询／删除／取消。当前 Chat IR 无法保留的有效选项或输出项会被明确拒绝。Responses 外层回显字段和 item ID 会归一化，不保证保留上游原始 ID 或精确请求回显；函数 `call_id`、内容顺序、终止状态和可表达的用量属于转换契约。

SSE 使用 Responses 命名生命周期事件、稳定 item ID、递增序号和完整终态快照，不输出 `[DONE]`。token 上限／内容过滤结束会映射成 `response.incomplete`；上游失败、内容矛盾、格式错误或断流不会伪造成功终态。原生上游流关闭 obfuscation。此子集不代表已经完整兼容 Responses SDK 或 Codex CLI。

参考：[OpenAI Responses 迁移指南](https://developers.openai.com/api/docs/guides/migrate-to-responses)、[Responses streaming](https://developers.openai.com/api/docs/guides/streaming-responses)。本地回归：`cargo test -p nyro-llm --test responses_codec --test responses_runtime`。

## 健康检查与当前范围

- HTTP 服务运行期间，`GET /healthz` 返回 `200`。
- 内核 Host 接受新的代际 lease 时，`GET /readyz` 返回 `200`；停止接受时返回 `503`。这个源码构建代理没有数据库就绪检查。

当前每个请求只向选定上游尝试一次。尚未实现重试、故障转移、频率限制、额度、控制面、Admin API、WebUI、`nyro serve` 或 `nyro tool`。程序不会展开环境变量，不接受旧 Standalone YAML，不监听或热更新文件，也不会远程获取配置；修改后需要重启进程。

贡献者可以在不连接真实 Provider 的情况下验证根进程、健康检查、认证、Chat、Embedding、SSE、脱敏和正常退出：

```sh
cargo build -p nyro
python3 tests/proxy_smoke.py
```
