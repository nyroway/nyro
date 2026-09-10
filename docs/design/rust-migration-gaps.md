# Rust 迁移差异审计

审计基线：`d943c174`（2026-09-09，含 PR #323）。后续关闭状态：G01 已由 PR #324 补齐；G02 的 OpenAI Chat／Anthropic Messages／Gemini／无状态 Responses 原生增量已由 PR #325–#328 合并，G04 的工具历史／结果与 Schema 增量已由 PR #329–#331 合并，G03 用户图片输入、缓存用量及官方缓存控制已由 PR #332–#334 合并，本轮在 `088caedd` 上推进 OpenAI 严格内容断点，其余差异继续跟踪。本文核对仓库实现和测试，不代表真实厂商或 SDK 兼容认证。[架构文档](architecture.md)仍是目标设计，[实验性代理指南](../standalone/rust-proxy_CN.md)说明当前支持范围。

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
| G02 同协议保真 | 部分 | [旧调度器](../../crates/nyro-core/src/proxy/dispatcher/mod.rs)按条件选择 Native 模式；[请求构建](../../crates/nyro-core/src/provider/common/pipeline.rs)和[非流式响应](../../crates/nyro-core/src/proxy/dispatcher/non_stream.rs)可跳过 IR 往返。[新运行时](../../crates/nyro-llm/src/runtime/native.rs)通过 Provider `native_chat: true` 保留匹配的 OpenAI Chat／无状态 Responses／Anthropic Messages／单候选 Gemini 请求、JSON 响应及 SSE 扩展；默认仍严格转换。 | OpenAI Chat／无状态 Responses／Anthropic Messages 和单候选 Gemini 的别名路由、usage、凭证隔离、重试兼容筛选、有界解析及流终态已覆盖；多候选／独立工具提示词计量和完整客户端仍待验收；Responses 已补无状态原生 mock 验证。Gemini 原生保留上游 modelVersion。只保留 JSON 字段，不承诺字节或任意 Header 透传；跨协议原始请求仍须通过来源严格 codec。 |
| G03 推理、缓存与媒体语义 | 部分 | [旧转换测试](../../crates/nyro-core/tests/protocol_conversion.rs)覆盖 thinking／签名回放、`reasoning_content`、think-tag 归一化和 Gemini `fileData`。[新版限制](../standalone/rust-proxy_CN.md)的严格转换路径仍拒绝未支持的 Chat 内容块、缓存控制扩展和 Responses reasoning items；OpenAI Chat／Responses／Anthropic Messages／Gemini 原生模式保留这些 JSON 字段；新[用户图片回归](../../crates/nyro-llm/tests/image_codec.rs)补充四格式内嵌 PNG／JPEG／WebP 及三格式 URL 引用转换，保持 detail／格式／角色边界。新增[缓存用量回归](../../crates/nyro-llm/tests/cache_usage_codec.rs)覆盖缓存读取转换、Anthropic 写入／TTL 保留与拒绝边界、总量及流式累计校验。新增[请求缓存控制回归](../../crates/nyro-llm/tests/cache_control_codec.rs)覆盖 OpenAI 当前 options／兼容 retention 及不兼容目标边界，原生路径补充缓存放置、TTL 和资源引用重试证据。新增[严格断点回归](../../crates/nyro-llm/tests/cache_breakpoint_codec.rs)覆盖 OpenAI 文本／用户图片及工具结果的断点保留、输入／输出边界和候选过滤；Anthropic 严格断点、推理、其他未实现用量扩展及其余媒体仍待处理。 | 为保留客户端建立字段／场景矩阵，保留可表达语义，明确拒绝不可表达的跨协议转换。OpenAI Chat 已有图片／音频类型字段，不能笼统说所有多模态都缺失；新增独立 Image/Audio/Video 操作另算范围。 |
| G04 工具历史与 Schema 处理 | 部分／待取舍 | 旧转换测试包含合成调用、重复 ID 修复、丢弃中间文本／孤立调用、Gemini Schema 裁剪。新 [Anthropic](../../crates/nyro-llm/tests/anthropic_codec.rs)／[Gemini](../../crates/nyro-llm/tests/gemini_codec.rs) encoder 保留完整结果批次及后续文本，拒绝缺失／重复／交错批次；Anthropic 显式错误、空结果、[Responses](../../crates/nyro-llm/tests/responses_codec.rs) 文本块数组及 Gemini ID／顺序已补充。多块结果到 Gemini、跨协议错误标志映射和媒体结果明确拒绝。[Schema 回归](../../crates/nyro-llm/tests/tool_schema_codec.rs)已覆盖 JSON Schema 对象保留、Gemini 计数／类型／嵌套方言转换及 strict 目标筛选；不裁剪引用或约束。完整客户端／厂商验收仍待后续。 | 回放并行调用、交错文本、工具结果和 Schema，保留合法客户端历史；不自动迁移虚构调用或丢内容的处理。区分有意拒绝和兼容回退。 |
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

