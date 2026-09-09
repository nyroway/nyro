# Rust 迁移差异审计

审计基线：`d943c174`（2026-09-09，含 PR #323）。后续关闭状态：G01 已由 PR #324 补齐；本轮在 `6b26f8f1` 上推进 G02 的 OpenAI Chat 原生兼容，其余差异继续跟踪。本文核对仓库实现和测试，不代表真实厂商或 SDK 兼容认证。[架构文档](architecture.md)仍是目标设计，[实验性代理指南](../standalone/rust-proxy_CN.md)说明当前支持范围。

**新文件配置 LLM 数据面已具备主要执行和生命周期机制，尚不能替换已发布 Server。** 同名能力不等于契约已迁移：模型 token bucket 不等于 API Key 请求窗口；存在 Responses 端点也不等于兼容 Codex 账号通道。

本文记录现状与建议顺序，不授权下线旧功能、修改用户数据或创建所有目标 crate。移除旧消费者前，每项差异必须有行为与回归证据，或经过明确接受的兼容性变更。

文档生命周期：本文作为临时迁移清单纳入 Git，仅维护这一份中文版本。整体迁移完成、切换验收通过后，将仍需保留的架构与用户升级说明移入正式文档，并在迁移收尾 PR 中删除本文件及其引用，不做归档。

## 1. 已有基础及其范围

| 能力 | 当前证据与边界 |
|---|---|
| 生命周期与代际 | [内核](../../crates/nyro-kernel/README_CN.md)、[装配](../../src/bootstrap.rs)和[重载](../../src/reload.rs)：候选激活、失败清理、lease 和退役；Unix 显式 SIGHUP 重载。没有远程配置来源。 |
| 类型化工作负载 | [IR](../../crates/nyro-llm/src/ir/mod.rs)：`Request`/`Response` 配对 Chat 和 Embedding，其他工作负载未实现。 |
| 协议执行 | [矩阵测试](../../crates/nyro-llm/tests/protocol_matrix.rs)覆盖三种 Chat 格式，[Responses 运行时测试](../../crates/nyro-llm/tests/responses_runtime.rs)补充 Responses 组合。覆盖已支持的文本、函数和流，不代表任意厂商字段兼容。 |
| 准入 | [安全包](../../crates/nyro-security/src/lib.rs)、[rate 运行时](../../crates/nyro-llm/tests/rate_runtime.rs)和[quota 运行时](../../crates/nyro-llm/tests/quota_runtime.rs)：凭证认证、模型授权、进程并发、按模型 rate 和累计 token 预留。旧契约差异见 G06–G07。 |
| 路由与健康 | [路由测试](../../crates/nyro-llm/tests/routing_runtime.rs)和[故障转移测试](../../crates/nyro-llm/tests/failover_runtime.rs)：优先级／权重选择、有界尝试、被动健康检查，尚不覆盖旧版全部四种策略。 |
| 交付与观测 | [响应体所有权](../../src/http/body.rs)、[观测](../../crates/nyro-llm/tests/observation_runtime.rs)和[进程测试](../../tests/proxy_smoke.py)：期限、取消、SSE 终止、请求／尝试关联日志及清理。没有持久化请求查询服务。 |
| 共享状态重载 | [重载进程测试](../../tests/proxy_reload_smoke.py)：去重、拒绝变更、持有 SSE 时轮换凭证／路由、rate/quota/health 复用、FIFO 拒绝及退出。监听／并发和既有策略变更限制仍存在。 |

## 2. 替换旧入口前的数据面差异

“缺失”表示没有发现等价的新路径；“部分”表示已有可运行子集；“待取舍”表示差异确实存在，但照搬旧行为不一定正确。对保留的迁移范围，三类都必须明确处理结果。

