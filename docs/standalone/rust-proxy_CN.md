# 实验性 Rust 文件配置代理

[English](rust-proxy.md)

根 `nyro` 二进制已包含一个实验性的 LLM 数据面，目前需要从源码构建。它与 [README](README.md) 介绍的已发布 `nyro-server` Standalone 模式相互独立，YAML 格式也不兼容。

## 从源码运行

复制 [rust-proxy.yaml](rust-proxy.yaml)，替换示例中的 Provider 地址、模型名和密钥，然后运行：

```sh
cargo run -p nyro -- proxy --config docs/standalone/rust-proxy.yaml
```

程序在绑定监听端口前读取并校验文件。默认地址是 `127.0.0.1:19530`。Unix 上可通过下述 `SIGHUP` 方式重载同一文件。

使用 `security.api_keys` 中配置的客户端凭据请求受保护的 Chat Completions 模型：

```sh
curl http://127.0.0.1:19530/v1/chat/completions \
  -H 'Authorization: Bearer replace-with-client-secret' \
  -H 'Content-Type: application/json' \
  -d '{"model":"chat-default","messages":[{"role":"user","content":"Hello"}]}'
```

当前入口实现 OpenAI Chat/Embedding、无状态 Responses、Anthropic Messages 和 Gemini generateContent 的强类型子集。Chat 支持四种已支持 Chat API 格式之间的文本、函数调用／结果及 SSE 转换，不承诺完整兼容各厂商 API。默认情况下，未知或尚未支持的请求字段会返回 `400`，不会原样转发；尚未支持的上游响应语义会在流开始前返回 `502`，如果出现在首帧校验后的 SSE 中则终止该流。下文的 OpenAI Chat、Anthropic Messages 和 Gemini generateContent 原生模式可显式开启，在匹配的端点之间保留厂商 JSON 字段。配置中的模型名是公开别名：Nyro 向上游发送所选 backend 的 `upstream_model`，并在类型化响应及 OpenAI／Anthropic 原生响应中恢复公开模型名；Gemini 原生响应保留 `modelVersion` 作为上游版本信息。

## 查询可见模型

`GET /v1/models` 按别名升序返回调用方可见的已配置公开模型别名：

```sh
curl http://127.0.0.1:19530/v1/models \
  -H 'Authorization: Bearer replace-with-client-secret'
```

响应采用 OpenAI 列表格式：`{"object":"list","data":[{"id":"public-alias","object":"model","created":0,"owned_by":"Nyro"}]}`。`created: 0` 为固定占位值，不是 Provider 时间戳。不提供凭证时，仅列出 `allow_anonymous: true` 的模型；有效 Bearer 密钥还可看到授权给其主体的模型。没有可见模型时返回 `200` 和空 `data` 数组。无效、重复或冲突凭证返回 `401`，即使存在公开模型也不会回退。该 OpenAI 格式端点仅接受 Bearer 凭证，不接受 `x-api-key` 或 `x-goog-api-key`。拒绝 query 凭证，不支持分页或其他 query 参数；仅支持 `GET`。

列表取自同一活动运行时代际，不包含上游模型名、Provider 地址或密钥。成功重载后列表与凭证更新，失败重载保持原列表。查询不访问 Provider，不消耗推理并发、rate 或 quota；推理预算耗尽或 backend 不健康时，模型仍可被发现，可见性表示授权范围，不保证实际可用。响应设置 `Cache-Control: no-store`，仍使用常规请求 ID、期限和响应体清理。模型发现的请求观测使用 `protocol=openai_models`、`workload=none`、零尝试和 `usage_state=not_attempted`。

回归：`cargo test -p nyro-llm --test models_runtime`；构建根二进制后执行 `python3 tests/proxy_reload_smoke.py`。

## 文件重载

Unix 上保存完整配置后，向正在运行的 **nyro 进程**发送 `SIGHUP`。例如使用源码构建的二进制：