步骤 1 已完成；步骤 2 已完成 16 份录制样本分类、OpenAI Chat／Anthropic Messages 原生增量及单候选 Gemini mock 验证，无状态 Responses 原生 mock 验证也已完成；PR #329 补充四种入口到 Anthropic 的完整并行工具结果历史转换，PR #330 补充工具错误、空／分块文本结果及 Gemini ID／批次语义，PR #331 补充函数 Schema 保留、转换和拒绝边界；PR #332 补充用户图片输入，PR #333 补充缓存用量计量，PR #334 补充按官方定义的缓存控制，本轮补充 OpenAI 文本／用户图片的严格断点转换；跨协议推理、Anthropic 严格断点和真实会话仍继续跟踪。原生保真和密钥限制语义仍是发布阻塞项；后续每一步可能需要多份聚焦 PR。

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

在 PR #325 的这一轮验证中，`native_chat` 默认关闭，只能在 OpenAI Chat Completions Provider 上开启；扩展 JSON 保存在 LLM runtime 私有路径，不进入公共 IR 或内核。JSON／SSE 只修改模型别名和已声明的流式 usage 行为，严格／跨协议 fallback 必须能够表达原始请求。[运行时回归](../../crates/nyro-llm/tests/native_runtime.rs)覆盖字段、凭证、计量、候选筛选、错误／截断／帧限制和释放，配置指纹及健康绑定也纳入验证。

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

## 9. G02 Anthropic Messages 原生增量验证

本轮扩展同一 `native_chat` 开关，仅对匹配的 Anthropic Messages 入口／上游生效。协议校验分别位于私有 [OpenAI](../../crates/nyro-llm/src/runtime/native/openai.rs) 和 [Anthropic](../../crates/nyro-llm/src/runtime/native/anthropic.rs) 路径，共享原有请求生命周期，没有新增 crate、公共 IR 扩展字段或内核业务逻辑。

[专项运行时测试](../../crates/nyro-llm/tests/native_anthropic_runtime.rs)覆盖 thinking／签名／redacted thinking 与工具历史请求、JSON 内容块、SSE event／data、厂商与缓存扩展保留，以及凭证替换、严格／跨协议 fallback、错误脱敏和释放。Anthropic 原生计量合计普通输入、缓存写入与缓存读取，保留但不重复计入嵌套缓存明细；流式累计快照只更新计数。缺失 `message_delta.output_tokens` 与显式零分别处理；显式清空停止原因后不能成功结算，直到再次报告有效原因。

16 份历史录制样本现在全部通过原生回放（8 份 OpenAI Chat、8 份 Anthropic Messages）。Anthropic 比较完整请求 JSON、响应 JSON、SSE event 名及 data 序列，只改写请求 model 与响应／message_start 中的 model。默认严格模式及显式 `false` 的 16 份分类仍一致：12 份 `502`、2 份在访问上游前 `400`、2 份 `200` 后断流；此结果是边界基线，不应解释为默认模式已兼容这些录制响应。

实际执行：

```sh
cargo test -p nyro-llm -p nyro-config -p nyro --offline
cargo test -p nyro-llm --test native_anthropic_runtime --offline
cargo clippy -p nyro-llm -p nyro-config -p nyro --all-targets --offline -- -D warnings
cargo check --workspace --exclude nyro-desktop --offline
cargo build -p nyro --offline
python3 tests/proxy_native_replay.py
python3 tests/proxy_smoke.py
python3 tests/proxy_reload_smoke.py
```

238 项 Rust 测试通过，其中新增 Anthropic 专项 10 项；最终补充的签名工具历史与嵌套缓存明细样本也通过专项复测。Clippy、非桌面 workspace 检查、构建、三组进程回归、格式及文档链接检查通过。配置和新路径先验证失败再实现；独立审查发现的缺失输出用量、停止原因清空问题均补齐失败回归并修复。

在 Anthropic 增量结束时，G02 仍为部分完成：Gemini／Responses 原生保真、真实厂商及完整 SDK 会话尚待验收。Anthropic beta Header、OAuth／账号通道、跨协议 thinking／媒体／缓存映射并未随原生 JSON 保留自动完成；这些边界在双语代理指南中说明。

## 10. G02 Gemini generateContent 原生增量验证

本轮将 `native_chat` 扩展到匹配的 Gemini generateContent／streamGenerateContent 入口和上游，协议检查位于私有 [Gemini 原生路径](../../crates/nyro-llm/src/runtime/native/gemini.rs)。公开别名和流式动作由 URL 决定，请求体中的 `model`／`stream` 明确拒绝；上游 `modelVersion` 是版本元数据，原样保留。没有新增 crate、公共 IR 扩展字段或内核业务逻辑，默认严格模式及跨协议转换边界保持不变。