| ID | 状态 | 旧实现证据 → 新行为 | 切换前要求 |
|---|---|---|---|
| G01 模型发现 | 已落地 | [旧模型列表](../../crates/nyro-core/src/proxy/handler.rs)的发现能力已由[新运行时](../../crates/nyro-llm/src/runtime.rs)承接；[模型列表测试](../../crates/nyro-llm/tests/models_runtime.rs)与[重载进程测试](../../tests/proxy_reload_smoke.py)覆盖过滤及代际变化。 | 按授权返回稳定排序的公开别名；匿名／绑定／无效／歧义凭证、空列表、成功／失败重载、密钥轮换和预算隔离已覆盖。无效密钥明确拒绝，不沿用旧公开列表回退；密钥到期仍由 G06 跟踪。 |
| G02 同协议保真 | 部分 | [旧调度器](../../crates/nyro-core/src/proxy/dispatcher/mod.rs)按条件选择 Native 模式；[请求构建](../../crates/nyro-core/src/provider/common/pipeline.rs)和[非流式响应](../../crates/nyro-core/src/proxy/dispatcher/non_stream.rs)可跳过 IR 往返。[新运行时](../../crates/nyro-llm/src/runtime/native.rs)通过 Provider `native_chat: true` 保留 OpenAI Chat 请求、JSON 响应和 SSE data 扩展；默认仍严格转换。 | OpenAI Chat 的模型重写、usage、凭证隔离、重试兼容筛选、有界解析及流终态已覆盖；Anthropic、Gemini、Responses 原生保真仍待实现／验收。只保留 JSON 字段，不承诺字节或任意 Header 透传；跨协议原始请求仍须通过严格 codec。 |
| G03 推理、缓存与媒体语义 | 部分 | [旧转换测试](../../crates/nyro-core/tests/protocol_conversion.rs)覆盖 thinking／签名回放、`reasoning_content`、think-tag 归一化和 Gemini `fileData`。[新版限制](../standalone/rust-proxy_CN.md)的严格转换路径仍拒绝未支持的 Chat 内容块、缓存扩展和 Responses reasoning items；OpenAI Chat 原生模式只保留这些 JSON 字段，不新增跨协议语义映射。 | 为保留客户端建立字段／场景矩阵，保留可表达语义，明确拒绝不可表达的跨协议转换。OpenAI Chat 已有图片／音频类型字段，不能笼统说所有多模态都缺失；新增独立 Image/Audio/Video 操作另算范围。 |
| G04 工具历史与 Schema 处理 | 部分／待取舍 | 旧转换测试包含合成调用、重复 ID 修复、丢弃中间文本／孤立调用、Gemini Schema 裁剪。新 [Anthropic](../../crates/nyro-llm/tests/anthropic_codec.rs)、[Gemini](../../crates/nyro-llm/tests/gemini_codec.rs)、[Responses](../../crates/nyro-llm/tests/responses_codec.rs)测试会拒绝部分顺序或身份场景。 | 回放并行调用、交错文本、工具结果和 Schema，保留合法客户端历史；不自动迁移虚构调用或丢内容的处理。区分有意拒绝和兼容回退。 |
| G05 Provider 通道与凭证 | 部分 | 旧版有[厂商适配](../../crates/nyro-core/src/provider/mod.rs)、[账号认证 driver](../../crates/nyro-core/src/auth/drivers/mod.rs)和 Vertex 服务账号支持。新 Provider 配置只有协议 kind、可选 OpenAI API、URL 和可选静态 API Key。 | 迁移必要端点、Header 和凭证行为，不向 driver 传递 Gateway 或数据库实体。账号授权、刷新、持久化归控制面；driver 消费已解析凭证。详见下方清单。 |
| G06 API Key 生命周期 | 部分 | [旧授权](../../crates/nyro-core/src/proxy/dispatcher/auth.rs)检查启用状态、过期时间和模型绑定。新 `ApiKey` 只有 `id`、`secret`；可以重载移除密钥，但没有到期自动失效。 | 对新准入执行到期检查；发布配置保留绑定及禁用语义，验证时间边界和重载；明确已准入请求保持原代际。模型发现与调用授权保持一致。 |
| G07 限制作用域与持久化 | 部分 | 旧授权按 API Key 查询 Minute/Day 窗口的请求／token 数（`rpm/rpd/tpm/tpd`）。新 rate 是模型级 token bucket；quota 是模型级、累计、进程内 token 额度。 | 明确主体／模型作用域、窗口／重置、尝试与完成的计量、重启行为；测试同一密钥跨模型、多密钥共享模型。显式迁移策略含义，不静默将 RPM 转成不同桶规则或将 TPM 转成累计额度；也不复制旧日志查询准入的竞争问题。 |
| G08 均衡与重试策略 | 部分／待取舍 | [旧 selector](../../crates/nyro-core/src/router/selector.rs)支持 weighted、priority、cooldown、latency。新版组合优先级和权重，没有 cooldown/latency 选择策略，健康和重试配置也不同。 | 映射保留的旧策略和默认值，包括禁用 backend 和全部不健康场景；保留必要策略或记录已接受的替代方案。被动健康检查的冷却期不是旧 cooldown 选择策略。保留新版重试和流提交安全边界。 |
| G09 HTTP 与网络配置 | 部分／待取舍 | [旧 router](../../crates/nyro-core/src/proxy/server.rs)有 CORS、`/health` 和 `/` 别名、100 MiB JSON 限制；[旧客户端构建](../../crates/nyro-core/src/lib.rs)支持 `use_proxy`、`proxy_url`、`proxy_force_http1`。新根入口有 `/healthz`、`/readyz`、可配置边界，无 CORS，driver 禁用代理和重定向。 | 核对已部署浏览器来源、探针、请求大小和显式出口代理需求，为保留项提供受限配置；说明有意变化的默认值、Header、query、错误行为。不为模仿旧默认值而恢复环境代理或宽泛 CORS。 |