```sh
target/debug/nyro proxy --config docs/standalone/rust-proxy.yaml &
nyro_pid=$!
# 保存新配置，建议通过原子替换发布完整文件。
kill -HUP "$nyro_pid"
```

信号处理器在服务就绪前注册。每次重载重新读取原 `--config` 路径，支持通过 rename 替换文件。重载只接受常规文件（允许指向常规文件的符号链接），拒绝目录、FIFO 等特殊文件。重载串行执行，多个信号可能合并，不构成配置版本队列。程序不会自动监听文件变化；Windows 上修改后仍需重启。

重载先校验整个文件及必须重启的设置，再比较有效配置指纹。等价配置直接跳过候选构建，保持原代际。有效变更构建候选后原子发布：新请求使用新代际，在途请求持有原路由、凭证、响应限制和期限直至响应体清理。移除模型或轮换密钥不会撤销已经准入的工作。读取、校验、候选构建或激活失败时，当前代际继续服务。退出时先取消在途重载激活，再由内核清理；重载发布使用十秒期限。

`server.listen` 和 `limit.concurrency` 必须重启才能修改。其他已支持的请求设置、路由、Provider 和凭证可以重载。rate/quota 规则不变时保留计数；修改活跃 rate 规则或活跃／已消耗 quota 规则仍按下文契约拒绝候选。健康状态只对身份未改变的 backend 复用。重载不清空进程内预算，也不强制结束旧流。

`nyro::reload` 事件通过 `outcome=applied` 或 `unchanged` 与数值代际 ID 表示结果。拒绝事件使用 `outcome=rejected`，并给出安全的原因：

| 原因 | 处理方式 |
|---|---|
| `read_failed` | 恢复可读取的配置文件 |
| `invalid_config` | 修正 YAML、未知字段、引用或无效值 |
| `restart_required` | 恢复原监听／并发设置，或重启以修改它们 |
| `candidate_rejected` | 检查活跃 rate/quota 规则变更及运行时构建约束 |
| `activation_failed` / `interrupted` | 检查退出／期限状态后再重试 |

重载日志不包含配置正文、文件路径、指纹或详细错误链。被拒绝的文件不会被写回，修正后重新发送信号即可。回归测试：`cargo test -p nyro reload::tests`；构建根二进制后执行 `python3 tests/proxy_reload_smoke.py`。

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

认证、授权和共享准入针对公开模型执行一次。随后 Nyro 使用各 backend 对应的严格 codec 或下文的原生模式在本地准备请求，排除无法表达当前请求的项，从当前可用的最小 `priority` 中按权重随机选择一个。权重作用于可用候选集合，不保证每一批请求都严格按比例分配。准备阶段不发送网络请求；没有可表达请求的 backend 时返回 `400`。例如请求未提供 token 上限时，Anthropic backend 不参与选择，而兼容的 OpenAI backend 仍可参与。准备阶段不能确定 Provider 的实际可用性，也不能保证其后续响应可转换。

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

健康状态以公开模型和 backend 身份为作用域。根组合层在代际之间共享状态：Provider URL/API/凭证/原生模式、上游模型与健康策略不变时复用，调整权重或优先级不会清零；这些绑定发生变化则使用新状态。旧代际及请求释放后，旧绑定可回收。没有后台探测，也不跨进程重启持久化。

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

根组合层在配置代际之间共享 rate 状态。公开模型的规则不变时，修改路由、Provider 凭证或其他设置不会重置余额。旧规则绑定仍活跃时，修改其参数会使候选构建失败；更改参数需要重启进程。移除或关闭规则后，旧代际仍按原绑定完成，最后一个持有者释放时回收状态。新绑定或新进程以配置的突发容量启动，不持久化计数。Unix 上其他已支持的文件修改可通过 SIGHUP 重载。

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

