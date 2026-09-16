# Rust 迁移差异审计

审计基线：`d943c174`（2026-09-09，含 PR #323）。后续关闭状态：G01 已由 PR #324 补齐；G02 的 OpenAI Chat／Anthropic Messages／Gemini／无状态 Responses 原生增量已由 PR #325–#328 合并，G04 的工具历史／结果与 Schema 增量已由 PR #329–#331 合并，G03 用户图片输入、缓存用量、官方缓存控制、OpenAI 严格断点及 Anthropic 严格缓存控制已由 PR #332–#336 合并，Anthropic 严格推理／签名已由 PR #337 合并，Gemini 严格推理／签名已由 PR #339 合并，Responses 严格推理已由 PR #340 合并，OpenAI effort 映射与推理边界已由 PR #341–#342 合并，有序 IR 基础已由 PR #343 合并，三协议交错输出已由 PR #344 合并，工具结果图片子集已由 PR #345 合并，本地多轮工具会话回归已由 PR #346 合并，G06 文件代理密钥生命周期已由 PR #347 合并，G07 主体 RPM／RPD 请求窗口已由 PR #348 合并，G07 主体 TPM／TPD 预留与结算已由 PR #349 合并，G08 可选选择策略与路由／健康／重试迁移契约已由 PR #350 合并，G09 文件代理出口配置与 HTTP 迁移契约已由 PR #351 合并，G10 首轮最小 SQLite 控制面已由 PR #352 合并，本轮推进 Provider／Model／API Key 实体管理与凭证脱敏查询，其余差异继续跟踪。本文核对仓库实现和测试，不代表真实厂商或 SDK 兼容认证。[架构文档](architecture.md)仍是目标设计，[实验性代理指南](../standalone/rust-proxy_CN.md)和[服务指南](../standalone/rust-serve_CN.md)说明当前支持范围。

**新文件配置 LLM 数据面已具备主要执行和生命周期机制，尚不能替换已发布 Server。** 同名能力不等于契约已迁移：模型 token bucket 不等于 API Key 请求窗口；存在 Responses 端点也不等于兼容 Codex 账号通道。

本文记录现状与建议顺序，不授权下线旧功能、修改用户数据或创建所有目标 crate。移除旧消费者前，每项差异必须有行为与回归证据，或经过明确接受的兼容性变更。

文档生命周期：本文作为临时迁移清单纳入 Git，仅维护这一份中文版本。整体迁移完成、切换验收通过后，将仍需保留的架构与用户升级说明移入正式文档，并在迁移收尾 PR 中删除本文件及其引用，不做归档。

## 1. 已有基础及其范围

| 能力 | 当前证据与边界 |
|---|---|
| 生命周期与代际 | [内核](../../crates/nyro-kernel/README_CN.md)、[装配](../../src/bootstrap.rs)和[重载](../../src/reload.rs)：候选激活、失败清理、lease 和退役；文件模式 Unix 显式 SIGHUP 重载；serve 模式由独立回环管理 API 发布 SQLite 快照，没有远程分发。 |
| 类型化工作负载 | [IR](../../crates/nyro-llm/src/ir/mod.rs)：`Request`/`Response` 配对 Chat 和 Embedding，其他工作负载未实现。 |
| 协议执行 | [矩阵测试](../../crates/nyro-llm/tests/protocol_matrix.rs)覆盖三种 Chat 格式，[Responses 运行时测试](../../crates/nyro-llm/tests/responses_runtime.rs)补充 Responses 组合。覆盖已支持的文本、函数和流，不代表任意厂商字段兼容。 |
| 准入 | [安全包](../../crates/nyro-security/src/lib.rs)、[rate 运行时](../../crates/nyro-llm/tests/rate_runtime.rs)和[quota 运行时](../../crates/nyro-llm/tests/quota_runtime.rs)：凭证认证、模型授权、进程并发、按模型 rate、主体 RPM／RPD 和 TPM／TPD 窗口、累计 token 预留。旧契约差异见 G06–G07。 |
| 路由与健康 | [路由测试](../../crates/nyro-llm/tests/routing_runtime.rs)和[故障转移测试](../../crates/nyro-llm/tests/failover_runtime.rs)：优先级内 weighted／least_recent／latency 选择、有界尝试、被动健康检查；旧策略映射和有意变化见第 31 节。 |
| 交付与观测 | [响应体所有权](../../src/http/body.rs)、[观测](../../crates/nyro-llm/tests/observation_runtime.rs)和[进程测试](../../tests/proxy_smoke.py)：期限、取消、SSE 终止、请求／尝试关联日志及清理。没有持久化请求查询服务。 |
| 共享状态重载 | [重载进程测试](../../tests/proxy_reload_smoke.py)：去重、拒绝变更、持有 SSE 时轮换凭证／路由、rate/quota/health 复用、主体请求窗口保留、FIFO 拒绝及退出。监听／并发和既有策略变更限制仍存在。 |

## 2. 替换旧入口前的数据面差异

“缺失”表示没有发现等价的新路径；“部分”表示已有可运行子集；“待取舍”表示差异确实存在，但照搬旧行为不一定正确。对保留的迁移范围，三类都必须明确处理结果。