### Provider 清单

协议 kind 与厂商身份分开。基础端点兼容 OpenAI Chat 的厂商可能已经能用 `kind: openai` 接入；没有厂商同名模块，本身不构成缺失。

| 旧版分组 | 新版覆盖／仍需证据 |
|---|---|
| OpenAI API Key、Anthropic API Key、Gemini API Key | 基础静态密钥路径已有；字段、原生保真、beta Header 和完整客户端会话仍需 G02–G04。旧 `google` 目录是历史命名，新协议命名保持 Gemini。 |
| DeepSeek、MiniMax、Moonshot AI、NVIDIA、OpenRouter、xAI、Z.ai、Zhipu AI、custom | [旧 inventory](../../crates/nyro-core/src/provider/mod.rs)与公共 pipeline 同时包含协议处理和厂商／通道元数据。逐个保留通道核对配置端点、认证、录制输出语义；不按厂商拆 crate，也不因 kind 相同就宣布认证完成。 |
| Ollama | 兼容端点可使用通用 driver；[能力探测](../../crates/nyro-core/src/provider/ollama/capabilities.rs)、模型发现、预设属于独立的控制面缺口。 |
| OpenAI Codex 账号通道 | [旧 OAuth driver](../../crates/nyro-core/src/auth/drivers/openai.rs)处理刷新、账号 Header、URL 绑定；旧 Responses 转换还有流／token 参数假设。静态 `api: responses` 不等价。Codex 作为下游客户端的测试与该上游账号通道分开。 |
| Anthropic Claude Code 账号通道 | [旧 OAuth driver](../../crates/nyro-core/src/auth/drivers/claude.rs)构造 Bearer/beta/version Header 并禁用默认 API Key 认证。新版 Anthropic driver 使用 `x-api-key`，把 OAuth token 填进去不等价。Claude Code 客户端兼容与账号授权分开。 |
| Vertex AI | [旧适配器](../../crates/nyro-core/src/provider/vertexai/mod.rs)获取服务账号 token，构造 project/location/publisher URL。新 Gemini 静态密钥 URL 构造不等价。 |

## 3. 尚未完成的产品迁移

| ID | 剩余工作 | 归属与验收 |
|---|---|---|
| G10 控制面与存储 | 根 [CLI](../../src/main.rs)只有 `proxy`；旧[管理服务](../../crates/nyro-core/src/admin/mod.rs)、[HTTP API](../../src-server/src/admin_routes.rs)、仓储和 WebUI 仍使用旧 Gateway。 | 实现 `nyro serve` 时引入 `nyro-control`：Provider／模型／密钥／设置管理、SQLite/Postgres 事务与 schema／数据兼容、配置校验发布、管理认证和 WebUI。发布失败不能默默将已编辑数据宣称为活动配置；数据库访问不进入内核和请求执行。 |
| G11 配置与部署兼容 | [旧 standalone YAML](../../src-server/src/yaml_config.rs)不同于 `nyro-config`；[旧 Server](../../src-server/src/main.rs)有 all/proxy/admin 模式及数据库 epoch 轮询；SIGHUP 只是本地文件来源。 | 提供明确的格式／CLI 迁移说明或工具，证明文件／控制面支持的等价快照行为一致。若保留分离或多副本部署，落实配置交付、就绪和恢复契约；本地 SIGHUP 不等价替代。 |
| G12 持久化观测与统计 | 旧[日志存储契约](../../crates/nyro-core/src/storage/traits.rs)及管理 API 提供日志／统计；新版只有有界结构化事件。 | 消费新请求／尝试语义建立历史查询和统计，明确保留周期、脱敏及可选正文记录；不隐式恢复旧正文日志。缺失用量、输出交付和 quota 扣费分别处理。 |
| G13 工具与发布 | [Tools CLI](../../crates/nyro-tools/src/main.rs)仍有 proxy/record/replay/print-scenarios/dump-schema；[Makefile](../../Makefile)、[CI](../../.github/workflows/ci.yml)、[Server 发布](../../.github/workflows/release-server.yml)仍构建旧产物。 | 保留命令迁入 `nyro tool`，验证支持平台的安装、构建、升级，切换发布产物，再移除 Tauri、旧 Server/Tools 和无消费者代码；保留 schema 生成与数据迁移约束。 |