[12 项专项运行时测试](../../crates/nyro-llm/tests/native_gemini_runtime.rs)覆盖请求／JSON／SSE 字段保留、thinking 与签名、工具历史、媒体与缓存、安全阻断、严格 fallback、凭证隔离、异常计量、截断和释放。可选非计量字段的 `null` 按未设置处理且原样保留；必需计量字段仍严格检查。缓存 token 属于输入子集，不重复相加；候选输出与思考 token 合计为输出，必须与总量一致。流必须到达终态并正常 EOF 才能完成，终态后的 usage 帧仍计量和交付；曾收到早期 usage 时，必须在终态或之后收到完整快照，避免提前按零输出结算。

新增[真实进程 mock 回归](../../tests/proxy_gemini_native_smoke.py)核对原生请求／响应 JSON 和 SSE data、URL 别名、静态上游密钥、阻断响应、尾部计量及 quota、错误拒绝和默认严格行为。这里使用本地合成 Gemini 响应，尚无 Gemini 录制样本或真实厂商／完整 SDK 会话认证。原有 16 份 OpenAI Chat／Anthropic Messages 录制样本全部通过精确原生回放，严格模式分类和显式 `false` 基线保持一致。

实际执行：

```sh
cargo test -p nyro-llm --test native_gemini_runtime --offline
cargo test -p nyro-llm -p nyro-config -p nyro --offline
cargo clippy -p nyro-llm -p nyro-config -p nyro --all-targets --offline -- -D warnings
cargo check --workspace --exclude nyro-desktop --offline
cargo build -p nyro --offline
python3 tests/proxy_gemini_native_smoke.py
python3 tests/proxy_native_replay.py
python3 tests/proxy_smoke.py
python3 tests/proxy_reload_smoke.py
```

250 项 Rust 测试、Clippy、非桌面 workspace 检查、构建和四组进程回归通过。配置、新协议路径、早期 usage 结算及审查发现的可选 `null` 兼容问题均有先失败再通过的回归证据。

Gemini 增量只支持单候选，非零 `toolUsePromptTokenCount` 的独立工具提示词计量明确拒绝。在 Gemini 增量结束时，G02 仍为部分完成：Responses 原生保真、Gemini 多候选／工具提示词计量、真实客户端验收及其余协议语义仍需后续增量。

## 11. G02 无状态 Responses 原生增量验证

本轮将现有 `native_chat` 开关扩展到 `kind: openai`、`api: responses` 的匹配入口／上游，协议校验位于私有 [Responses 原生路径](../../crates/nyro-llm/src/runtime/native/responses.rs)。请求 model 替换为上游模型，响应及生命周期快照中的 model 恢复公开别名；省略／null 的 `store` 归一化为 `false`。其余受支持的原生 JSON 字段保留，包括 reasoning／加密历史、assistant phase、函数历史、媒体内容块、annotations 和厂商扩展。不新增 crate、公共 IR 扩展字段或内核业务逻辑，跨格式 fallback 仍需原始请求通过严格来源 codec。

[11 项专项运行时测试](../../crates/nyro-llm/tests/native_responses_runtime.rs)覆盖 JSON／SSE 保真、静态上游凭证隔离、严格／跨格式候选筛选与 fallback、无状态限制、completed／incomplete、错误脱敏、不重试、预算和释放。用量存在时要求输入、输出、总量完整且一致；缓存／推理详情是各自总量的子集，不重复相加。SSE 从 created 开始，序号连续，生命周期身份一致，只有终态 usage 参与结算；正常 EOF 才完成，缺失 usage 使用预留回退。Item 和 delta 内容保持不透明，本轮没有新增完整 item 重建或跨协议语义校验。

独立审查发现 `instructions` 搭配空 input 数组在严格模式有效、原生模式被拒绝的兼容差异。已先通过严格／原生对照测试复现，再修正原生输入校验；同时验证早期 usage 不能替代缺失的终态用量。

新增[真实进程 mock 回归](../../tests/proxy_responses_native_smoke.py)完整比较请求／响应 JSON、命名及仅 data 的 SSE，覆盖无状态字段、别名、计量／quota、异常响应／事件和认证。它使用本地合成 Responses 响应，不能视为录制流量、真实厂商、完整 SDK 或 Codex CLI 认证；原有 16 份 OpenAI Chat／Anthropic Messages 录制回放和 Gemini mock 回归继续保持独立。

实际执行：

```sh
cargo test -p nyro-llm --test native_responses_runtime --offline
cargo test -p nyro-llm -p nyro-config -p nyro --offline
cargo clippy -p nyro-llm -p nyro-config -p nyro --all-targets --offline -- -D warnings
cargo check --workspace --exclude nyro-desktop --offline
cargo build -p nyro --offline
python3 tests/proxy_responses_native_smoke.py
python3 tests/proxy_gemini_native_smoke.py
python3 tests/proxy_native_replay.py
python3 tests/proxy_smoke.py
python3 tests/proxy_reload_smoke.py
```

