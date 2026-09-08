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

所有配置结构都会拒绝未知字段。`kind` 必填，支持 `openai`、`anthropic`、`gemini`。OpenAI Provider 可选 `api: chat_completions`（默认）或 `api: responses`；其他 kind 拒绝 `api` 字段。Provider URL 必须使用 HTTP 或 HTTPS、包含主机，且不能包含用户信息、查询参数或 fragment。Nyro 在基础路径后追加原生端点。上游只使用 Provider 配置的可选 `api_key`，不会转发调用方凭据；同时禁用重定向、环境变量配置的 HTTP 代理和 Reqwest 自动协议重试，由 LLM runtime 统一管理重试预算。

| Provider kind | 基础地址示例 | 追加端点 | 上游凭据 |
|---|---|---|---|
| `openai`，默认 API | `https://api.example.com/v1` | `chat/completions` 或 `embeddings` | Bearer Authorization |
| `openai`，`api: responses` | `https://api.example.com/v1` | `responses` | Bearer Authorization |
| `anthropic` | `https://api.anthropic.com/v1` | `messages` | `x-api-key`；固定 `anthropic-version: 2023-06-01` |
| `gemini` | `https://generativelanguage.googleapis.com/v1beta` | `models/{upstream_model}:generateContent` 或 `:streamGenerateContent?alt=sse` | `x-goog-api-key` |

Gemini 上游模型名只接受由 ASCII 字母、数字、`-_.` 构成的单段名称，可带 `models/` 前缀；拒绝仅含路径点段的名称。

每个模型包含：

- `backends`：非空上游列表。每项包含模型范围内唯一且非空白的 `id`、`llm.providers` 中的 `provider` ID、`upstream_model`，以及可选整数 `weight`（默认 `100`）和 `priority`（默认 `0`，数值越小越优先）。
- `max_attempts`：正整数，默认 `1`，包含首次上游发送。每个请求最多尝试每个 backend ID 一次。
- `health`：可选的被动熔断策略，省略时关闭。空对象启用默认值 `failure_threshold: 3` 和 `cooldown_ms: 30000`，两者必须大于零。
- `rate`：可选的模型请求频率，省略时关闭。见下文“请求频率”。
- `quota`：可选的模型累计 token 额度，省略时关闭。见下文“Token 额度”。
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

认证、授权和共享准入针对公开模型执行一次。随后 Nyro 使用各 backend 对应的 codec 在本地准备请求，排除无法表达当前请求的项，从当前可用的最小 `priority` 中按权重随机选择一个。权重作用于可用候选集合，不保证每一批请求都严格按比例分配。准备阶段不发送网络请求；没有可表达请求的 backend 时返回 `400`。例如请求未提供 token 上限时，Anthropic backend 不参与选择，而兼容的 OpenAI backend 仍可参与。准备阶段不能确定 Provider 的实际可用性，也不能保证其后续响应可转换。

默认每次调用只尝试一个上游。将 `max_attempts` 设置为大于 `1` 后，连接建立失败或上游返回 HTTP `429`、`500`、`502`、`503`、`504`、`529` 时，可以切换到其他候选。先尝试同一优先级的剩余 backend，再考虑更大的优先级。跳过不兼容、禁用或不健康的 backend 不消耗预算。当前没有退避和独立的单次尝试超时；所有尝试共享整个请求期限与同一个并发许可。单个慢请求可能耗尽期限而无法故障转移。取消或丢弃请求／响应体会停止其持有的工作。

其他 HTTP 状态，以及无法确认发生在请求发送前的传输错误，不触发切换。上游一旦返回 `2xx`，便固定使用该 backend：JSON 格式错误、Content-Type 不匹配、首个 SSE 帧错误或后续断流均直接失败。失败尝试的响应头和响应体会被丢弃。开启重试仍可能在多个上游产生实际工作，不保证 Provider 恰好执行一次。请求日志记录公开模型、最后选择的 backend ID 与尝试次数。

例如，以下模型优先使用 `primary`，允许故障转移至 `secondary`：