## 4. 有意变化与未来范围

- 保留严格校验、凭证隔离、有界解析、错误脱敏、强制准入和终态清理。兼容不要求恢复不安全或有损旧行为；错误状态／正文、别名、query 和默认值的变化必须说明客户端预期。
- 五阶段 Hook API 随旧运行时退出。[phase.rs](../../crates/nyro-core/src/plugin/phase.rs)头部仍写“types only”，但调用点已接入。仓库搜索发现阶段注册位于测试，请求／响应扩展示例也存在，没有发现必须重建该框架的内置生产消费者；这不证明仓库外没有消费者。按实际需求提供类型化扩展点，本次切换不要求动态加载 ABI 或全局通用注册表。
- `nyro-protocol`、`nyro-security`、`nyro-limit` 已有独立包边界。新 `nyro-llm` Provider driver 仍是私有具体实现，并非通用外部 driver 扩展 API。稳定插件 API 要有实际下游复用验证，不预建 `nyro-provider` 或全部规划 crate。
- MCP、Gemini Interactions、独立 Image/Audio/Video 操作、托管 Responses 会话／后台任务／内置工具、分布式硬预算，在本次审计中没有确立为旧功能对齐要求。除非保留部署有需求，否则列为后续扩展；这不推迟上文已有 Chat 媒体、凭证和策略差异。
- Tauri、MySQL 不属于已接受的目标产品范围，但退役仍需发布／数据说明；本次审计不删除实现，也不授权破坏性数据库转换。

## 5. 建议实施顺序

| 步骤 | 有界交付 | 完成证据 |
|---|---|---|
| 1（已完成） | G01：在现有 LLM 运行时内补充带授权过滤的 `GET /v1/models`，根程序继续持有代际 lease，不新增 crate。 | 仅公开别名；稳定排序；匿名／绑定／无效／歧义凭证；空集合；成功重载后更新，失败重载后保持；不调用上游或扣 quota。使用现有严格凭证处理，不默默复制旧无效密钥回退。 |
| 2（进行中） | G02–G04：分类旧录制 fixture，按协议逐步补兼容。 | 每个输入标明保留、明确不支持或回退；同协议和跨协议预期分开。先补旧客户端需要的原生字段、推理／工具历史，再补媒体／缓存；验证 JSON、SSE、错误／截断响应和取消。 |
| 3 | G05–G09：补保留的凭证、策略和网络差异。 | 到期与主体窗口测试、路由／配置迁移用例、特殊通道 URL/Header/token 轮换 mock 测试。账号登录／刷新持久化结合步骤 4，不隐式弱化策略。 |
| 4 | G10–G12：最小 `nyro serve`，先 SQLite 和一条管理到发布路径，再补 Postgres 等价与其余管理能力。 | 持久化 → 校验 → 发布 → 新请求使用快照；失败保留活动代际；重启／数据兼容；WebUI/API 与观测一致。可与兼容工作并行推进，单独完成不代表可发布替换。 |
| 5 | G11/G13：部署、工具、发布切换。 | 文件／控制面等价，保留 CLI、录制客户端矩阵、支持平台构建、迁移／恢复说明全部过关后移除旧入口。 |

步骤 1 已完成；步骤 2 已完成 16 份录制样本分类和 OpenAI Chat 原生增量，接下来推进 Anthropic 原生兼容，再核对其他协议与跨协议语义。原生保真和密钥限制语义仍是发布阻塞项；后续每一步可能需要多份聚焦 PR。