261 项 Rust 测试（含 11 项 Responses 专项）、Clippy、非桌面 workspace 检查、构建和五组进程回归通过。原有 16 份录制样本全部通过原生精确回放，默认严格／显式 false 分类保持一致；格式和文档链接检查通过。

G02 仍为部分完成：Gemini 多候选／独立工具提示词计量、真实客户端会话和其余保留语义尚需后续验收。Responses 托管状态、item 引用、后台任务、内置工具和账号通道未随原生字段保留自动实现；在 Responses 增量结束时，后续方向为按 G02–G04 的保留客户端场景核对跨协议推理／工具历史差异。

## 12. G04 Anthropic 并行工具结果历史增量验证

核对现有四份 tool-use 录制样本后确认：它们的请求均只有一条 user 消息，验证的是首轮工具调用输出，不能作为完整多轮工具历史的验收证据。旧[转换测试](../../crates/nyro-core/tests/protocol_conversion.rs)包含并行调用和工具结果历史，但其中合成调用、丢弃中间文本及重排交错历史的做法不自动成为迁移目标。

本轮修复严格 [Anthropic encoder](../../crates/nyro-llm/src/codec/anthropic.rs)将并行结果拆成多条 user 消息的问题：一个 assistant 调用批次的全部结果合并到紧随其后的一条 user 消息，结果原始顺序、ID 对应、工具名称／对象参数和后续用户文本保留。结果可以按与调用不同的顺序返回。批次内重复调用 ID、缺失／未知／重复结果或批次未完成时插入其他消息，使该目标 backend 在网络尝试前不再符合条件；没有可表达的候选时返回 `400`。不合成调用、不删除文本、不重排历史。匹配的原生路径保持既有契约，不新增全局工具历史规范化或 IR／内核能力。

新增 5 项回归：[codec 测试](../../crates/nyro-llm/tests/anthropic_codec.rs)先复现错误分组和漏校验，再验证精确输出、反向解码再编码及连续两个工具批次；[HTTP 矩阵](../../crates/nyro-llm/tests/protocol_matrix.rs)覆盖 OpenAI Chat／Responses／Anthropic／Gemini 四种入口到 Anthropic 的 8 组 JSON／SSE 成功路径，并核对完整上游历史、模型别名和凭证隔离，以及四种入口不完整结果在访问上游前被拒绝、并发许可释放。独立审查未发现阻塞问题。

实际执行：

```sh
cargo test -p nyro-llm --test anthropic_codec --test protocol_matrix --offline
cargo test -p nyro-llm -p nyro-config -p nyro --offline
cargo clippy -p nyro-llm -p nyro-config -p nyro --all-targets --offline -- -D warnings
cargo check --workspace --exclude nyro-desktop --offline
cargo build -p nyro --offline
python3 tests/proxy_native_replay.py
python3 tests/proxy_gemini_native_smoke.py
python3 tests/proxy_responses_native_smoke.py
python3 tests/proxy_smoke.py
python3 tests/proxy_reload_smoke.py
```

266 项 Rust 测试（本轮新增 5 项）、Clippy、非桌面 workspace 检查、构建和五组进程回归通过。16 份历史样本的原生回放全部通过，默认严格／显式 false 分类保持一致；格式及 88 个文档相对链接检查通过。

PR #329 结束时，G04 仍为部分完成：该增量只处理严格转换到 Anthropic 的完整相邻结果批次，尚未补齐任意交错工具历史、工具错误标记、媒体结果或 Schema 处理。后续状态见第 13 节。G03 的跨协议推理／签名映射和 G02 的真实厂商／客户端会话验收仍待后续增量；本轮没有使用真实厂商密钥。

## 13. G04 工具历史与结果语义增量

本轮合并处理共享同一转换链的结果状态、文本块、ID 与历史顺序，不新增 crate 或依赖，不修改 kernel、原生转发或 Schema。

- IR 增加仅用于工具结果的 `Message.tool_error`，默认 `false` 并省略默认值。Anthropic `is_error:true` 可在严格路径中往返；无法无损表达它的 OpenAI Chat／Responses／Gemini backend 在发往上游前被排除，不降级为普通文本。
- Anthropic 接受省略、空数组及空字符串结果，并保留文本块边界。Responses 接受字符串或仅含 `input_text` 的结果数组，包括空数组；不再拼接多块文本。媒体及其他结果块仍明确拒绝。
- Gemini 保留对象业务数据，包括名为 `error` 的键，不据此推断工具失败。缺失结果 ID 仅在函数名唯一匹配 pending 调用时解析；显式 ID 必须匹配函数名；缺失调用 ID 的生成避开历史中的全部显式 ID。
- Gemini 解码按原顺序保留用户文本／结果混排。编码采用完整相邻结果批次，合并后续用户文本；缺失、重复、未知或被文本打断的批次被拒绝。不会合成调用、删除文本或修复性重排；多文本块结果也被拒绝，避免转成单对象时丢失块边界。