```yaml
chat-failover:
  backends:
    - {id: primary, provider: example, upstream_model: example-chat-model, priority: 0}
    - {id: secondary, provider: responses-example, upstream_model: example-responses-model, priority: 1}
  max_attempts: 2
  health: {failure_threshold: 3, cooldown_ms: 30000}
  workloads: [chat]
  subjects: [local-client]
```

开启 `health` 后，实际观察到的网络错误、上述临时错误状态和无效上游响应会计入失败阈值。完整校验成功的响应清零计数；SSE 必须完成协议终止校验，响应头和首帧不足以证明恢复。其他状态、客户端取消、响应体丢弃和整个请求期限耗尽均不改变成功／失败计数。达到阈值后跳过该 backend，冷却结束后的首个合格请求独占一次恢复探测；并发请求使用其他可用 backend，否则返回 `503`。探测成功恢复，失败则重新冷却；取消或丢弃探测只释放探测资格，不认定恢复。首次尝试前全部兼容 backend 都被阻止时返回 `503`；已发生尝试失败时返回最后一个脱敏上游错误。

健康状态以公开模型和 backend 身份为作用域。根组合层在代际之间共享状态：Provider URL/API/凭证、上游模型与健康策略不变时复用，调整权重或优先级不会清零；这些绑定发生变化则使用新状态。旧代际及请求释放后，旧绑定可回收。没有后台探测，也不跨进程重启持久化。

本地路由回归：`cargo test -p nyro-llm --test routing_runtime --test failover_runtime`。

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

## 请求频率

每个公开模型可以显式启用内存令牌桶：

```yaml
rate:
  requests: 60
  period_ms: 60000
  burst: 5
```

`requests` 和 `period_ms` 必填且为正整数，`burst` 默认为 `1`，也必须为正数。请求数与突发容量须在 `u32` 范围内，周期须能由平台单调时钟表示。上述配置初始允许连续接纳五个请求，随后以每秒一个请求的速度连续补充，最多积累五个。这表示持续平均速率与突发容量，并不保证任意滚动一分钟内都不超过 60 次。实现不创建补充任务，也不排队等待。

同一公开模型的所有调用方（包括匿名调用）、工作负载和入口协议共享桶。不同模型别名使用独立桶，即使它们指向同一个 Provider 或上游模型。计数仅在当前进程内共享，多副本之间不共享。这里限制逻辑请求数，不计生成 token，也不代表并发流数量。

认证、授权、并发准入、兼容 backend 准备和首次取消／期限检查先于 rate 准入；这些检查拒绝时不扣减频率容量。通过 rate 准入后，无论上游成功、失败、没有健康 backend、随后取消或响应体被丢弃，都消耗一个单位。内部重试和故障转移不重复扣减，请求完成或失败也不退还容量。

超限返回对应协议的脱敏 `429`，并带有向上取整到秒的 `Retry-After`。不会调用上游，并立即释放刚取得的并发许可，即使错误响应体尚未读取。等待时间只是建议，其他调用方可能先消耗下一份容量。并发拒绝或上游错误等其他 `429` 原因不会附加此 rate 响应头。

根组合层在配置代际之间共享 rate 状态。公开模型的规则不变时，修改路由、Provider 凭证或其他设置不会重置余额。旧规则绑定仍活跃时，修改其参数会使候选构建失败；更改参数需要重启进程。移除或关闭规则后，旧代际仍按原绑定完成，最后一个持有者释放时回收状态。新绑定或新进程以配置的突发容量启动，不持久化计数。文件代理本身仍需重启才能加载任何文件修改。

可复用原语为 `nyro_limit::rate::RateLimit`，接收数量、`Duration` 与突发容量，返回准入结果或建议等待时长，不依赖 LLM、认证、HTTP、内核或数据库类型；作用域映射与协议拒绝结果由应用决定。回归测试：`cargo test -p nyro-limit` 和 `cargo test -p nyro-llm --test rate_runtime`。

## Token 额度