完整响应有效时，以报告的总用量替换预留，包括显式零用量和超过预留的用量。JSON 在向下游编码前结算，SSE 在校验上游协议终止信号时结算。用量快照按累计值替换，不逐帧相加，且不能递减；输入加输出必须无溢出地等于总量，Embedding 的输入必须等于总量。无效用量导致响应失败：流开始前返回 `502`，开始后终止流。为收集用量，即使客户端未请求输出 usage，OpenAI Chat 上游流请求也会主动请求用量；下游仍保留客户端的输出偏好。

结算前遇到用量缺失、上游 HTTP 错误、无效响应、取消、超时、响应体丢弃或断流，均按预留量与已知有效用量的较大值扣减。已结算后发生的下游失败或响应体丢弃不改变扣减结果。只有实际观察到连接建立失败才全额释放预留；故障转移之前每次失败的 HTTP 尝试独立扣减。这种保守回退可能多计实际用量。另一方面，`reserve_tokens` 是运维配置值，不是可信的上游消耗上界：实际消耗可能超过配置预算，超出部分如实记账并阻止后续准入，当前不承诺真实上游 token 的硬上限。

根组合层在代际、路由变更和模型移除／加回之间保留已消耗及在途账本。规则绑定仍活跃或账本已有消耗时，修改规则会使候选构建失败；无持有者且没有消耗或预留的候选账本可被回收。保留账本持续至注册表销毁，进程重启会清空余额，也是更改已建立规则的方式。Unix 上其他已支持的配置修改可通过 SIGHUP 重载。关闭 quota 后新请求停止计量，但不会删除已有账本。

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

严格转换路径是实验性的文本／函数 Chat 子集，其 codec 拒绝图片、音频、视频、thinking／签名块、无法保留的内容顺序、多候选以及不支持的厂商选项或诊断字段。例如 Anthropic 缓存／用量扩展和命中 stop sequence 的响应；Gemini 安全设置、safety ratings、grounding／引用、prompt feedback 和结构化输出设置；以及无法在原生输出中表示的 OpenAI fingerprint／service-tier／logprob 元数据。某协议已支持的字段不一定能转换到另一协议：不可转换的请求在发往上游前失败；不可转换的上游响应返回 `502` 或终止 SSE 流。原生 Embedding API 和完整 SDK／厂商功能对齐属于后续工作。