[Anthropic codec](../../crates/nyro-llm/tests/anthropic_codec.rs)、[Gemini codec](../../crates/nyro-llm/tests/gemini_codec.rs) 和 [Responses codec](../../crates/nyro-llm/tests/responses_codec.rs) 新增回归覆盖上述边界。[运行时矩阵](../../crates/nyro-llm/tests/responses_runtime.rs) 覆盖 48 组文本块／空结果的 JSON／SSE 路径、8 组显式错误目标选择及四种目标的 Gemini 对象保留，核对精确上游结果、发送前拒绝和并发许可释放。独立代码审查未发现阻塞问题。

实际执行：

```sh
cargo test -p nyro-protocol -p nyro-llm -p nyro-config -p nyro --offline
cargo clippy -p nyro-protocol -p nyro-llm -p nyro-config -p nyro --all-targets --offline -- -D warnings
cargo check --workspace --exclude nyro-desktop --offline
cargo build -p nyro --offline
python3 tests/proxy_native_replay.py
python3 tests/proxy_gemini_native_smoke.py
python3 tests/proxy_responses_native_smoke.py
python3 tests/proxy_smoke.py
python3 tests/proxy_reload_smoke.py
```

286 项 Rust 测试通过（相比上一增量新增 17 项，另将 `nyro-protocol` 的既有 3 项纳入本轮命令），Clippy、非桌面 workspace 检查、构建和五组进程回归通过。16 份录制样本的原生精确回放、默认严格／显式 false 分类均通过；格式、diff 和 92 个文档相对链接检查通过。`docs/superpowers/` 继续保持忽略，不纳入 Git。

PR #330 结束时，G04 仍为部分完成：下一开发项为 Schema 兼容与拒绝边界，后续状态见第 14 节；媒体结果和无法保留的交错顺序继续明确拒绝。G03 跨协议推理／签名及 G02 真实厂商／完整客户端会话仍未完成；本轮未使用真实厂商密钥。验证结果在本节记录，整体迁移完成后删除本文，不归档。

## 14. G04 函数 Schema 兼容增量

核对[旧 Gemini Schema 裁剪](../../crates/nyro-core/src/protocol/codec/google/gemini/encoder.rs)后，本轮保留 JSON Schema 对象和引用，不迁移删除 `$schema`、`additionalProperties`、`$ref`、`definitions`／`$defs` 的行为。原生 Gemini `parameters` 方言的计数字段是 int64 字符串，原先直接送入 JSON Schema 会留下错误字段类型；该问题由明确转换修复，不通过裁剪消除约束。

- OpenAI Chat、Responses、Anthropic 与 Gemini `parametersJsonSchema` 路径保留 JSON Schema 对象；共享校验检查非空白函数名和参数对象形状，也覆盖直接调用公共 encoder 的 IR 请求。不添加通用 JSON Schema 校验器、引用解析器或工具参数执行校验。
- OpenAI Chat 显式 `strict:false` 可发送到 Anthropic／Gemini；`strict:true` 仍只在当前可保留它的 OpenAI Chat／Responses 目标之间转换。Responses 入口显式 strict 要求不变，出站始终设置 strict，不因默认值变化擅自加强约束。
- Gemini 原生 Schema 在 properties/items/anyOf 位置递归转换，保留字面 default 数据，转换已知类型及非负 int64 计数，校验受支持字段的形状。拒绝未知字段、错误类型，以及 nullable 与 enum/anyOf 的含糊组合；propertyOrdering、example、原生方言的引用和 additionalProperties 不被静默删除，JSON Schema 可改用 parametersJsonSchema。详细支持表见[代理指南](../standalone/rust-proxy_CN.md)。

新增 5 项 [codec 测试](../../crates/nyro-llm/tests/tool_schema_codec.rs)：4 项先复现缺失校验、strict:false 误拒绝和 Schema 转换问题，再通过修复；另一项固定已有 JSON Schema／引用／字面数据保留契约。新增 2 项[运行时矩阵](../../crates/nyro-llm/tests/responses_runtime.rs)覆盖 48 组 JSON／SSE Schema／strict 组合、8 组 Gemini 方言转换及 12 组发送前拒绝，并核对实际上游 Schema、并发许可释放和成功响应。

独立代码审查未发现阻塞问题。实际执行：

```sh
cargo test -p nyro-llm --test tool_schema_codec --test openai_codec --test anthropic_codec --test gemini_codec --test responses_codec --offline
cargo test -p nyro-llm --test responses_runtime --offline
cargo test -p nyro-protocol -p nyro-llm -p nyro-config -p nyro --offline
cargo clippy -p nyro-protocol -p nyro-llm -p nyro-config -p nyro --all-targets --offline -- -D warnings
cargo check --workspace --exclude nyro-desktop --offline
cargo build -p nyro --offline
python3 tests/proxy_native_replay.py
python3 tests/proxy_gemini_native_smoke.py
python3 tests/proxy_responses_native_smoke.py
python3 tests/proxy_smoke.py
python3 tests/proxy_reload_smoke.py
```