在公开模型下添加可选的 `quota`：

```yaml
quota:
  total_tokens: 1000000
  reserve_tokens: 4096
```

两个字段均为必填正整数，且 `reserve_tokens` 不得超过 `total_tokens`。显式 `null` 和未知字段会被拒绝。同一别名的所有调用方、工作负载和入口 API 共享输入加输出 token 的累计额度，不同别名独立计量。当前仅为进程内记账，不提供定时补充、持久化、金额计费或副本间协调。

认证、授权、并发和 rate 准入之后，Nyro 在每次可用上游尝试发出前预留 `reserve_tokens`，每次重试都需要独立预留。没有可用 backend 时不预留。准入原子检查已结算用量加在途预留；余额不足返回原生协议 `429`：OpenAI API 使用 `quota_exceeded`，Anthropic 使用 `rate_limit_error`，Gemini 使用 `RESOURCE_EXHAUSTED`。不带 `Retry-After`，不发出新的上游请求，并立即释放并发许可。此前 rate 准入已消耗的次数不退还。剩余余额小于 `reserve_tokens` 时无法再准入一次尝试。

完整响应有效时，以报告的总用量替换预留，包括显式零用量和超过预留的用量。JSON 在向下游编码前结算，SSE 在校验上游协议终止信号时结算。用量快照按累计值替换，不逐帧相加，且不能递减；输入加输出必须无溢出地等于总量，Embedding 的输入必须等于总量。无效用量导致响应失败：流开始前返回 `502`，开始后终止流。启用 quota 后，即使客户端未请求输出 usage，OpenAI Chat 上游流请求也会主动请求用量；下游仍保留客户端的输出偏好。

结算前遇到用量缺失、上游 HTTP 错误、无效响应、取消、超时、响应体丢弃或断流，均按预留量与已知有效用量的较大值扣减。已结算后发生的下游失败或响应体丢弃不改变扣减结果。只有实际观察到连接建立失败才全额释放预留；故障转移之前每次失败的 HTTP 尝试独立扣减。这种保守回退可能多计实际用量。另一方面，`reserve_tokens` 是运维配置值，不是可信的上游消耗上界：实际消耗可能超过配置预算，超出部分如实记账并阻止后续准入，当前不承诺真实上游 token 的硬上限。

根组合层在代际、路由变更和模型移除／加回之间保留已消耗及在途账本。规则绑定仍活跃或账本已有消耗时，修改规则会使候选构建失败；无持有者且没有消耗或预留的候选账本可被回收。保留账本持续至注册表销毁，进程重启会清空余额，也是更改已建立规则的方式。文件代理仍需重启才能加载配置修改。关闭 quota 后新请求停止计量，但不会删除已有账本。

`nyro_limit::quota::Quota` 提供通用原子预留、结算和快照，不依赖 LLM、HTTP、内核或存储类型；`nyro_llm::quota::QuotaRegistry` 负责模型映射与 token 语义。库调用方应在代际间复用 `runtime::SharedResources` 并通过 `Runtime::with_resources` 构建运行时；`Runtime::new` 创建全新注册表，`with_health` 仅共享健康状态。回归测试：`cargo test -p nyro-limit` 和 `cargo test -p nyro-llm --test quota_runtime`。

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
- 内核 Host 接受新的代际 lease 时，`GET /readyz` 返回 `200`；停止接受时返回 `503`。这个源码构建代理没有数据库或上游 backend 就绪检查。

重试与被动健康检查需要按上述配置显式开启。尚未实现额度的持久化／跨进程共享存储、控制面、Admin API、WebUI、`nyro serve` 或 `nyro tool`。程序不会展开环境变量，不接受旧 Standalone YAML，不监听或热更新文件，也不会远程获取配置；修改后需要重启进程。

贡献者可以在不连接真实 Provider 的情况下验证根进程、健康检查、认证、Chat、Embedding、SSE、脱敏和正常退出：

```sh
cargo build -p nyro
python3 tests/proxy_smoke.py
```