协议参考：[Anthropic streaming](https://platform.claude.com/docs/en/build-with-claude/streaming)、[Gemini generateContent](https://ai.google.dev/api/generate-content)。本地矩阵回归：`cargo test -p nyro-llm --test protocol_matrix`。

## 显式开启 OpenAI Chat 原生兼容

为 OpenAI Chat Provider 设置 `native_chat: true`，可以在 `POST /v1/chat/completions` 与 OpenAI Chat 上游之间保留厂商 JSON 字段：

```yaml
llm:
  providers:
    example:
      kind: openai
      api: chat_completions
      native_chat: true
      base_url: https://api.example.com/v1
      api_key: replace-with-provider-secret
```

这段 Provider 配置应合入现有配置。默认值为 `false`；OpenAI Chat、Anthropic Messages 和 Gemini Provider 支持开启，`api: responses` 拒绝设置为 `true`。OpenAI Provider 只对匹配的 OpenAI Chat 入口启用此模式，其他入口 API 和 Embedding 继续经过严格 codec。

Nyro 保留请求 JSON、非流式响应 JSON 和 SSE data JSON，包括嵌套的推理／工具历史、厂商选项和 usage 详情。请求顶层 model 替换为上游模型，响应／chunk 则恢复公开别名。流式请求强制上游 `stream_options.include_usage: true`，其他选项保持；下游只在客户端要求时输出 usage。隐藏 usage 时省略纯 usage chunk，计量仍然执行。

原生模式校验路由／控制结构（model、消息 role、流式选项）、响应／chunk 结构、数字用量以及有界 SSE 帧，并要求显式 `[DONE]` 终止；厂商字段的语义由所选上游处理。保真粒度是 JSON 字段：序列化和 SSE 帧格式可能变化，注释／ID／retry 字段会丢弃，event 名只接受省略、空值或 `message`，也不会任意转发 Header。认证、授权、准入、quota 结算、重试上限、期限及清理仍强制执行，上游失败响应继续脱敏。切换原生模式会改变配置指纹，并重置受影响的健康状态绑定。

混合 backend 池中，原生请求只有在**原始请求**通过严格 OpenAI codec 且目标能够表达时，才允许使用严格模式或其他协议 backend。不会通过删除扩展字段让故障转移变得可用。本能力不承诺跨协议推理／媒体转换、完整 SDK 会话，也不包含 Responses 原生保真。

本地录制回归：构建 `cargo build -p nyro` 后运行 `python3 tests/proxy_native_replay.py`。它完整比较 DeepSeek、Zhipu AI 的 8 份 OpenAI Chat 录制请求／响应序列，同时回放 8 份 Anthropic Messages 样本，并分类默认严格模式下全部 16 份录制数据。这些历史样本不等于真实厂商在线认证。

## 显式开启 Anthropic Messages 原生兼容

Anthropic Provider 同样可以设置 `native_chat: true`，只对 `POST /v1/messages` 入口与 Anthropic 上游之间生效。保留 `kind: anthropic`、基础 URL 和静态 API Key，不设置 OpenAI 专用的 `api` 选择器。混合池中的 OpenAI／Anthropic 原生 backend 不能直接互换：跨协议仍须让原始 JSON 通过来源严格 codec，且目标能够表达。

请求保留 system／cache 配置、thinking／签名、工具历史和厂商字段。Nyro 校验 model、messages、正整数 `max_tokens` 和流式控制，厂商语义由上游校验。JSON 响应保留内容块、停止详情及 usage 扩展，只将 model 恢复为公开别名。SSE 保留 event 名和 data JSON，包括 ping、thinking／signature delta 和工具参数片段；改写嵌套的 `message_start.message.model`。帧格式、注释、ID 和 retry 字段不保证字节一致。

原生流校验 event 名／type 匹配及生命周期：一次 `message_start`、按顺序编号的内容块、message delta，最后在已有停止原因且所有块关闭后接受 `message_stop`。携带用量的 message delta 可以重复；省略停止原因时沿用已有值，显式 null 会清除它，需要后续重新报告非空停止原因才能完成。缺少终态、格式错误、事件乱序、未知顶层事件和上游 error 事件都会失败，收到 `2xx` 后不再重试，上游错误正文不会直接转发。内容块扩展在此生命周期内保持不透明，对已知块／delta 的基础字段做类型校验。工具片段逐段转发，不组装或修复最终 JSON；thinking 签名不做密码学校验。这些由上游负责，不代表跨协议转换已经支持。

quota 和观测中的总输入为 `input_tokens + cache_creation_input_tokens + cache_read_input_tokens`，缺省缓存计数从零开始。`message_delta` 使用累计快照，每个 message delta 必须报告 `output_tokens`，省略的输入／缓存计数沿用已有值，重复快照不会重复累加。无效数字、计数递减或溢出会使响应失败。总输入与输出相加得到总 token；嵌套缓存时长明细、service tier 和服务端工具计数会保留，但不额外计入 token。到 `message_stop` 才成功结算，此前失败／丢弃仍使用既有保守扣费规则。参见 [Anthropic 缓存计量](https://platform.claude.com/docs/en/build-with-claude/prompt-caching#tracking-cache-performance)及[流事件约定](https://platform.claude.com/docs/en/build-with-claude/streaming)。

认证、授权、rate／并发准入、健康状态、期限和代际所有权沿用现有路径。上游仍使用配置中的 `x-api-key` 和 `anthropic-version: 2023-06-01`，不转发调用方凭证、任意 Header 或 `anthropic-beta`。本轮不新增 OAuth／账号通道及 beta Header 配置。

## 显式开启 Gemini generateContent 原生兼容

为 `kind: gemini` Provider 设置 `native_chat: true`，不设置 `api`。它只对匹配的 Gemini 入口／上游生效，跨协议 fallback 仍须通过来源严格 codec。支持 `/v1beta/models/{alias}:generateContent` 与 `/v1/models/{alias}:generateContent`，以及对应的 `:streamGenerateContent` 动作。流式查询参数只允许 `alt=sse`，凭证必须放在 Header 中。

公开模型和流式模式由 URL 决定。Nyro 将上游 URL 中的模型替换为配置中的 `upstream_model`，不会向 JSON 注入 `model` 或 `stream`；客户端正文包含这两个字段时拒绝，即使值为 null。响应的 `modelVersion` 保留为上游版本信息，不改为别名。上游只使用 Provider 配置的 `x-goog-api-key`，不转发调用方密钥或任意 Header。

原生请求／响应保留 JSON 字段，包括思考签名、函数历史、内联／文件媒体数据、缓存、安全配置／评级、grounding／引用及结构化输出选项。这些内容留在 runtime 私有路径中。Nyro 校验 contents／parts、路由结构及已知字段的基本类型；厂商语义与签名验证由上游负责。可选的非计量字段按 ProtoJSON 将 null 视为未设置，并在 JSON 中保留；这不会放宽必填计量数字或禁止的正文路由字段。本轮延续单候选范围：`candidateCount` 只能未设置或为 `1`，响应候选 index 只能为 `0` 或未设置，多个候选会被拒绝。被拦截的提示词可以不返回候选，但 `promptFeedback.blockReason` 必须为非空且非未指定值；反馈 JSON 原样保留，HTTP 为 `200`。

Gemini SSE 使用 JSON data 帧，不合成 `[DONE]`。非空且非未指定的候选结束原因或提示词拦截原因建立终态，但仅在正常 EOF 后成功结算；尾部纯 usage 帧仍然输出并参与计量。终态之后再出现候选、帧格式错误、上游 error 正文、传输失败或缺少终态的 EOF 都会失败，收到 `2xx` 后不再重试。未知非空结束／拦截枚举值予以保留，以兼容未来扩展；event 名只允许省略、空值或 `message`。

提供 `usageMetadata` 时，`promptTokenCount` 和 `totalTokenCount` 必须为非负整数；省略的 candidate／thought 计数视为零，并须与报告总量一致。输入为 prompt token（已经包含缓存），输出为 candidate 加 thought token，总量采用有溢出检查的求和。缓存计数不得大于 prompt，不再重复相加。输入、输出和总量快照不得递减；candidate／thought 内部分配或缓存子集变化，只要满足这些聚合约束就会保留。本轮尚未实现独立工具提示词计量，因此拒绝非零 `toolUsePromptTokenCount`。详情数组及其他元数据保留，但不重复计费。参见 [Gemini 响应及用量约定](https://ai.google.dev/api/generate-content#UsageMetadata)。

全程缺失 usage 时保留“用量未知”的 fallback 计量；若前段报告过 usage，则终态帧或之后必须再次报告完整快照，否则 EOF 失败并使用保守扣费。不能仅因连接结束，就把早期零输出快照当成最终用量。期限、取消、rate／并发、健康与代际清理继续走共享路径。

构建 `nyro` 后运行 `python3 tests/proxy_gemini_native_smoke.py`。该测试是本地 mock 契约回归，不是录制样本或真实 SDK／厂商认证；现有 16 份 OpenAI／Anthropic 录制数据仍单独回放。多候选、独立工具提示词计量、Gemini Interactions 及跨协议思考／媒体转换不属于本轮交付。

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

## 请求与用量观测

LLM 运行时以 `INFO` 级别输出结构化 `tracing` 事件：每次已发出的上游尝试记录一次 `nyro::attempt`，每个请求结束时记录一次 `nyro::request`。根程序默认过滤规则 `nyro=info` 包含两者，可用 `RUST_LOG` 调整。每个运行时请求生成随机 128 位 `request_id`，通过 `x-request-id` 响应头返回 32 位十六进制字符串；不采用或转发客户端提供的 ID。健康探针与获取运行代际之前被拒绝的请求不在此范围内。

| 记录 | 字段及含义 |
|---|---|
| 两者共有 | `request_id`、已配置的公开 `model`、`backend`、API `protocol` 与 `duration_ms` |
| 请求 | `workload`、`streaming`、`attempts`、HTTP `status`（产生响应前为 `0`）、`outcome`、`delivery_outcome`、安全的 `error_code`、用量汇总与 quota 扣减 |
| 上游尝试 | 从 `1` 开始的 `attempt`、配置中的 `provider` ID、`upstream_status`（响应头前为 `0`）、`outcome`、用量与额度结算 |

请求 `outcome` 为 `complete`、`error`、`cancelled` 或 `timeout`；`delivery_outcome` 独立描述响应体交付，处理 future 在交付前被丢弃时为 `none`。错误响应体正常读到 EOF 不会把请求标记成功。已解码的 JSON 响应被丢弃时，可以同时出现请求取消、上游尝试完成。尝试结果区分 `complete`、`http_error`、`connect_error`、`transport_error`、`protocol_error`、`cancelled` 和 `timeout`。尝试完成表示 JSON 校验完成或收到 SSE 协议终止，不证明客户端收到了全部字节。耗时覆盖各自持有资源的生命周期，不表示网络刷新时间或首 token 延迟。`error_code` 记录交付前的执行错误，之后的响应体失败通过 outcome 字段表达。

关闭 quota 时仍收集用量。流式 OpenAI Chat 上游请求始终要求报告 usage，下游是否输出 usage 仍遵循客户端偏好。每次尝试记录最后一个有效累计快照的 `input_tokens`、`output_tokens`、`total_tokens`；字段缺失表示未知，显式零仍是已知用量。`usage_state` 分为 `complete`、`partial`、`missing`、`invalid`。总量不一致或递减时标记无效，不更新观测值；未启用 quota 时不会因此新增协议拒绝，既有 codec 校验仍适用。启用 quota 后保持原有严格记账校验。

请求用量汇总各次尝试的有效观测，不累加重复流快照。任一尝试缺失用量或未完成时，不会报告完整用量；此时总数仅包含已观测单位，使用前应检查 `usage_state`。没有上游尝试时为 `not_attempted`。未启用 quota 时，尝试的 `quota_charged_tokens` 字段缺失；`quota_outcome` 区分 `actual`、`fallback`、`released`、`disabled`。请求的 `quota_charged_tokens` 汇总真实账本扣减，包括保守回退值，不能与上游报告的 token 用量混用。

事件不包含凭证、Provider URL、请求路径、客户端提供的 ID、提示词或响应正文，也不记录客户端提交的未知模型名。当前输出由宿主 tracing subscriber 接收，尚无持久化事件库、统计查询 API、指标导出或分布式追踪传播。崩溃、强制退出、过滤规则和输出端故障可能导致记录丢失；quota 结算不依赖日志消费者。回归测试：`cargo test -p nyro-llm --test observation_runtime`。

## 健康检查与当前范围

- HTTP 服务运行期间，`GET /healthz` 返回 `200`。
- 内核 Host 接受新的代际 lease 时，`GET /readyz` 返回 `200`；停止接受时返回 `503`。这个源码构建代理没有数据库或上游 backend 就绪检查。

重试与被动健康检查需要按上述配置显式开启。尚未实现额度的持久化／跨进程共享存储、控制面、Admin API、WebUI、`nyro serve` 或 `nyro tool`。程序不会展开环境变量，不接受旧 Standalone YAML，不自动监听文件，也不会远程获取配置；Unix 支持显式 SIGHUP 重载，不支持重载的设置仍需重启。

贡献者可以在不连接真实 Provider 的情况下验证根进程、健康检查、认证、Chat、Embedding、SSE、脱敏和正常退出：

```sh
cargo build -p nyro
python3 tests/proxy_smoke.py
```