293 项 Rust 测试（本轮新增 7 项）、Clippy、非桌面 workspace 检查、构建和五组进程回归通过。16 份录制样本的原生精确回放及默认严格／显式 false 分类通过；格式、diff 和 97 个文档相对链接检查通过。`docs/superpowers/` 继续忽略，不进入 Git。

G04 的当前文本／客户端函数子集已具备历史和 Schema 回归证据；仍不据此宣称全部厂商／客户端兼容完成。媒体工具结果、不可保留的顺序、含糊 Schema 映射继续明确拒绝，完整客户端验收与 G02–G03 协同推进。没有使用真实厂商密钥，没有新增 crate、依赖或内核职责。

## 15. G03 用户图片输入增量

核对旧[推理／媒体转换测试](../../crates/nyro-core/tests/protocol_conversion.rs)及 16 份现有录制样本：旧测试包括 thinking／签名、reasoning_content、think-tag 处理和 Gemini fileData；录制请求不包含图片，不能据此证明图片转换兼容。推理签名和缓存控制需要各自的语义边界，本轮先复用既有图片 IR 补充有明确协议对应关系的用户输入。

- 四种入口／目标支持 PNG、JPEG、WebP 内嵌图片；OpenAI Chat、Responses、Anthropic 另支持 GIF 及 HTTP(S) 图片引用。Gemini 不接受 GIF 或远程 URL 转换，不下载内容或合成 fileData。
- 保留图文顺序、URL、声明 MIME 与 base64 数据。detail 的 auto／缺省使用目标默认行为，low／high／original 只在 OpenAI Chat／Responses 间保留；不猜测与 Gemini mediaResolution 或 Anthropic 变换选项的对应关系。
- 仅允许 user 消息图片。工具结果、system／developer／assistant、厂商文件 ID、fileData、HEIC／HEIF 和缓存等扩展仍明确拒绝。新 wire 图片类型不放宽 Anthropic／Gemini JSON 或流式图片输出；本轮不新增图片生成／输出转换。
- `nyro-protocol` 补充基础 wire 形状，`nyro-llm` 复用 `ContentPart::ImageUrl` 并增加私有图片来源辅助逻辑，直接依赖仓库已锁定的 base64 0.22 版本。公共 IR、内核、Provider 和原生转发没有新增职责。不校验像素、MIME 与实际字节的一致性或模型能力，也不下载／转码；请求体限制沿用现有运行时边界。

新增 6 项 [codec 回归](../../crates/nyro-llm/tests/image_codec.rs)，其中 4 项先复现图片拒绝及校验缺失；另覆盖新增 wire 变体不能被误当作生成文本、以及 MIME／GIF 目标边界。新增 2 项[运行时回归](../../crates/nyro-llm/tests/responses_runtime.rs)，覆盖 56 组内嵌／URL 图片 JSON／SSE 转换与拒绝、32 组 detail 保留／目标筛选，核对实际上游内容和许可释放。旧“所有图片均拒绝”的断言调整为仍不支持的文件 ID，工具图片拒绝继续保留。独立审查未发现阻塞问题。

实际执行：

```sh
cargo test -p nyro-llm --test image_codec --test responses_runtime --offline
cargo test -p nyro-protocol -p nyro-llm -p nyro-config -p nyro --offline
cargo clippy -p nyro-protocol -p nyro-llm -p nyro-config -p nyro --all-targets --offline -- -D warnings
cargo check --workspace --exclude nyro-desktop --offline
cargo build -p nyro --offline
python3 tests/proxy_native_replay.py
python3 tests/proxy_gemini_native_smoke.py
python3 tests/proxy_responses_native_smoke.py
python3 tests/proxy_smoke.py
python3 tests/proxy_reload_smoke.py
```

301 项 Rust 测试（本轮新增 8 项）、Clippy、非桌面 workspace 检查、构建和五组进程回归通过。16 份录制样本的原生精确回放与默认严格／显式 false 分类通过；格式、diff 和 101 个文档相对链接检查通过。`docs/superpowers/` 继续忽略，不进入 Git。

PR #332 结束时，G03 仍为部分完成：推理／签名、缓存控制及计量、其余 Chat 媒体和完整客户端会话仍待后续；缓存计量进展见第 16 节。这里使用本地 mock 与一张 1×1 PNG，没有使用真实厂商密钥，不代表视觉推理质量或厂商认证。整体迁移完成后删除本文，不做归档。

## 16. G03 缓存用量计量增量