复用[现有录制 fixture](../../tests/e2e/fixtures)和[旧回放矩阵](../../tests/e2e/proxy/test_protocol_matrix.py)作为输入，不能把它们当作新代理已通过的证据。旧 harness 依赖旧配置／工具流程，部分断言仅检查文本锚点／字段名；新断言必须核对有效内容顺序、工具 ID／参数、用量、凭证隔离和终态。端到端认证需记录客户端版本，本地 mock 不代表当前 SDK／厂商兼容。

## 6. 原始审计验证

在基线代码上实际执行：

```sh
cargo test -p nyro-core --test protocol_conversion --test passthrough_fidelity --offline
cargo test -p nyro-llm --test openai_codec --test anthropic_codec --test gemini_codec --test responses_codec --offline
```

结果：**旧版 68 项、新版 codec 39 项，共 107 项通过**。它们确认各自现有契约；新版部分测试明确断言拒绝旧测试保留的行为，不是跨版本等价测试。上述原始审计仅变更文档，没有进行真实 Provider 请求、完整客户端会话、数据库迁移、WebUI 测试或发布构建；另行检查文档相对链接和空白格式。


## 7. G01 关闭验证

本轮模型发现实现复用运行时认证、授权和代际所有权，没有新增 crate。实际执行：

```sh
cargo test -p nyro-llm -p nyro --offline
cargo clippy -p nyro-llm -p nyro --all-targets --offline -- -D warnings
cargo build -p nyro --offline
python3 tests/proxy_reload_smoke.py
python3 tests/proxy_smoke.py
```

198 项 Rust 测试、Clippy、构建和两组进程回归通过。新增 4 项模型发现测试先在缺失端点时失败，再通过实现；重载进程回归覆盖模型增删、密钥轮换、主体授权撤销、匿名可见性变化和失败配置保留，以及不访问上游、不消耗推理预算、零尝试观测。G01 关闭不代表 G06 的密钥到期、其他协议兼容或整个迁移已经完成。

## 8. G02 OpenAI Chat 原生增量验证

`native_chat` 默认关闭，只能在 OpenAI Chat Completions Provider 上开启；扩展 JSON 保存在 LLM runtime 私有路径，不进入公共 IR 或内核。JSON／SSE 只修改模型别名和已声明的流式 usage 行为，严格／跨协议 fallback 必须能够表达原始请求。[运行时回归](../../crates/nyro-llm/tests/native_runtime.rs)覆盖字段、凭证、计量、候选筛选、错误／截断／帧限制和释放，配置指纹及健康绑定也纳入验证。

[录制进程回归](../../tests/proxy_native_replay.py)分类仓库中 16 份历史样本（DeepSeek、Zhipu AI，各含 OpenAI Chat 和 Anthropic Messages 的 basic JSON／basic SSE／reasoning／tool-use）：

| 模式与样本 | 实测结果 |
|---|---|
| 默认严格模式：8 份 OpenAI Chat | 均为 HTTP `502`；reasoning、厂商 usage 或响应扩展未被严格 codec 接受。 |
| 默认严格模式：8 份 Anthropic Messages | 2 份 reasoning 请求在访问上游前 `400`；4 份响应 `502`；Zhipu basic/tool SSE 为 `200` 后首个 data event 交付，再因不支持的语义断流，没有完成终态。 |
| 原生模式：8 份 OpenAI Chat | 全部 `200` 且完整终止；完整比较发往上游的请求 JSON 和返回的 JSON／SSE data 序列，仅允许模型重写及约定的 usage 行为。 |
| 显式 `native_chat: false` | 全部 16 份样本的状态、错误、完整性和上游调用次数与省略配置一致。 |

实际执行：

```sh
cargo test -p nyro-llm -p nyro-config -p nyro --offline
cargo clippy -p nyro-llm -p nyro-config -p nyro --all-targets --offline -- -D warnings
cargo check --workspace --exclude nyro-desktop --offline
cargo build -p nyro --offline
python3 tests/proxy_native_replay.py
python3 tests/proxy_smoke.py
python3 tests/proxy_reload_smoke.py
```

228 项 Rust 测试、Clippy、非桌面 workspace 检查、构建和三组进程回归通过；格式与文档相对链接检查通过。新增能力及审查发现的响应结构／终止符问题均有先失败再通过的回归证据。尚无 Gemini／Responses 录制样本或真实厂商／完整 SDK 会话验证，G02 仍为部分完成。