| ID | 状态 | 旧实现证据 → 新行为 | 切换前要求 |
|---|---|---|---|
| G01 模型发现 | 已落地 | [旧模型列表](../../crates/nyro-core/src/proxy/handler.rs)的发现能力已由[新运行时](../../crates/nyro-llm/src/runtime.rs)承接；[模型列表测试](../../crates/nyro-llm/tests/models_runtime.rs)与[重载进程测试](../../tests/proxy_reload_smoke.py)覆盖过滤及代际变化。 | 按授权返回稳定排序的公开别名；匿名／绑定／无效／歧义凭证、空列表、成功／失败重载、密钥轮换和预算隔离已覆盖。无效密钥明确拒绝，不沿用旧公开列表回退；密钥到期仍由 G06 跟踪。 |
| G02 同协议保真 | 部分 | [旧调度器](../../crates/nyro-core/src/proxy/dispatcher/mod.rs)按条件选择 Native 模式；[请求构建](../../crates/nyro-core/src/provider/common/pipeline.rs)和[非流式响应](../../crates/nyro-core/src/proxy/dispatcher/non_stream.rs)可跳过 IR 往返。[新运行时](../../crates/nyro-llm/src/runtime/native.rs)通过 Provider `native_chat: true` 保留匹配的 OpenAI Chat／无状态 Responses／Anthropic Messages／单候选 Gemini 请求、JSON 响应及 SSE 扩展；默认仍严格转换。 | OpenAI Chat／无状态 Responses／Anthropic Messages 和单候选 Gemini 的别名路由、usage、凭证隔离、重试兼容筛选、有界解析及流终态已覆盖；多候选／独立工具提示词计量和完整客户端仍待验收；Responses 已补无状态原生 mock 验证，本地三轮会话组合见第 27 节。Gemini 原生保留上游 modelVersion。只保留 JSON 字段，不承诺字节或任意 Header 透传；跨协议原始请求仍须通过来源严格 codec。 |
| G03 推理、缓存与媒体语义 | 部分 | [旧转换测试](../../crates/nyro-core/tests/protocol_conversion.rs)包含 thinking／签名、`reasoning_content`、think-tag 归一化和 Gemini `fileData`。新版已支持 Anthropic、Gemini generateContent、Responses 各自的严格推理配置、历史及 JSON／SSE 保留；已补齐 OpenAI Chat／Responses effort 双向映射与[边界回归](../../crates/nyro-llm/tests/reasoning_boundary_codec.rs)。厂商签名／加密状态不互换，旧标签自动抽取不迁入，兼容扩展交由匹配原生模式；具体范围见[代理指南](../standalone/rust-proxy_CN.md#推理转换与旧行为取舍)。用户图片、缓存用量、OpenAI 与 Anthropic 严格缓存控制／断点已有回归（第 15–22 节）。 | 当前推理转换取舍见第 23 节；有序 IR 基础见第 24 节，三协议交错输出见第 25 节，工具结果图片见第 26 节，本地三轮组合回归见第 27 节；其余 Chat 媒体、未实现缓存／用量扩展及完整客户端验收继续推进。独立 Image/Audio/Video 操作另算范围，不将自动合成签名或丢弃推理当作兼容方案。 |
| G04 工具历史与 Schema 处理 | 部分／待取舍 | 旧转换测试包含合成调用、重复 ID 修复、丢弃中间文本／孤立调用、Gemini Schema 裁剪。新 [Anthropic](../../crates/nyro-llm/tests/anthropic_codec.rs)／[Gemini](../../crates/nyro-llm/tests/gemini_codec.rs) encoder 保留完整结果批次及后续文本，拒绝缺失／重复／交错批次；Anthropic 显式错误、空结果、[Responses](../../crates/nyro-llm/tests/responses_codec.rs) 文本块数组及 Gemini ID／顺序已补充。多块文本结果到 Gemini、跨协议错误标志映射和非图片媒体结果明确拒绝；图片结果新增支持范围见第 26 节。[Schema 回归](../../crates/nyro-llm/tests/tool_schema_codec.rs)已覆盖 JSON Schema 对象保留、Gemini 计数／类型／嵌套方言转换及 strict 目标筛选；不裁剪引用或约束。完整客户端／厂商验收仍待后续。 | 回放并行调用、交错文本、工具结果和 Schema，保留合法客户端历史；不自动迁移虚构调用或丢内容的处理。区分有意拒绝和兼容回退。 |
| G05 Provider 通道与凭证 | 部分 | 旧版有[厂商适配](../../crates/nyro-core/src/provider/mod.rs)、[账号认证 driver](../../crates/nyro-core/src/auth/drivers/mod.rs)和 Vertex 服务账号支持。新 Provider 配置只有协议 kind、可选 OpenAI API、URL 和可选静态 API Key。 | 迁移必要端点、Header 和凭证行为，不向 driver 传递 Gateway 或数据库实体。账号授权、刷新、持久化归控制面；driver 消费已解析凭证。详见下方清单。 |
| G06 API Key 生命周期 | 文件代理已落地 | [安全包](../../crates/nyro-security/src/lib.rs)新增默认启用状态与可选 Unix 秒到期时间，所有入口共用认证时检查；禁用／到期返回通用 `401`，无匿名回退。主体绑定、重复校验和配置指纹保留生命周期语义。 | 精确到期边界、慢上传、所有入口在准入前拒绝、禁用／到期／续期重载与已准入 SSE 保留见第 28 节。控制面管理、旧数据到新快照的发布仍由 G10–G11 跟踪，不据此宣布产品迁移完成。 |
| G07 限制作用域与持久化 | 部分 | 旧授权按 API Key 查询 Minute/Day 窗口的请求／token 数（`rpm/rpd/tpm/tpd`）。新 rate 是模型级 token bucket；已补主体级 RPM／RPD 请求窗口、TPM／TPD 用量预留／结算与组合原子准入；请求边界见第 29 节，token 边界见第 30 节。quota 仍是模型级累计额度。 | 持久化、多副本一致性和旧数据发布仍待推进；请求按逻辑准入，token 按尝试结算加在途预留计量，重启清零。显式迁移策略含义，不静默将 RPM 转成不同桶规则或将 TPM 转成累计额度；也不复制旧日志查询准入的竞争问题。 |
| G08 均衡与重试策略 | 文件代理已落地 | [新运行时](../../crates/nyro-llm/src/runtime.rs)支持最小可用优先级内 weighted（默认）、least_recent 和 latency，按有效 backend 绑定跨代际保留历史。第 31 节记录旧策略映射、采样与重载契约及回归。 | 已接受用每次发起顺序替代旧 cooldown、用单次 2xx 响应头耗时替代旧混合 latency；保留新版重试／2xx 提交安全边界。旧 weighted 统一优先级，确定顺序用不同 priority，显式配置健康与尝试预算；控制面配置发布和旧数据导入仍由 G10–G11 跟踪。 |
| G09 HTTP 与网络配置 | 文件代理已落地 | [Provider driver](../../crates/nyro-llm/src/provider.rs)支持显式 HTTP／HTTPS 出口代理及独立 HTTP/1 配置；直连默认、环境隔离、代理认证、传输绑定历史和重载已覆盖。第 32 节记录旧配置映射与有意变化。 | 保留 CORS 关闭、规范探针路径、显式大小／期限和 Header 凭证；不迁入隐式系统代理或旧别名。跨域白名单尚未实现，需要浏览器部署需求后另行确定；serve 就绪、控制面配置发布与部署切换继续由 G10–G11 跟踪，不据此宣布产品迁移完成。 |

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
| G10 控制面与存储 | 部分完成：根 [serve](../../src/serve.rs) 与 [nyro-control](../../crates/nyro-control/src/lib.rs) 实现专用 SQLite 完整草稿、管理认证、版本冲突、显式发布与重启恢复，并补充 Provider／Model／API Key CRUD、脱敏查询及显式导出；第 33–34 节说明持久化／活动边界和实体契约。旧管理服务、仓储与 WebUI 仍使用旧 Gateway。 | 继续补 Postgres 等价、OAuth、旧数据兼容、持久化预算与 WebUI。当前拒绝旧库，配置含明文凭证，单进程独占；不持久化额度或观测，不支持分布式发布。已提交发布但激活未完成必须报告 pending，不能当作编辑拒绝或已生效。 |
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
| 4（部分完成） | G10–G12：已交付 `nyro serve` 的 SQLite 管理到发布路径及实体 CRUD，继续补 Postgres 等价与其余管理能力。 | 持久化 → 校验 → 发布 → 新请求使用快照；失败保留活动代际；重启／数据兼容；WebUI/API 与观测一致。可与兼容工作并行推进，单独完成不代表可发布替换。 |
| 5 | G11/G13：部署、工具、发布切换。 | 文件／控制面等价，保留 CLI、录制客户端矩阵、支持平台构建、迁移／恢复说明全部过关后移除旧入口。 |

步骤 1 已完成；步骤 2 已完成 16 份录制样本分类、OpenAI Chat／Anthropic Messages 原生增量及单候选 Gemini mock 验证，无状态 Responses 原生 mock 验证也已完成；PR #329 补充四种入口到 Anthropic 的完整并行工具结果历史转换，PR #330 补充工具错误、空／分块文本结果及 Gemini ID／批次语义，PR #331 补充函数 Schema 保留、转换和拒绝边界；PR #332 补充用户图片输入，PR #333 补充缓存用量计量，PR #334 补充按官方定义的缓存控制，PR #335 补充 OpenAI 文本／用户图片的严格断点转换，PR #336 补充 Anthropic 严格缓存控制，PR #337 补充 Anthropic 严格推理配置、历史及 JSON／SSE 签名回放，PR #339 补充 Gemini 严格推理配置、Part／函数签名及流式保留，PR #340 补充 Responses 严格推理；PR #341–#342 补齐 OpenAI effort 映射并明确跨协议签名、兼容扩展与旧标签处理的取舍；PR #343 迁移有序消息项及流式位置，PR #344 补齐三协议交错输出；PR #345 补齐工具结果图片子集；PR #346 补充本地三轮工具会话回归（第 27 节）；本轮先交付步骤 3 中范围集中的 G06（第 28 节），步骤 2 的其余媒体／缓存和真实客户端／厂商会话仍继续跟踪。原生保真和密钥限制语义仍是发布阻塞项；后续每一步可能需要多份聚焦 PR。

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


## 19. G03 Anthropic 严格缓存控制

基于官方 Messages 与 prompt-caching 定义，在既有 crate 内补齐请求顶层自动缓存、system／消息文本、用户图片、工具定义、tool_use、外层 tool_result 及结果内部文本的 `cache_control`。类型只支持 ephemeral 和可选 5m／1h，省略 TTL 保持省略，控制对象 null 归一化为缺省，错误 TTL／未知字段前置拒绝。缓存条数、混合 TTL 顺序、自动断点冲突、模型资格和实际缓存执行由上游负责；不合成或移动断点。

IR 将控制附着在原有语义节点，工具定义改用 `ir::Tool`，避免污染 OpenAI wire 类型；不新增通用 JSON 扩展袋、位置映射表、新 crate 或内核职责。Anthropic decoder 直接构造类型化消息，system 文本块保持边界；工具结果的内外层控制、调用关联及批次顺序保留。生成响应使用独立 `OutputBlock`，JSON／SSE 及直接构造 IR 的输出路径均拒绝输入控制。

[Codec 测试](../../crates/nyro-llm/tests/anthropic_cache_control_codec.rs)覆盖九种位置、TTL 缺省／null／非法形状、混合控制、并行工具批次及输入／输出边界；[HTTP 测试](../../crates/nyro-llm/tests/native_anthropic_runtime.rs)核对实际 JSON／SSE 请求、503 后严格与原生到严格的 Anthropic 重试、其他协议候选排除及准入释放。此前仅因缓存控制而排除严格候选的预期相应更新，其他原生扩展契约继续保留。

341 项 Rust 测试（新增 8 项）、受影响 crate 的 Clippy、非桌面 workspace 检查、构建及五组代理进程回归通过；16 份原生录制精确回放与原有严格模式分类保持通过。格式、diff 和 117 个文档相对链接检查通过，独立审查未发现阻塞问题。G03 仍为部分完成，推理／签名、其余 Chat 媒体及完整客户端会话继续跟踪；document／thinking 控制、工具结果内图片与 max_tokens=0 预热未包含在本轮严格子集内。仅使用本地 mock，无真实厂商请求或缓存命中认证。整体迁移完成后删除本文，不归档；`docs/superpowers/` 保持忽略。


## 20. G03 Anthropic 严格推理／签名

以当前官方 [thinking](https://platform.claude.com/docs/en/build-with-claude/thinking) 与[手动预算](https://platform.claude.com/docs/en/build-with-claude/extended-thinking)定义为准。严格 Messages 支持 enabled／adaptive／disabled、summarized／omitted display；省略不注入默认值，手动预算至少 1,024 token。模型能力、采样／工具组合和 interleaved budget 例外交由上游校验，不按旧模型列表硬编码。

`nyro-protocol` 增加明确的 thinking wire 类型；`nyro-llm` 的类型化 IR 保留 assistant 摘要、签名、redacted 数据及流式边界。JSON 与 SSE 可回放到严格 Anthropic；只有签名的 omitted 响应不会被视为空内容，签名不解释、不改写。错误类型／索引、缺失或重复签名、乱序、超限和截断流拒绝；不改变既有缓存计量或失败保守结算。

[codec 回归](../../crates/nyro-llm/tests/anthropic_thinking_codec.rs)覆盖配置形状、默认值、历史／工具结果、JSON、SSE、有界验证及不兼容目标拒绝；[HTTP 回归](../../crates/nyro-llm/tests/native_anthropic_runtime.rs)覆盖严格／原生到严格重试、跳过不兼容 backend、公开别名、上游凭证、签名保留、用量单次结算和失败释放。

352 项 Rust 测试（新增 11 项）、受影响 crate 的 Clippy、非桌面 workspace 检查、构建及五组代理进程回归通过。16 份原生录制精确回放与默认严格／显式 false 分类保持通过；格式、diff 和 120 个文档相对链接检查通过。独立审查未发现可操作的正确性问题。流中断回归确认：初始快照已报告的输入用量沿用保守结算，不因签名不完整而退回较小预留值。

G03 仍为部分完成：跨协议推理、工具调用后的 thinking 顺序、beta updates／output_config／output_tokens_details、其他媒体及完整客户端会话尚待后续增量。不新增 crate、依赖或内核职责，不使用真实厂商密钥。整体迁移完成后删除本文，不归档；`docs/superpowers/` 保持忽略。


## 21. G03 Gemini 严格推理／签名

依据官方 [generateContent thinking](https://ai.google.dev/gemini-api/docs/generate-content/thinking)、[Part 签名规则](https://ai.google.dev/gemini-api/docs/generate-content/thought-signatures)及 [ThinkingConfig](https://ai.google.dev/api/generate-content#ThinkingConfig)。支持 includeThoughts、budget -1／0／正数及已定义 level；小写 level 规范化为大写，不同时设置 budget 和 level，不注入模型默认值。Interactions 的 thought step 不混入当前 generateContent 协议。

`nyro-protocol` 提供配置与 Part 字段；`nyro-llm` 的 GeminiText／GeminiCall 类型保留摘要标记、签名、空文本和调用 ID 缺省。JSON 历史及 SSE 保留 Part 边界，不把签名拼到相邻文本或其他调用；不解释、伪造签名。普通跨协议文本／函数路径继续使用原有 IR，专有字段在不兼容目标前明确拒绝。system／user／结果／图片元数据和函数调用后的文本／签名不属于严格子集，原生模式保持更宽范围。

[codec 测试](../../crates/nyro-llm/tests/gemini_thinking_codec.rs)验证配置、null／缺省、调用关联、签名回放、JSON／SSE、跨协议拒绝及容量／终态；[HTTP 回归](../../crates/nyro-llm/tests/native_gemini_runtime.rs)验证严格／原生到严格重试、跳过不兼容目标、凭证替换、公开模型名、末尾累计用量及资源释放。完整会话结束前保留流生命周期，不重复累计 thought token。

364 项 Rust 测试（新增 12 项）、受影响 crate 的 Clippy、非桌面 workspace 检查、构建及五组代理进程回归通过。16 份原生录制精确回放和严格／显式 false 分类保持通过；格式、diff 与 123 个文档相对链接检查通过。独立审查发现的同帧用量延后观测问题已先由 HTTP 测试复现后修复：首个 Part 编码前观测已知用量，finish 仍在最后一个 Part。另补拆帧内存放大边界，在重复复制响应身份前限制展开规模，回归先失败后通过。两项修复均经复查，无剩余阻塞问题。

G03 仍为部分完成：跨协议推理映射、其他媒体、无法表示的交错顺序及真实客户端验收仍需后续增量。本轮不新增 crate、依赖或内核职责，不使用真实厂商密钥。整体迁移完成后删除本文，不归档；`docs/superpowers/` 保持忽略。


## 22. G03 Responses 严格推理

本轮依据官方创建请求与流事件 schema，在既有 `nyro-protocol`／`nyro-llm` 中支持 Responses 专有 reasoning 配置、assistant 历史及 JSON／SSE 推理 item。保留原 item ID、有序摘要块、空摘要、可选状态、历史密文及 done 事件的最终密文，不将 added 的部分密文当作最终值。配置保留 effort／summary／generate_summary／context／mode，不写死模型默认值；旧 include 写法兼容，无状态默认加密行为交由上游执行。字段范围与官方来源见[代理指南](../standalone/rust-proxy_CN.md#严格模式下的-responses-reasoning)。

[codec 回归](../../crates/nyro-llm/tests/responses_reasoning_codec.rs)覆盖配置与历史往返、JSON 用量、摘要块边界、空摘要、最终密文、incomplete、中断／错误生命周期、快照冲突、跨协议拒绝、有界展开与累计状态。新增 [HTTP 回归](../../crates/nyro-llm/tests/native_responses_runtime.rs)验证严格／原生重试、历史保留、模型别名、配额结算、无效配置不访问上游以及已交付流失败不重试。

验证共覆盖 379 项 Rust 测试：受影响 crates 全量 378 项通过，独立审查新增的 JSON completed／incomplete 一致性回归先失败后修复，12 项 reasoning codec 测试复跑通过（其中新增 1 项）。受影响 crate 的 Clippy、非桌面 workspace 检查、构建及五组代理进程回归通过。16 份原生录制精确回放与严格／显式 false 分类保持通过；格式、diff 和 127 个文档相对链接检查通过。独立审查及修复复查无剩余问题。

G03 仍为部分完成：跨协议推理映射、原始 reasoning_text、工具调用后的交错顺序、其余媒体及完整客户端会话继续跟踪。本轮严格输出只接受推理前缀→普通内容→函数调用；其他协议目标明确拒绝 Responses 推理，不合成或丢弃它。不新增 crate、依赖或内核职责，不使用真实厂商密钥。整体迁移完成后删除本文，不归档；`docs/superpowers/` 保持忽略。


## 23. G03 跨协议推理边界与旧行为取舍

根据官方字段定义，在两种 OpenAI API 之间原值转换 `reasoning_effort`／`reasoning.effort`，共用 effort 枚举，不注入模型默认值，不将档位换算成预算。直接 IR 的两处 effort 相等则合并，冲突则拒绝。Responses 的摘要／context／mode／include 等控制继续专有；修正 Gemini 对共享 OpenAI validator 的依赖，避免新映射放行后忽略原始 Responses 控制。官方来源、支持矩阵及调用方类型变更见[代理指南](../standalone/rust-proxy_CN.md#推理转换与旧行为取舍)和[架构文档](architecture.md)。

本轮明确以下新路径取舍：厂商摘要不转为普通回答，签名／密文不互换，不合成缺失签名；OpenAI 兼容扩展 `reasoning_content`／`reasoning`／`reasoning_signature` 仅由匹配原生模式保留；普通文本的 `<think>` 标签与空白原样保留，不迁入旧自动抽取、拼接或裁剪。旧入口暂不修改。上述有损启发式迁移不再作为待实现功能，但目标客户端是否能使用这些边界仍须 G02 会话验收，不据此关闭全部 G03。

新增 [codec 回归](../../crates/nyro-llm/tests/reasoning_boundary_codec.rs)覆盖官方取值、字段省略、双向转换、不兼容目标、冲突 IR、旧扩展拒绝及 JSON／SSE 字面量标签；[HTTP 矩阵](../../crates/nyro-llm/tests/responses_runtime.rs)验证网络请求前的转换／拒绝；[重试回归](../../crates/nyro-llm/tests/native_responses_runtime.rs)验证严格／原生输入跳过不兼容候选、公开别名、配额，以及推理输出无法表达时失败但仍结算已知用量。配置可转换不保证上游生成内容可表达，不会丢弃 reasoning items 后伪造成功响应。

388 项 Rust 测试（新增 9 项）、受影响 crate 的 Clippy、非桌面 workspace 检查、构建及五组代理进程回归通过。16 份原生录制精确回放与严格／显式 false 分类保持通过；格式、diff 和 131 个文档相对链接检查通过。代码与文档独立审查未发现可操作问题。验证仅使用本地 mock 与现有录制样本，不代表真实厂商／完整客户端认证。

G03 仍为部分完成：工具调用后的交错顺序、原始 reasoning_text、其余媒体与缓存／用量扩展及完整客户端会话继续跟踪。本轮不新增 crate、依赖或内核职责，不使用真实厂商密钥。整体迁移完成后删除本文，不归档；`docs/superpowers/` 保持忽略。


## 24. G03／G04 有序 IR 基础

基于 `a66951dd`，将 `Message`／`ResponseMessage` 的正文与工具调用迁移到唯一的 `items: Vec<MessageItem>`，保留 `Content` 的原有嵌套类型。`Delta` 改为 `events: Vec<PositionedDelta>`，各协议 encoder 逐事件消费并校验位置归属。OpenAI Chat 使用消息／工具字段槽位及原工具索引，Anthropic 使用源块索引，Responses 使用 output 与 content／summary 坐标，Gemini 使用收到的 Part 序号；不按调用首次出现顺序重编号，不将签名从所属节点剥离。

本轮是四阶段中的基础 PR：所有现有 codec 迁移，原支持范围继续受校验约束；静态多正文组、工具后正文／推理及既有不可表达排列继续拒绝。普通块 start／end 的完整生命周期、Responses 完整 item 元数据以及新交错排列尚未实现。后续依次补齐 Anthropic、Gemini 和 Responses 的协议增量，每一步覆盖历史、JSON 和 SSE，不把表示能力等同于外部支持完成。工具结果批次、媒体结果、原始 reasoning_text 和完整客户端验收仍分别跟踪。

正文位置使用固定大小状态或复用已受限的协议 item 状态；工具增量位置随现有有界调用缓冲持有。用量仍在 `ChatChunk` 中，Gemini／Responses 展开时已知用量置于首个 chunk，转换失败后的结算仍由原运行时负责。[基础回归](../../crates/nyro-llm/tests/ordered_ir_codec.rs)与各协议回归覆盖有序项、源坐标、索引不重排及错误归属。

406 项 Rust 测试（新增 18 项）、受影响 crate 的 Clippy、非桌面 workspace 检查、构建及五组代理进程回归通过。16 份原生录制精确回放与严格／显式 false 分类保持通过；格式、diff 和 136 个文档相对链接检查通过。独立审查发现的空工具列表角色校验、已关闭正文位置复用、新事件向量的批次与堆分配预算问题均以回归复现后修复，复查无剩余问题。仅使用本地 mock 与现有录制样本，不代表真实厂商／完整客户端验收。

直接引用 `nyro-llm` 的 Rust 调用方需要迁移字段和枚举匹配，见[代理指南](../standalone/rust-proxy_CN.md#rust-调用方的有序-ir-迁移)。本轮不新增 crate、依赖、内核职责，不移除旧入口，不使用真实厂商密钥。整体迁移完成后删除本文，不归档；`docs/superpowers/` 保持忽略。


## 25. G03／G04 三协议交错输出

基于 `40bb0092`，在同一个 PR 中完成 Anthropic、Gemini generateContent 与 Responses 的有序历史、JSON 和 SSE 增量。普通正文／函数／正文在三种 API 之间保序转换；thinking、签名与加密推理仍是各协议专有能力，同协议可在调用后继续出现，不作为跨协议通用字段。Responses 静态 message／function 容器 ID 和状态与函数 `call_id` 分开保留，连续 assistant output items 合并为同一 IR 消息中的有序项，以继续验证紧邻且完整的工具结果批次。

普通叶节点使用显式 start／end，Responses 容器另有独立生命周期。Anthropic／Gemini 使用有界等待队列，按源开始顺序处理反序完成的并行工具及首次正文迟到的容器；未受阻的普通文本不累计完整正文。Responses 保留 output／content 坐标、容器身份和终态快照。Chat 有状态输出跨帧拒绝工具后正文等不可表达顺序，失败后不合成成功终态，也不重试已返回成功 HTTP 状态的上游。

[有序 IR 回归](../../crates/nyro-llm/tests/ordered_ir_codec.rs)与各协议 codec 回归覆盖正文／工具／专有推理交错、容器与叶节点生命周期、反序完成、空容器、错误身份、截断及资源上限。[HTTP 矩阵](../../crates/nyro-llm/tests/responses_runtime.rs)用独立 wire fixtures 覆盖三协议 JSON／SSE 和历史互转、完整结果关联、Chat 目标发送前拒绝，以及无法表达的上游输出失败后已观测用量的单次结算。439 项 Rust 测试（新增 33 项）、受影响 crate 的 Clippy、非桌面 workspace 检查、构建及五组代理进程回归通过。最后的展开预算贴边修复后，63 项 Responses codec／HTTP 专项及 Clippy 复验通过，最终构建的五组进程回归再次通过。16 份原生录制精确回放与严格／显式 false 分类保持通过；格式、diff 与 138 个文档相对链接检查通过。独立审查发现的容器首次正文迟到、工具身份迟到、Chat 跨 choice 状态预算／跨帧正文越序以及生命周期展开预算问题均已补回归修复，定向复核无剩余问题。验证仅使用本地 mock 与既有录制样本，不代表真实厂商／完整客户端认证。

G03／G04 仍为部分完成：原始 reasoning_text、其余媒体与工具结果媒体、未实现缓存／用量扩展及完整客户端会话继续跟踪。不新增 crate、依赖或内核职责，不移除旧入口，不使用真实厂商密钥。整体迁移完成后删除本文，不归档；`docs/superpowers/` 保持忽略。


## 26. G03／G04 工具结果图片

基于 `0f9ea370`，按官方定义扩展现有工具结果路径：Anthropic 与 Responses 在结果内部保留有序文本／图片、调用 ID 和图片来源，支持公共 PNG／JPEG／WebP／GIF base64 与 HTTP(S) URL 子集互转。Responses detail 和输入断点、Anthropic 内外缓存控制及显式错误仍遵守各自目标边界。图片不提升为独立 user 消息，不下载或转码，不借此开放 Chat Completions 的工具图片。

Gemini 同时保留 response 对象与嵌套媒体 parts，包括 PNG／JPEG／WebP、base64、displayName 和命名引用。新增协议专有 `ContentPart::GeminiFunctionResponse` 作为工具结果消息的唯一本体；不保存第二份文本副本，也不假定任意对象和有序文本／图片数组等价，因此当前仅同协议转换。显式空 parts 保留专有表示，省略／null 沿用纯对象路径。具体支持范围和官方来源见[代理指南](../standalone/rust-proxy_CN.md#工具结果中的图片)。

新增 [Anthropic](../../crates/nyro-llm/tests/anthropic_tool_images.rs)、[Responses](../../crates/nyro-llm/tests/responses_tool_images.rs)、[Gemini](../../crates/nyro-llm/tests/gemini_tool_images.rs) 和 [Chat 边界](../../crates/nyro-llm/tests/tool_image_boundary_codec.rs) 回归；[HTTP 矩阵](../../crates/nyro-llm/tests/responses_runtime.rs)验证结果嵌套位置、字节／块顺序、调用关联、JSON／SSE、目标筛选及非法来源发送前拒绝。复用原有完整工具结果批次、凭证、用量和资源收尾规则。457 项 Rust 测试（新增 18 项）、受影响 crate 的 Clippy、非桌面 workspace 检查、构建及五组代理进程回归通过；16 份录制精确回放与严格／显式 false 分类保持通过，格式、diff 和 144 个文档相对链接检查通过。独立代码审查未发现实质问题，中文指南中两处旧支持范围已修正。原生回放首次与其他进程测试并行执行时在启动阶段超过原有 5 秒 readiness 上限，尚未开始样本校验；保留该上限、加入临时诊断后单独复跑，三次启动约为 2 秒，随后未修改的原脚本也完整通过。未复现首次启动超时，未修改程序或测试阈值。验证仅使用本地 mock 与既有录制样本，不代表真实厂商／完整客户端认证。

G03／G04 仍为部分完成：非图片结果媒体、厂商文件引用、原始 reasoning_text、未实现缓存／用量扩展和完整客户端会话继续跟踪。本轮不新增 crate、依赖或内核职责，不移除旧入口，不使用真实厂商密钥。整体迁移完成后删除本文，不归档；`docs/superpowers/` 保持忽略。


## 27. G02–G04 本地多轮工具会话

基于 `9a93dfb0`（PR #345），补充[多轮 HTTP 会话矩阵](../../crates/nyro-llm/tests/responses_runtime/multiturn.rs)。每条成功会话发送三次真实 Runtime 请求：首轮返回两个并行调用，客户端从实际响应提取调用 ID／参数并反序提交结果，第二轮继续调用一个工具，第三轮验证最终回答。历史来源是客户端收到的 JSON／SSE，而非预先构造的下一轮样本；不通过被测 codec 或 IR 生成期望值。比较只规范化协议外层 ID／状态、文本 JSON 表示、单文本结果块与字符串的等价形式，以及不带签名的空文本流片段；媒体块顺序、调用 ID 和专有推理状态不规范化。

四种严格入口／目标形成 16 条文本路径；图片结果覆盖 Anthropic／Responses 双向转换及 Gemini 同协议对象、parts 和命名引用；专有推理与图片组合验证 Anthropic 签名、Gemini 函数签名和 Responses 最终密文跨两轮历史的保留。另覆盖四种匹配原生模式。共 112 条三轮成功会话（336 次请求），每条路径分别使用全 JSON、全 SSE 和两种交替模式；另有 28 条第二轮边界／恢复会话。上游 SSE 按 7 字节分片。逐轮比较历史顺序、调用与结果关联、图片位置／字节、专有状态、模型别名、凭证隔离、用量和并发许可释放。

第二轮拒绝用例从真实首轮响应构造：在 Anthropic／Gemini 完整批次规则下验证缺失、重复和未知结果 ID，确认不访问上游且修正后可继续；图片与专有状态在不兼容目标前拒绝，原协议仍可处理。OpenAI 历史校验规则没有被替换为全局完整批次要求；移除请求推理配置后单独验证专有历史本身的拒绝边界。

验证：`cargo test -p nyro-llm --test responses_runtime --offline` 的 28 项 HTTP 回归通过（原有 22 项、新增 6 组会话测试）；最后将 Gemini 两轮函数签名设为不同样本后，6 组会话专项复验通过。`cargo clippy -p nyro-llm --all-targets --offline -- -D warnings`、格式、diff 与 147 个文档相对链接检查通过。独立审查发现的工具消息角色与 Anthropic SSE 块索引漏检已补断言并复查。临时在生产请求 encoder 注入“丢弃工具图片”和“替换历史签名”，均使对应会话在第二轮断言失败；随后逐字恢复源码，最终没有生产代码差异。新增会话测试还修正了旧测试重组器将省略的 Chat 正文补为空字符串的问题，避免测试客户端自行制造历史内容。

这些是本地测试客户端与 HTTP mock 的组合回归，不是 SDK、真实工具执行、厂商会话或签名真实性认证。G02–G04 仍为部分完成：真实客户端／厂商验收、非图片媒体、文件引用、原始 reasoning_text 和未实现缓存／用量扩展继续跟踪。本轮不新增 crate、依赖或生产功能，不移除旧入口。整体迁移完成后删除本文，不归档；`docs/superpowers/` 保持忽略。


## 28. G06 文件代理 API Key 生命周期

基于 `e92b52e6`（PR #346），在独立 `nyro-security` 原语中补充 `ApiKey.enabled`（省略为 true）和 `expires_at`（可选非负 Unix 秒，省略／null 不过期）。每次认证判断启用状态与当前墙钟，`now >= expires_at` 拒绝；不依赖重载计时器，不保存明文 secret，不新增依赖或内核职责。显式 `authenticate_at` 支持精确时间测试；时钟回拨按当前时间重判，带到期时间的密钥在 epoch 之前拒绝。

禁用和已过期密钥仍接受合法配置并保留主体／模型绑定，也继续参与 ID／secret 唯一性校验。生命周期值进入现有配置指纹，时间流逝不改变指纹；默认字段的显式／省略表示等价。无效字段类型拒绝整份配置且错误不包含输入值。Rust struct literal 需显式提供新字段，原有调用方和测试已同步迁移；旧 YAML 的省略行为保持兼容。

模型发现与四种 Chat API／Embedding 的严格和匹配原生路径共用认证；失效密钥在匿名模型上也返回通用 401，不访问上游或消耗并发、rate、quota。代际在请求进入时选定，认证在读取请求体后执行：旧代际的慢上传仍按当前时间检查到期，已准入 SSE 不因到期或重载禁用而中断。[HTTP 回归](../../crates/nyro-llm/tests/responses_runtime/api_key_lifecycle.rs)与[进程回归](../../tests/proxy_reload_smoke.py)覆盖拒绝、禁用／续期／移除到期时间、失败重载、相同配置指纹及在途响应保留。

验证：`cargo test -p nyro-security -p nyro-protocol -p nyro-llm -p nyro-config -p nyro --offline` 全量 473 项通过；随后新增慢上传测试，2 项生命周期 HTTP 专项复验通过，共验证 474 项不同 Rust 测试。受影响 crate 的 Clippy（all-targets、拒绝 warning）、非桌面 workspace 检查、根二进制构建及扩展后的重载进程回归通过；格式、diff 与 149 个文档相对链接检查通过。配置新字段和禁用凭证用例先在旧行为下失败，再通过实现。独立审查未发现生产代码问题；已将进程到期准备余量增至 10 秒、上游流等待预算增至 40 秒，并给墙钟等待增加单调时间上限，复查无剩余问题。测试只使用本地 mock 与临时密钥字符串。

G06 的文件代理数据面部分已落地。管理界面／数据库密钥编辑、旧数据时间格式转换和配置发布属于 G10–G11；G02–G05、G07–G13 的其余差异继续跟踪。本轮不移除旧入口、不使用真实厂商密钥；整体迁移完成后删除本文，不归档。`docs/superpowers/` 继续忽略。


## 29. G07 主体请求窗口与组合准入

基于 `91f6e5aa`（PR #347），在同一个 PR 中补齐主体作用域、RPM／RPD 请求窗口、与模型 token bucket 的原子组合及计数／重载规则。配置为 `llm.subject_limits.<主体 ID>.rpm/rpd`，引用已有 API Key ID，至少一项正 `u32`；省略禁用对应窗口，零／null／空策略／未知字段或主体拒绝。密钥生命周期仍归 `nyro-security`，限制策略没有进入密钥实体。

[`nyro-limit/request`](../../crates/nyro-limit/src/request.rs) 使用单调时钟和准入时间队列实现最近 60 秒／24 小时滚动窗口，达到边界的记录过期，日窗口不在午夜清零。各窗口及可选模型桶在固定锁序下原子准入；拒绝不扣减任何请求规则，原生协议 `429` 带建议性的 `Retry-After`，立即释放并发许可且不调用上游。一次逻辑请求跨重试只计一次；准入后的上游错误、无健康 backend、quota 拒绝、取消与响应丢失不退款，准入前拒绝与模型发现不计数。

主体跨模型别名、协议和工作负载共享；不同主体相互独立。匿名请求没有主体窗口，认证调用匿名模型仍受主体规则约束。[LLM 注册表](../../crates/nyro-llm/src/subject_limit.rs)通过 `SharedResources` 跨代际保留活跃或未过期历史，轮换 secret、禁用／重新启用、到期／续期、移除／加回同一 ID 不清零。有活跃绑定或未过期历史时修改窗口参数拒绝候选；不活跃且历史过期后在后续绑定时回收。失败候选不重置现役预算，未使用的候选绑定不永久占住规则。

[HTTP 回归](../../crates/nyro-llm/tests/rate_runtime/subject_limits.rs)覆盖作用域、四种 Chat API／Embedding、匹配原生 Chat、发现隔离、组合拒绝、代际与部分构建失败；原有 rate 回归分别对模型桶和主体窗口运行，覆盖准入前拒绝、重试、健康、取消／超时及 SSE 丢失。[进程回归](../../tests/proxy_reload_smoke.py)增加实际 SIGHUP 轮换、窗口参数拒绝、禁用／重新启用与移除／加回计数保留。

验证：`cargo test -p nyro-limit -p nyro-security -p nyro-protocol -p nyro-llm -p nyro-config -p nyro --offline` 全量 506 项通过；随后补充部分候选构建回滚和后续 quota 拒绝计数，6 项主体专项通过，共验证 508 项不同 Rust 测试。受影响 crate 的 Clippy（all-targets、拒绝 warning）、非桌面 workspace 检查、根二进制构建及扩展后的重载进程回归通过；格式、diff 与 154 个文档相对路径检查通过。配置接受、未知主体拒绝和 HTTP 限速先验证旧行为失败，再通过实现；并发组合准入与窗口精确边界由原语测试覆盖。独立审查未发现生产代码问题，已补充建议的部分候选失败测试及重载排查文档。验证仅使用本地 mock 与临时测试密钥字符串。

截至本节对应的 PR #348，G07 仍为部分完成：主体 TPM／TPD、持久化、多副本协调与旧数据发布尚未实现；后续 token 窗口进展见第 30 节。不把累计模型 quota 当成 TPM，不从历史日志导入准入计数。进程重启清零；立即修改已建立的窗口规则需重启。本轮不新增 crate、依赖、数据库 schema 或内核职责，不移除旧入口。整体迁移完成后删除本文，不归档；`docs/superpowers/` 保持忽略。


## 30. G07 主体 TPM／TPD 预留与结算

基于 `02c75a3f`（PR #348），在现有 `llm.subject_limits` 中补充可选 `tpm/tpd` 与配套 `reserve_tokens`，至少一个请求或 token 窗口，token 预留须为正且不超过各已配置上限。允许仅 token 窗口或与 RPM／RPD 组合；旧请求窗口 YAML 保持兼容，Rust `SubjectLimitConfig` 字面量需新增三个可选字段。

[`nyro-limit/window`](../../crates/nyro-limit/src/window.rs) 按通用单位维护每个窗口的已结算队列及共享在途预留。预留不随时间过期；正用量从结算时刻进入最近 60 秒／24 小时窗口，边界准确过期。与模型累计 quota 在固定锁序下原子预留，拒绝不改变两类 token 预算；原先逻辑请求准入次数不退款。每次上游重试独立预留，连接建立失败全额释放，协议完成按实际结算，缺失／失败／取消分别取各预算自身预留和最高有效观测值的较大值。超额如实记录并阻止后续预留，不中断已准入流，不承诺厂商实际消耗硬上限。

主体 token 拒绝返回原生协议 `429`，已结算历史可腾出预算时返回向上取整的最长所需 `Retry-After`；仅在途预算已足以阻塞时不返回未知等待时间。模型 quota 保持原错误语义。现有观察与 `AttemptQuota` 结算路径共同覆盖严格、原生、JSON、SSE 和 Embedding；`token_window_charged_tokens`／`token_window_outcome` 与模型 quota、上游报告用量分开，TPM 和 TPD 不重复累加成日志用量。

注册表跨代际保留请求历史、未过期 token 结算和在途预留。主体跨模型、协议和工作负载共享，匿名无主体规则，认证调用匿名模型仍计量。轮换、禁用／恢复及移除／加回不清空预算；活跃绑定或任何未清空状态拒绝参数修改，闲置空状态在后续绑定时回收。长流跨窗与只剩预留凭据时的生命周期由原语／注册表回归覆盖；[HTTP 回归](../../crates/nyro-llm/tests/quota_runtime/token_windows.rs)覆盖组合、重试、跨协议、零／未知／无效／超额用量、终态与中断；[观测回归](../../crates/nyro-llm/tests/observation_runtime.rs)区分两类实际／回退扣费；[进程回归](../../tests/proxy_reload_smoke.py)跨 SIGHUP 持有 SSE，验证轮换、参数拒绝、禁用、移除／加回及实际结算后的退款和历史保留。

验证：`cargo test -p nyro-limit -p nyro-security -p nyro-protocol -p nyro-llm -p nyro-config -p nyro --offline` 全量 529 项通过；独立审查后补充四种主体窗口同时启用时的请求计数回归，1 项专项通过，共验证 530 项不同 Rust 测试。受影响 crate 的 Clippy（all-targets、拒绝 warning）、最后新增测试目标的 Clippy、非桌面 workspace 检查、根二进制构建和扩展后的重载进程回归通过；格式、diff 与 159 个文档相对路径检查通过。配置接受测试先验证旧行为失败；修正复用客户端默认匿名的前提后，临时跳过主体 token 准入的变异使认证 HTTP 测试在期望用量处失败，随后逐字恢复运行时代码并通过预算／观测回归。独立审查未发现生产代码缺陷，建议的 token-only、仅剩预留时的策略保留、双窗口最长等待和混合规则先后顺序均已补回归，历史进度表述也已修正。测试只使用本地 mock 与临时字符串密钥。

G07 仍为部分完成：持久化、多副本协调、旧数据导入和控制面发布继续跟踪，不从旧请求日志猜测在途或已结算状态。当前进程重启清零，活跃／未清空规则需要立即修改时重启。本轮不新增 crate、依赖、数据库 schema 或内核职责，不下线旧入口。整体迁移完成后删除本文，不归档；`docs/superpowers/` 保持忽略。


## 31. G08 路由、健康与重试契约核对

基于 `5916a045`（PR #349）核对[旧选择器](../../crates/nyro-core/src/router/selector.rs)、[旧调度器](../../crates/nyro-core/src/proxy/dispatcher/mod.rs)、[旧健康注册表](../../crates/nyro-core/src/router/health.rs)，以及[新运行时](../../crates/nyro-llm/src/runtime.rs)、[新健康注册表](../../crates/nyro-llm/src/health.rs)和[配置定义](../../crates/nyro-llm/src/config.rs)。先核对旧行为，再按已接受方案新增可选选择策略；以下对照表保留旧实现证据，并列出最终迁移契约。

### 选择策略与配置映射

| 项目 | 旧实现 | 新版现状与迁移要求 |
|---|---|---|
| 默认 weighted | 仅保留正权重目标，按权重生成不重复尝试顺序；不读取 priority。未知 balance 字符串也回退到 weighted。 | 每次从健康且兼容的最小 priority 中按权重选择，单请求不重复 backend ID。迁移旧 weighted 时将所有 priority 统一为 `0`，保留正权重比例；不能直接复制差异化 priority。新配置使用 `strategy`，严格拒绝未知字段，没有 balance 字段。 |
| priority | priority 升序，同级保留传入顺序，不使用权重；文件内关于同级随机的注释不代表实现。 | priority 升序，同级按权重随机。若需要确定的尝试顺序，为 backend 分配不同 priority；配置列表顺序不参与指纹，不能依赖它表达顺序。旧有符号 priority 可按相对顺序映射到从 `0` 起的非负层级，同级是否随机需明确。 |
| cooldown | 按最近成功调度距今时间降序，间隔最多计 60 秒，未使用目标视为 60 秒；并列保留传入顺序。失败不更新，选择时也不预占。 | 接受以 `least_recent` 替代：按每次实际发起顺序选择，同级并列按权重，不保留成功才更新与 60 秒截断。它不是故障冷却；`health.cooldown_ms` 无法替代。 |
| latency | 成功调度后更新 EMA，alpha 为 `0.2`；无样本按 `0` 排最前，并列保留传入顺序。 | 接受以新 `latency` 替代：单次发送到 2xx 响应头的 EMA，另有 5% 探索。旧样本从整个 dispatch 起点计时，包含前置处理及此前尝试；JSON 包含响应体处理，SSE 只到流式响应返回，不能当作统一上游耗时或首 token 延迟。 |
| 禁用目标 | 只有 weighted 过滤 `weight <= 0`；其余三个策略仍可能选择这些目标。 | 所有请求统一排除 `weight: 0`，负权重拒绝；全部权重为零时启动失败。迁移需明确旧零权重目标是否应继续启用。禁用目标仍接受配置与协议引用校验。 |
| 状态作用域 | cooldown／latency 使用进程全局表，以 `provider_id:model` 为键；不同公开模型共享样本。 | 新选择历史和健康状态均按公开模型和有效 backend 绑定隔离，由 LLM 显式拥有；路由历史不依赖健康策略，均不进入内核。 |

### 健康、重试与响应提交

| 项目 | 旧实现 | 新版现状与迁移要求 |
|---|---|---|
| 尝试预算 | 顺序遍历候选，遇可重试失败继续，没有模型级总尝试数配置。 | `max_attempts` 默认 `1`，包含首次发送。若需要多个目标故障转移，显式配置上限；跳过不兼容、禁用或不健康目标不计尝试。 |
| HTTP 重试 | 调度响应状态 `408/429/500/502/503/529` 允许换目标；该状态可能由本地处理生成。 | 仅上游 `429/500/502/503/504/529` 或明确建连失败允许换目标；`408` 和发送结果不明的传输错误不重试。不要为旧列表放宽重复执行边界。 |
| 成功响应提交 | 调度返回 `<400` 即记录成功；成功上游响应经转换变成可重试错误时仍可能换目标。 | 上游 2xx 已提交 backend，即使首帧之前解析失败也不换目标。SSE 完整协议终态才恢复健康；首个有效帧仅允许开始交付。 |
| 默认健康策略 | Gateway 总是创建注册表，阈值 `3`，恢复等待 `30` 秒；返回错误状态都会记失败。 | 省略 `health` 则关闭；`health: {}` 启用阈值 `3`、冷却 `30000` 毫秒。显式迁移该设置，数值相同不代表错误计数相同：非瞬时 HTTP 状态、取消、丢弃及整体期限保持中性。 |
| 冷却后恢复 | 时间到后并发请求均可通过健康检查。 | 时间到后仅一个请求持有恢复探测，其余使用其他候选或得到 `503`；取消释放探测资格但不宣告恢复。 |
| 全部不健康 | 全部跳过、没有此前响应时，调度器返回 `502`。 | 首次尝试前全部健康阻塞返回 `503`；已有尝试失败时返回最后一个脱敏错误。请求窗口已在此前准入计数，尚未发送的尝试不预留 token。 |
| 代际与绑定 | 健康键同样为 `provider_id:model`，不区分公开模型或同名目标的凭证／地址变化。 | 地址、API、凭证、原生模式、上游模型、backend ID 或健康策略变化会获得新绑定；仅权重／优先级变化保留健康状态。由根组合层显式共享注册表，退役绑定随持有者释放，重启清空。 |

### 已落地范围与采样契约

在现有 `nyro-llm` 增加模型级 `strategy: weighted | least_recent | latency`；省略与显式 weighted 的配置指纹相同，策略变化会发布新代际，列表重排仍不改变指纹。拒绝 null、未知名称和旧 cooldown／priority 别名。全部策略统一执行协议兼容、零权重禁用、健康、优先级筛选；新策略的权重只处理最终并列。

`least_recent` 在健康探测认领、token 预算准入成功后记录每次发起，选择到记录受模型范围内的短锁保护，不跨网络 await；失败、取消不回退发起顺序，预算拒绝不更新。没有 60 秒截断，不等待响应成功后才轮转。

`latency` 只采单次发送到收到 2xx 响应头的耗时，新样本占 EMA 的 20%；不混入前置准入、以前尝试、JSON 正文或 SSE 帧消费。样本在正文校验前记录，后续正文无效也不撤销；健康成功仍独立要求完整有效响应。非 2xx、建连失败、响应头前取消／期限耗尽没有延迟样本。每个从未尝试的目标先获一次机会，有成功样本后通常选最快者，5% 概率探索最久未发起的可用目标；没有成功样本且已有在途尝试的目标不参与这次探索。如果所有候选都没有成功样本，则依次按最少在途尝试、最久未发起、权重选择。失败目标不能因一直无成功样本而持续抢占正常目标；探索不承诺固定次数内重新采样。

根组合层通过 `SharedResources.routing` 复用历史；三种策略都记录发起和响应头，策略切换可使用已有样本。历史与健康共用有效 backend 身份定义，但不共享健康策略或成功判定。权重、优先级、选择／健康策略变化保留历史；身份、地址、凭证等绑定变化重置。不同公开模型隔离，已移除绑定在最后持有者释放后回收；无持有者后再加回与进程重启均重新采样。不新增 crate、依赖、数据库 schema 或内核职责，不下线旧入口。控制面配置发布／旧数据导入仍待 G10–G11。

完整用户契约和旧配置迁移说明见[中文代理指南](../standalone/rust-proxy_CN.md)及[英文代理指南](../standalone/rust-proxy.md)。整体迁移完成后删除本文，不归档；`docs/superpowers/` 保持忽略。

### 本轮验证

新增[策略 HTTP 回归](../../crates/nyro-llm/tests/failover_runtime/strategies.rs)、router 单元测试及配置指纹测试，覆盖并发在途分配、按权重处理并列、初始采样／失败重采样／探索、EMA、响应头与 JSON／SSE 首帧耗时分离、健康故障转移、预算拒绝不改变历史、策略／权重／顺序重载、凭证变化隔离和退役回收。[SIGHUP 进程回归](../../tests/proxy_reload_smoke.py)补充旧 SSE 持有期间切换策略、历史复用、等价重排、非法策略拒绝与绑定变化。

配置、并发路由、延迟选择回归分别先在缺少实现时失败，再通过实现。原有健康／重试／响应提交测试继续保留；没有真实厂商请求。

实际执行：

```sh
cargo test -p nyro-llm -p nyro-config -p nyro --offline
cargo test -p nyro-llm --test responses_runtime api_key_lifecycle::expiry_is_checked_after_a_slow_request_body_arrives --offline -- --exact
cargo test -p nyro-llm --test responses_runtime --test responses_tool_images --test routing_runtime --test runtime --test tool_image_boundary_codec --test tool_schema_codec --offline -- --test-threads=4
cargo test -p nyro-llm --test failover_runtime strategies --offline
cargo test -p nyro-llm -p nyro-config --doc --offline
cargo clippy -p nyro-llm -p nyro-config -p nyro --all-targets --offline -- -D warnings
cargo clippy -p nyro-llm --test failover_runtime --offline -- -D warnings
cargo build -p nyro --offline
python3 tests/proxy_reload_smoke.py
```

首轮全量测试在既有慢上传到期用例的前置条件失败：运行时构建及请求启动已超过预设的 5 秒到期窗口，尚未进入要验证的到期后鉴权行为。该用例单独复跑通过，所在 Responses 组以 4 个线程复跑全部通过，并补完首轮被中断的后续组；没有修改鉴权实现或该测试。最终覆盖 **505 项不同的 Rust 测试**，Clippy、根构建、扩展 SIGHUP 进程回归及格式检查通过；169 个本地文档链接路径有效。独立代码及文档／进程测试审查未发现待修复问题。


## 32. G09 文件代理网络配置与迁移契约

基于 `bffcee58`（PR #350），核对[旧 Gateway 客户端](../../crates/nyro-core/src/lib.rs)、[旧代理 HTTP router](../../crates/nyro-core/src/proxy/server.rs)、[旧 Server 组合](../../src-server/src/main.rs)，以及[新 HTTP 入口](../../src/http.rs)、[新 Provider driver](../../crates/nyro-llm/src/provider.rs)、[新接入校验](../../crates/nyro-llm/src/runtime/endpoint.rs)和[新文件配置](../../crates/nyro-config/src/lib.rs)。本轮交付 Provider 出口代理与独立 HTTP/1 配置，并按推荐方案保留新版 HTTP 默认行为；不新增 crate，不改管理数据库或旧入口。

### 已确认差异

| 项目 | 旧路径 | 新路径与迁移契约 |
|---|---|---|
| 显式出口代理 | Provider `use_proxy` 为 true 且全局 `proxy_enabled` 为 true，才读取 `proxy_url` 构造代理客户端；未启用时返回默认客户端，启用但 URL 空白时报错。 | Provider 通过 `transport.proxy_url` 显式配置 HTTP／HTTPS 代理，省略为直连；不采用旧全局开关与局部开关组合。 |
| 系统／环境代理 | 默认客户端没有调用 `.no_proxy()`，所以 `use_proxy: false` 或全局开关关闭不保证直连。 | 继续明确禁用系统／环境自动代理；配置显式代理后也不依赖环境旁路规则。旧部署若靠环境变量生效，需迁入显式配置。 |
| HTTP 版本 | `proxy_force_http1` 仅在构建启用代理的客户端时生效，默认 false；默认客户端不读取它。 | Provider 级 `transport.http1_only` 独立于是否走代理。默认保留现有协商行为，可显式限制为 HTTP/1；不提供强制 HTTP/2 或任意 TLS 放宽开关。 |
| CORS 默认值 | 默认包含 localhost、127.0.0.1 对应端口及两个 Tauri 来源；支持 `*`。来源字符串仅尝试转成 HeaderValue，全部解析失败时回退任意来源。旧数据面允许 GET／POST／OPTIONS 及固定请求头列表。 | 新入口没有 CORS 层，继续默认关闭；确认浏览器跨域调用需求后，再提供严格来源白名单。非法配置应拒绝，不能静默丢弃后放宽。Tauri 来源不属于目标 Server 默认值；CORS 不能替代认证授权。 |
| 存活探针 | `/health`、`/healthz`、`/` 都返回状态 200 和 JSON 样式文本。 | 仅 `/healthz`，状态 200、空正文。部署应改用该端点并检查状态码，不因旧别名存在就恢复 `/`；有不能修改的现有探针时再明确兼容范围。 |
| 就绪探针 | `/readyz` 查询存储可连接与 schema 兼容，返回 200 或 503 及状态文本。 | `/readyz` 根据 Host 是否接受新 lease 返回 200／503、空正文，不表示所有 Provider 健康。未来 serve 的发布就绪与存储要求在 G10–G11 明确，不能让文件代理依赖数据库。 |
| 请求与响应大小 | 代理 Json 提取器允许 100 MiB 请求。 | 新 runtime 默认请求 1 MiB、响应 16 MiB、流帧 1 MiB，均可配置；请求正文累积读取时执行限制，不依赖 Content-Length。保留现有默认，迁移时显式设置需要的上限。 |
| 期限 | 旧上游客户端总超时 300 秒，不等价于整个逻辑请求的统一期限。 | 新版默认 120 秒，覆盖正文读取、全部尝试及响应交付，由 `server.request_timeout_ms` 配置。代理与 HTTP/1 配置不得引入独立重试预算或延长原请求期限。 |
| 重定向／自动重试 | 旧客户端采用依赖默认行为。 | 新 driver 显式禁用重定向与 Reqwest 自动重试；继续由 runtime 管理故障转移与 2xx 提交，不随网络配置开放自动重定向。 |
| 查询凭证／Header | 旧 Gemini 入口在缺少 `x-goog-api-key` 时把查询参数 `key` 注入本地鉴权 Header；旧 CORS 放行的 Header 也不等于协议层会原样透传。 | 新入口拒绝查询参数 `key`、`api_key`、`access_token`，仅 Gemini 流式端点接受单个 `alt=sse`；按协议接收 Header 凭证。保留显式凭证隔离；厂商特殊 Header 需求归 G05 与协议兼容，不做任意透传。 |

依赖依据为仓库锁定的 Reqwest `0.12.28`。官方 [ClientBuilder](https://docs.rs/reqwest/0.12.28/reqwest/struct.ClientBuilder.html#method.no_proxy) 文档说明 `no_proxy()` 关闭自动系统代理并清空显式代理，添加显式代理也会关闭自动系统代理；[HTTP/1 开关](https://docs.rs/reqwest/0.12.28/reqwest/struct.ClientBuilder.html#method.http1_only)和 [Proxy](https://docs.rs/reqwest/0.12.28/reqwest/struct.Proxy.html) 已提供所需基础能力。实现先设直连默认，再添加显式代理，避免后续 `no_proxy()` 清空刚添加的设置。

### 已交付：Provider 显式出口配置

配置和 driver 留在 `nyro-llm`，根组合层继续负责代际发布，kernel 不参与网络策略。可用配置如下，完整契约见[代理指南](../standalone/rust-proxy_CN.md#provider-网络传输)：

```yaml
llm:
  providers:
    example:
      kind: openai
      base_url: https://api.example.com/v1
      transport:
        proxy_url: http://127.0.0.1:7890
        http1_only: true
```

- `transport` 省略或空对象表示直连、保留现有 HTTP 版本协商；`http1_only` 默认 false。代理 URL 省略表示直连，显式 null／错误类型／未知字段拒绝，不静默回退直连。Rust `Provider` 构造方新增 `transport: Default::default()`。
- 仅支持显式 HTTP／HTTPS 代理，URL 用户信息提供代理 Basic 认证；拒绝缺失主机、零端口、非根路径、query、fragment 和字面空白。Debug、网关错误和 Nyro 日志不暴露代理地址或凭证；配置序列化仍包含凭证。默认直连和显式代理均忽略自动系统／环境代理及 NO_PROXY 旁路规则。
- Provider API Key 与代理认证分开，HTTP 转发和 HTTPS CONNECT 均有本地 mock 回归；HTTP 转发代理可看到 HTTP 上游请求，CONNECT 代理认证不进入隧道内请求。代理连接失败不会偷偷改为同一 Provider 直连；其他 backend 继续服从既有路由和尝试预算。
- 配置指纹纳入传输选项，省略／空对象／默认值一致。代理 URL 文本保留在指纹中，等价拼写可能发布新代际；健康与选择历史使用归一化 URL 身份。地址、代理认证或 HTTP 模式变化会隔离历史，模型 rate、主体窗口和 quota 不因此清零。
- 新请求使用新客户端，旧 SSE 持有旧代际完成；无效候选保留活动代际。重定向、隐式重试继续关闭，连接建立和响应交付仍共用原请求期限。TLS 保持正常证书校验，没有新增放宽开关。

文件代理范围内的 G09 交付完成。CORS 白名单没有实现，后续浏览器部署如需跨域应另行明确需求；未来 serve 的发布就绪、控制面配置到快照、部署探针更新仍由 G10–G11 跟踪。整体迁移完成后删除本文，不归档；`docs/superpowers/` 继续遵守 Git 忽略规则。

### 本轮验证

新增配置测试先因未知 `transport` 字段失败，实现后通过；配置指纹、默认值、URL 拒绝、脱敏和健康／路由绑定测试覆盖传输变化。[网络进程测试](../../tests/proxy_network_smoke.py)使用真实根二进制和本地 HTTP／TLS mock，覆盖默认直连及环境隔离、显式代理且忽略 NO_PROXY、百分号编码代理认证、CONNECT 凭证边界、直连／代理 HTTP/1 ALPN、非法重载保留活动配置、失败不直连、禁止重定向、期限、旧 SSE 和已消耗 quota 跨重载保留、探针／CORS 及日志脱敏。TLS fixture 为公开测试证书，只注入子进程信任，不改变系统证书或生产校验。

实际执行：

```sh
cargo test -p nyro-llm -p nyro-config -p nyro --offline -- --test-threads=4
cargo clippy -p nyro-llm -p nyro-config -p nyro --all-targets --offline -- -D warnings
cargo build -p nyro --offline
python3 tests/proxy_network_smoke.py
python3 tests/proxy_reload_smoke.py
cargo fmt --all -- --check
```

**508 项 Rust 测试通过，零失败**；Clippy、根构建、新网络进程回归、既有重载回归和格式检查通过。177 个本地文档链接路径有效，`git diff --check` 通过。独立代码与文档审查未发现待修复问题。首轮 TLS mock 因测试证书误用 `CA:TRUE` 被正常校验拒绝，改为带 `serverAuth` 的 `CA:FALSE` 端点证书后网络回归通过，没有放宽生产 TLS 校验。没有真实厂商请求、数据库迁移或旧入口下线。


## 33. G10 最小 SQLite 控制面闭环

本节记录已合并的 PR #352：在 PR #351 的文件代理基础上新增 [`nyro-control`](../../crates/nyro-control/src/lib.rs) 和根 [`serve`](../../src/serve.rs)。控制包仅拥有类型化快照与 SQLite 存储；根 [`control`](../../src/control.rs) 负责 HTTP、管理认证、版本协调及 Host 发布。数据库实体不进入 kernel 或 LLM 请求执行，也不新建通用数据库框架。

专用 SQLite v1 只保存一行完整草稿与已发布快照；schema 定义见[数据库文档](../database/schema.md)。`--config` 仅为新库提供种子，已有库拒绝再次传入；重启读取已发布版本，保留但不激活草稿。单进程独占，拒绝旧库与外部 schema，没有数据导入。新 Unix 文件使用 `0600`，数据库、备份与导出内容均需按明文凭证保护。旧表及 PostgreSQL/MySQL 参考 schema 继续服务旧入口。

管理监听默认 `127.0.0.1:19531`，仅接受回环地址，与配置中的数据监听分开。独立令牌文件提供 Bearer 认证。首轮 `GET /admin/config` 返回含明文密钥的完整草稿及已发布／活动版本，并标记 `no-store`（第 34 节已将明文读取迁至显式 `/admin/config/export`）；`PUT` 以 `expected_revision` 比较保存完整有效配置，`POST /admin/config/publish` 以当前 `revision` 显式发布，冲突返回 `409`。

发布先校验进程设置并构建候选，再持久化发布目标，最后激活 Host。提交前失败保留旧目标和活动代际；提交后激活中断报告 `202 pending`，重启恢复已提交目标，不宣称跨资源原子提交。接受后的管理操作由服务持有，客户端断连后仍可能完成，调用方需用 GET 核对。重复发布等价配置不新建代际，旧 SSE 保留原 lease；内存限制、健康与路由历史继续按现有身份规则复用，重启清零。

监听地址／共享并发容量不能在线修改；拒绝后的草稿不会因重启自动生效，本轮需以修订种子另建数据库。活跃或未清空的 rate/quota/window 规则继续遵守候选拒绝约束。serve 不通过 SIGHUP 重新导入种子。完整错误码、权限、超时及恢复操作见[中文服务指南](../standalone/rust-serve_CN.md)和[英文服务指南](../standalone/rust-serve.md)。

首轮完成时 G10 仍为部分完成：实体 CRUD 在第 34 节补齐；WebUI、OAuth、Postgres、旧数据导入、持久化预算／观测和多副本协调继续待交付。数据面 `/readyz` 继续表示 Host lease 可用，不代表数据库健康或已发布版本已激活。旧 Server、Tauri、Tools 及发布流程保留，整体迁移完成后删除本文，不归档；`docs/superpowers/` 保持忽略。

本地验证入口为 `cargo test -p nyro-control -p nyro --offline`、`cargo build -p nyro --offline` 和 `python3 tests/serve_smoke.py`。[真实进程回归](../../tests/serve_smoke.py)使用临时 SQLite 与本地 mock，覆盖认证、草稿与发布分离、版本冲突、拒绝、旧 SSE 和重启恢复；文件模式继续由 [SIGHUP 回归](../../tests/proxy_reload_smoke.py)检查。


本轮实际执行：

```sh
cargo test -p nyro -p nyro-control -p nyro-config --offline -- --test-threads=4
cargo clippy -p nyro -p nyro-control -p nyro-config --all-targets --offline -- -D warnings
cargo build -p nyro --offline
python3 tests/serve_smoke.py
python3 tests/proxy_reload_smoke.py
cargo fmt --all -- --check
```

**59 项 Rust 测试通过，零失败**；Clippy、根构建、serve 真实进程与既有 proxy 重载回归通过。仓储覆盖重开恢复、版本冲突、无效／超限配置、旧库／损坏 schema／特殊文件拒绝、独占、Unix 权限及真实 SQLite 只读写失败；管理 API 覆盖认证、大小限制、并发比较保存、调用方断开、等待超时后继续执行，以及已提交但激活失败的快照重开和 Host 恢复。新进程用例先在缺少 `serve` 的二进制上失败，实现后通过。独立审查提出的权限、超时状态和启动 FIFO 问题已修正并复核；没有修改 kernel 或 LLM 业务实现。

按仓库约束通过 `nyro-tools dump-schema --backend postgres/mysql` 重新生成两份旧数据库参考 schema，结果与已纳管内容一致；新控制库只支持 SQLite，未向旧表添加字段。本轮不涉及真实厂商、WebUI 或旧用户数据迁移。


## 34. G10 实体草稿管理与凭证脱敏查询

在已合并的 PR #352 基础上，复用现有 `nyro-control`、根 `serve` 和 SQLite v1 完整快照，不新增 crate、实体表或数据库 schema。[控制库实体模块](../../crates/nyro-control/src/entity.rs)拥有类型化编辑命令、凭证变更、引用检查与查询投影；[根 HTTP 适配](../../src/control/entity.rs)解析请求后交给既有服务持有队列。业务规则不进入 kernel、LLM 请求路径或 HTTP handler。

`/admin/providers`、`/admin/models`、`/admin/api-keys` 支持列表／新建和单项读取／替换／删除。列表按 ID 排序，返回 `{draft_revision, items: [{id, value}]}`；详情返回 `{draft_revision, item: {id, value}}`。新建请求为 `{expected_revision, id, value}`，完成返回 `201 {draft_revision}`；替换为 `{expected_revision, value}`，删除为 `{expected_revision}`，均完成返回 `200 {draft_revision}`。路径 ID 整段 URL 编码，包含 `/` 时使用 `%2F`；不提供重命名。

实体编辑和完整配置 PUT 共用全局草稿版本、完整配置校验和原保存路径，不隐式发布。元数据采用完整替换及默认值，只有凭证省略时保留。凭证输入为 `{action: "keep"}`、`{action: "set", value: "..."}`、`{action: "clear"}`；`null` 和未知字段拒绝。Provider 必填 `kind/base_url`，其 API Key 与代理 URL 可清除；API Key 新建必须 Set，已有 secret 可省略保留，不可 Clear；`enabled` 默认 true，`expires_at` 省略／null 为不过期。API Key DTO 不编辑限额，`llm.subject_limits` 继续通过导出、编辑、完整配置 PUT 修改。

删除被任意 backend 引用的 Provider（包括零权重引用），或被任意模型 subjects／subject_limits 引用的 API Key，返回 `409 entity_referenced`，不级联。删除最后一个模型因完整配置无效返回 `422 invalid_config`。重复 ID 返回 `409 entity_exists`，版本过期返回 `409 revision_conflict`，不存在返回 `404 not_found`；拒绝不改变草稿和发布状态。

普通 `GET /admin/config` 和实体查询以 `has_api_key`、`has_proxy_url`、`has_secret` 表示凭证存在，不返回掩码字符串或整个代理 URL。读取投影不是写入 DTO，未知 `has_*` 字段被拒绝，前端不能直接读后原样写回。显式 `GET /admin/config/export` 保留原 `{draft, published_revision, active_revision, publication}` 结构和明文完整配置，沿用同一管理认证与 `no-store`，属于敏感导出。文件和 SQLite 仍保存原始凭证。中英文服务指南的完整配置往返示例已改为 `/export`，实体示例无需导出密钥。

所有新路径复用既有管理认证、1 MiB 正文上限、15 秒正文读取期限、16 个操作准入、10 秒等待及断连后继续执行契约。编辑只保存草稿；显式发布、失败边界、等价配置、SSE lease、共享限制状态和重启恢复沿用第 33 节。完整请求、默认值与恢复说明见[中文服务指南](../standalone/rust-serve_CN.md)和[英文服务指南](../standalone/rust-serve.md)。

本轮回归入口为[控制库实体测试](../../crates/nyro-control/tests/entity.rs)、[管理 API 测试](../../src/control/tests.rs)及[真实 serve 进程回归](../../tests/serve_smoke.py)。G10 仍部分完成，WebUI、Postgres、OAuth、旧数据导入、持久化预算／观测、多副本协调及产品切换继续跟踪。此清单仅维护这一份中文临时文档，整体迁移完成后删除，不归档；`docs/superpowers/` 继续保持忽略。


本轮实际执行：

```sh
cargo test -p nyro -p nyro-control -p nyro-config --offline -- --test-threads=4
cargo clippy -p nyro -p nyro-control -p nyro-config --all-targets --offline -- -D warnings
cargo build -p nyro --offline
python3 tests/serve_smoke.py
cargo fmt --all -- --check
```

**69 项 Rust 测试通过，零失败**；Clippy、根构建、格式检查和真实 serve 进程回归通过。新增实体测试覆盖凭证更新、脱敏、引用保护、版本冲突、拒绝原子性及 SQLite 重开；HTTP 测试覆盖认证、编码 ID、错误映射和显式导出。真实进程覆盖草稿密钥轮换在发布前不生效、发布后新密钥生效而旧密钥失效、旧 SSE 完成、重启恢复及日志脱敏。202 个本地文档链接有效，10 个中英文 shell 示例语法通过，`git diff --check` 通过。独立代码审查未发现待修复问题；没有新增 schema、真实厂商请求或旧入口下线。