Anthropic 普通输入不包含缓存读取／写入；本轮将三者合计为 IR 输入和 quota／观测输入，输出只加一次。缓存读取沿用既有 cached-token 细分，可在四种输出格式间表达；非零写入及五分钟／一小时 TTL 明细使用可选类型化 `Usage.cache_creation`，仅 Anthropic 严格输出能保留。其余目标明确拒绝，包括 OpenAI 关闭流式 usage 的情况，避免静默丢失明细。零写入及有效零 TTL 明细归一化为缺省。

严格 JSON／SSE 校验非负整数、溢出、TTL 合计与分项递减；流式省略字段保留已有计数。原生 Anthropic 同步校验已知 TTL 明细，同时保留原始扩展 JSON。失败或无法编码输出时，已知消耗仍按既有失败规则参与结算，不重试、不伪造成功终态，释放并发许可。

新增 5 项 [codec 回归](../../crates/nyro-llm/tests/cache_usage_codec.rs)，其中 4 项先复现旧路径拒绝缓存字段，再通过实现；新增 4 项[运行时回归](../../crates/nyro-llm/tests/native_anthropic_runtime.rs)，覆盖严格 JSON／SSE 精确用量、四种输出的只读缓存、写入不兼容输出、严格／原生异常流及预算／释放。已有原生样本补充 TTL 明细和无效结构。独立审查未发现阻塞问题。

实际执行：

```sh
cargo test -p nyro-protocol -p nyro-llm -p nyro-config -p nyro --offline
cargo clippy -p nyro-protocol -p nyro-llm -p nyro-config -p nyro --all-targets --offline -- -D warnings
cargo check --workspace --exclude nyro-desktop --offline
cargo build -p nyro --offline
python3 tests/proxy_native_replay.py
python3 tests/proxy_gemini_native_smoke.py
python3 tests/proxy_responses_native_smoke.py
python3 tests/proxy_smoke.py
python3 tests/proxy_reload_smoke.py
```

310 项 Rust 测试（本轮新增 9 项）、Clippy、非桌面 workspace 检查、构建和五组进程回归通过。16 份录制样本原生精确回放及默认严格／显式 false 分类通过；格式、diff 和 104 个文档相对链接检查通过。`docs/superpowers/` 继续忽略，不进入 Git。

本轮不新增 crate、依赖或内核职责，不实现请求 cache_control 映射、缓存放置、金额折扣或缓存执行服务。G03 仍为部分完成，推理／签名、缓存控制、其余媒体及完整客户端会话继续跟踪。使用本地 mock 和已有录制样本，没有使用真实厂商密钥。整体迁移完成后删除本文，不归档。

## 17. G03 官方缓存控制与放置边界

以当前官方文档确定字段、默认行为与语义。旧 Anthropic decoder 的 `map_cache_control` 将 TTL 固定为五分钟，encoder 则依赖原始请求快照保留断点；不能把这段实现或通用缓存类型的注释当成跨协议转换依据。新实现保持厂商控制独立，不新增统一缓存策略或 crate。

- OpenAI 当前 `prompt_cache_options` 支持可选 mode（implicit／explicit）与 TTL（30m）；Chat／Responses 间保留结构，不注入省略字段。严格转换接受空对象，顶层 null 归一化为缺省；拒绝错误枚举、嵌套 null 及未实现的比较／诊断字段。
- `prompt_cache_retention` 作为兼容字段接受 in_memory／24h，不是新默认值。它与 options TTL 的含义分别为最长保留策略、最短存活时间，不相互改写；两者可独立保留。模型是否支持由上游判断，既有 prompt_cache_key 不改写、不生成。
- Responses 合法 options 回显沿用请求回显的校验／归一化契约，原生模式才能保证原样回显。OpenAI 逐块 breakpoint 仍由原生路径保留；严格转换拒绝，explicit 模式无断点时不合成隐式缓存。
- Anthropic 顶层自动缓存，以及 system／tool／用户图片／文本／工具结果中的位置、顺序和 TTL，在匹配原生路径保持不变。Gemini 已有 cachedContent 引用也原样保留；不在 Nyro 中创建或迁移资源。JSON／SSE 的失败重试排除严格与其他协议候选，原始控制不会静默丢失。
- 独立审查指出，新的 OpenAI 请求选项还要求处理官方 `cache_write_tokens`，否则连合法零写入响应也会被拒绝。已先复现再补齐 Chat／Responses 的 JSON／SSE 字段，复用 IR `Usage.cache_creation` 且不推测 TTL。写入与读取是输入内互斥子集，合计不得超过输入，零写入归一化为缺省。无 TTL 写入量可转换到 OpenAI Chat／Responses／Anthropic；Anthropic TTL 明细不能丢弃，Gemini 非零写入仍明确拒绝。原生 Chat／Responses 同步校验已知写入子集，其他 JSON 扩展原样保留。

新增 6 项 [codec 测试](../../crates/nyro-llm/tests/cache_control_codec.rs)，覆盖两种 OpenAI API 的保留、缺省和拒绝边界及 Responses 回显。新增[请求矩阵](../../crates/nyro-llm/tests/responses_runtime.rs)核对 JSON／SSE 的实际发送字段、合法回显和不兼容目标；新增 [Anthropic](../../crates/nyro-llm/tests/native_anthropic_runtime.rs) 与 [Gemini](../../crates/nyro-llm/tests/native_gemini_runtime.rs) 各一项原生重试回归，使用仅含缓存控制的输入排除其他扩展导致拒绝的假阳性。OpenAI／Responses 既有原生精确保真样本补充当前缓存 options 与内容断点。

额外增加 2 项[缓存用量 codec 回归](../../crates/nyro-llm/tests/cache_usage_codec.rs)，覆盖无 TTL 写入转换、零值、无效数值和子集溢出；新增原生 Chat 无效写入回归，并扩展 Responses 无效 JSON／SSE 样本。严格 HTTP 矩阵使用真实字段形状的缓存读取／写入用量，额外的 quota 测试确认两次各 5 token 消耗刚好耗尽 10 token，关闭 Chat 流式 usage 也不漏计、不重复计量。

最终验证：

```sh
cargo test -p nyro-protocol -p nyro-llm -p nyro-config -p nyro --offline
cargo clippy -p nyro-protocol -p nyro-llm -p nyro-config -p nyro --all-targets --offline -- -D warnings
cargo check --workspace --exclude nyro-desktop --offline
cargo build -p nyro --offline
python3 tests/proxy_native_replay.py
python3 tests/proxy_gemini_native_smoke.py
python3 tests/proxy_responses_native_smoke.py
python3 tests/proxy_smoke.py
python3 tests/proxy_reload_smoke.py
```

323 项 Rust 测试（新增 13 项）、Clippy、非桌面 workspace 检查、构建及五组进程回归通过。16 份原生录制精确回放与默认严格／显式 false 分类保持通过；格式、diff 和 111 个文档相对链接检查通过。独立审查发现的写入用量缺口已补齐，复查未发现新的阻断问题。完整回归另确认：没有写入字段的旧 OpenAI 无效总量仍由既有观测／quota 契约处理，不因本轮添加字段校验而改变行为。

G03 仍为部分完成：严格内容断点、跨协议策略、其他未实现用量扩展、推理／签名和其余媒体继续跟踪。官方来源及用户可用字段矩阵见[代理指南](../standalone/rust-proxy_CN.md#请求缓存控制)。这里没有真实厂商请求或缓存命中验证。整体迁移完成后删除本文，不做归档；`docs/superpowers/` 继续忽略。

## 18. G03 OpenAI 严格内容断点

依照官方定义扩展既有 `nyro-protocol` wire 类型和 `nyro-llm` 内容块，不引入新 crate、依赖或内核职责。文本与用户图片支持 `prompt_cache_breakpoint: {"mode":"explicit"}`，Chat／Responses 间保留内容顺序、断点位置、图片 detail、历史角色和工具结果关联。文本包括 system／developer／user／assistant 历史和工具结果；TTL 继承请求级 options，不注入默认值，不将“四次缓存写入”误用为历史断点个数限制。

带断点的 assistant 历史消息使用 Responses EasyInputMessage 的 input_text 列表，整条消息的文本统一使用输入形状，混合 refusal 明确拒绝；生成结果和 output_text 不接受输入断点。Chat prediction 文本断点在 Chat 内保留。音频／文件断点、Anthropic 严格 cache_control 和跨协议缓存策略仍未实现；Gemini 资源引用继续仅在原生路径保留。

[Codec 回归](../../crates/nyro-llm/tests/cache_breakpoint_codec.rs)覆盖超过四个历史断点、双向转换、marker-only 的全角色目标过滤、无效形状、生成结果边界及 prediction；[HTTP 回归](../../crates/nyro-llm/tests/responses_runtime.rs)覆盖两种入口到四种目标的 JSON／SSE 转发，核对实际发送位置及 TTL 缺省，验证 503 重试跳过 Anthropic／Gemini 而保留断点，并检查准入释放。输入只含断点而无顶层缓存选项，避免已有 options 拒绝掩盖断点丢失。

333 项 Rust 测试（新增 10 项）、受影响 crate 的 Clippy、非桌面 workspace 检查、构建及五组代理进程回归通过；16 份原生录制精确回放及默认严格／显式 false 分类保持通过。格式、diff 和 114 个文档相对链接检查通过。独立审查发现的 prediction 图片断点误接收及 Responses 图片断点 null 问题均先由测试复现后修复，复查无新的代码问题。G03 仍为部分完成，推理／签名、其余 Chat 媒体和完整客户端会话继续跟踪。仅使用本地 mock，没有真实厂商请求或缓存命中验证。整体迁移完成后删除本文，不归档；`docs/superpowers/` 继续忽略。
