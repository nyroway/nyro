# 更新日志

Nyro 的所有重要变更均记录在此文件中。

---

## v1.8.3

> 发布于 2026-07-13

#### 新功能

- **请求生命周期 P0 脚手架** (#227)：交付请求生命周期 P0 脚手架，实现 `RequestContext` 端到端贯穿与类型化扩展袋
- **请求生命周期与扩展框架（P0 + P1）** (#229)：新增请求生命周期与扩展框架，包含 admin 扩展 API 与 `PluginKernel` 聚合
- **OnResponse/OnLog 生命周期阶段（P2 A1-A3）** (#230)：在 dispatch 管线中接线 `OnResponse` 与 `OnLog` 生命周期阶段
- **API 密钥级可观测性** (#237)：在管理 UI 中新增 API 密钥级可观测性，并修复缓存命中率上报
- **日志与统计增强** (#239)：改进日志与统计展示，并修复指标计算问题

#### 改进 / 重构

- **架构文档** (#232)：刷新 `architecture.md`，与当前代码库对齐
- **WebUI 扩展页** (#260)：移除无用户可操作能力的只读扩展页

#### 修复

- **Anthropic 转 OpenAI 函数调用** (#233)：修复 Anthropic 请求转 OpenAI 时的函数调用失败
- **日志页提供商筛选** (#234)：修复日志页提供商筛选显示空日志的问题

---

## v1.8.2

> 发布于 2026-06-18

#### 修复

- **Codec extra 字段透传** (#225)：透传 extra 字段时跳过跨协议的内部键
- **访问控制开关文案** (#224)：明确 WebUI 中访问控制开关文案，描述其当前状态
- **pnpm 10+ 下 WebUI 构建** (#223)：放行 pnpm 10+ 的 esbuild 构建脚本，并将 CI 的 pnpm 升级到 11（Node 22）

---

## v1.8.1

> 发布于 2026-06-15

#### 新功能

- **服务端部署模式** (#206)：新增 `--mode` 启动参数和 `embed-webui` Cargo feature，支持服务端分布式部署
- **K8S 外部数据库迁移** (#207)：支持 K8S 环境下外部数据库迁移工作流，适配生产环境部署
- **清空所有日志** (#216)：WebUI 中新增一键清空全部请求日志功能

#### 改进 / 重构

- **Makefile 构建工具** (#210, #213)：新增 `build nyro-tools` 和 `cargo test` Makefile 命令
- **Clippy 警告清理** (#218)：修复 nyro-core 和 nyro-server 中全部 clippy 警告
- **发版文档** (#219)：新增本地发版操作手册和 AGENTS 发版流程文档

#### 修复

- **Gemini from_uri 内容缺失** (#205)：修复 Gemini 处理时 `from_uri` 消息内容被跳过的问题
- **OpenAI responses 工具调用解析** (#212)：修复 OpenAI responses API 工具调用完成事件解析错误
- **Desktop 执行测试导入错误** (#214)：修复 desktop 执行测试中的导入错误
- **Anthropic 内联 system 消息** (#217)：支持 Claude Code >=2.1.154 发送的内联 `system` 角色消息

---

## v1.8.0

> 发布于 2026-06-05

#### 新功能

- **MySQL 存储后端** (#196)：新增完整的 MySQL 存储实现，包含基础设施、配置、连接池及文档
- **多副本生产就绪** (#203)：新增配置 epoch 同步、健康探针和 webui-dir 支持，适配多副本部署场景
- **按模型 Payload 日志控制** (#195)：新增按模型的 Payload 日志开关，统一 `enable_*` 命名规范
- **优雅停机** (#192)：为服务端添加优雅停机处理
- **Gemini API 最大请求体** (#188)：为 Gemini API 代理添加可配置的最大请求体设置

#### 改进 / 重构

- **AI Gateway 术语统一** (#190)：将 `routes` 重命名为 `models`，全面对齐 AI 网关标准术语
- **请求日志字段重命名** (#191)：将 `request_logs` 中的 `route_id`/`route_name` → `model_id`/`model_name`，`strategy` → `balance`
- **虚拟模型字段合并** (#194)：将 `virtual_model` 合并到 `name` 字段，简化数据模型
- **响应缓存移除** (#187)：移除精确匹配和语义相似度响应缓存模块
- **加密模块清理** (#198)：移除未使用的 AES-256-GCM 加密模块和过期的 sqlite-vec CI 步骤
- **未用依赖清理** (#199)：移除 8 个未使用的 Cargo 依赖
- **WebUI 模型标签样式** (#197)：模型列表中的模型名使用 code label 样式

#### 修复

- **Gemini 代理认证和流式处理** (#186)：修复原生 Gemini 代理认证和流式传输行为
- **MySQL SUM 类型兼容** (#201)：将 MySQL `SUM()` 结果转换为 `SIGNED`，确保 Rust 中 i64 类型映射正确
- **MySQL AVG 类型兼容** (#202)：将 MySQL `AVG()` 结果转换为 `DOUBLE`，确保 Rust 中 f64 类型映射正确
- **Storage E2E MySQL 后端** (#200)：同步 Storage E2E 测试字段重命名并新增 MySQL 后端测试

---

## v1.7.6

> 发布于 2026-05-22

#### 新功能

- **Vertex AI Provider 支持** (#172)：新增 Vertex AI 内置 Provider，修复 Gemini 入口认证处理
- **Provider 复制工作流** (#173)：支持一键复制已有 Provider 配置
- **Provider 复制时继承路由目标** (#178)：复制 Provider 时自动关联原 Provider 的路由目标
- **客户端缓存提示透传** (#181)：将客户端请求中的 `anthropic-beta` 缓存控制提示转发至上游 Provider，同时不泄露客户端身份头
- **上游错误信息保留** (#184)：代理转发失败时捕获并展示上游响应体和状态码，改善调试可见性
- **Gemini 流式 JSON 处理** (#182)：处理 Gemini 流式端点返回的非流式 JSON 响应，将其解析到统一 IR 流式管线中

#### 改进 / 重构

- **Protocol 标识统一** (#174)：将不透明的短代码替换为描述性的规范 Protocol 标识符
- **GoogleGenerativeAI → GoogleGemini 重命名** (#175)：重命名协议枚举变体并统一所有 Google/Gemini 端点常量
- **Codec 目录重组** (#176)：将 Codec 模块按 `vendor/endpoint` 目录结构重组，与协议边界对齐
- **Admin 模块化** (#180)：将单体 Admin Handler 拆分为专注的子模块，实现更清晰的职责分离

#### 修复

- **Storage E2E 模型同步** (#167)：对齐 Storage E2E 测试与当前模型定义
- **Storage E2E 认证模式** (#168)：在 Storage E2E 测试中使用有效的 `apikey` 认证模式
- **Storage E2E Token 检查** (#169)：移除 Storage E2E 测试中不可靠的 `total_output_tokens` 断言
- **Postgres api_keys INSERT** (#170)：移除 api_keys INSERT 语句中过时的 `status` 列
- **Postgres AVG() f64 兼容** (#171)：将 `AVG()` 结果转换为 `FLOAT8`，确保 Rust 中 f64 类型映射正确

---

## v1.7.5

> 发布于 2026-05-19

#### 修复

- **Ingress decode 失败日志记录** (#164)：Anthropic Messages、OpenAI-compatible Chat Completions / Embeddings / Responses 与 Google Generate Content 入口在请求 decode 失败时，现在会在应用内日志模块记录请求元数据与 400 响应
- **Anthropic context management beta 兼容** (#165)：兼容 Anthropic `context-management-2025-06-27` beta 发送的 `context_management` 请求形状，将其作为透传 JSON 保留，不再因缺少旧的外层 `type` 字段而返回 400

---

## v1.7.4

> 发布于 2026-05-19

#### 改进 / 重构

- **Provider 配置简化** (#160)：将 Provider 存储与 WebUI 从多协议 endpoint 映射收敛为单一 `protocol` / `base_url` 配置，并保留旧数据迁移支持，同时同步 standalone 配置文档与测试

#### 修复

- **OpenAI-compatible 流式完成事件** (#162)：当同时收到 `finish_reason` 和 `[DONE]` 时去重终止 `Done` 事件，避免客户端收到重复的流式结束通知

---

## v1.7.3

> 发布于 2026-05-18

#### 功能

- **IR 与协议 codec 流水线重构** (#145–#153)：围绕 `AiRequest`、`AiResponse`、`AiStreamDelta`、扩展后的 `ContentBlock`、`AiError`、`CacheControl` 和 `ProtocolExt` 重塑内部请求/响应表示；入口 decoder、出口 encoder、响应/流式 parser、dispatcher、provider adapter 与 cache 流程均改为直接消费新 IR；移除旧 `InternalRequest` / `InternalResponse` 路径，并将 codec trait 命名统一到 `Decoder` / `Encoder`
- **请求日志元数据增强** (#154)：请求日志新增持久化 `provider_name`、`api_key_name`、`route_id`、`route_name`，并恢复 `is_stream` 记录，便于区分流式/非流式请求
- **Prompt cache usage 统计** (#156)：补齐 Chat Completions prompt-cache 命中 token 采集，确保 cache-read usage 能进入下游统计

#### 修复

- **协议转换思考内容保留** (#157)：将 Anthropic thinking block 桥接到 OpenAI-compatible `reasoning_content`
- **OpenAI Responses 流式 usage** (#140)：修复流式 usage delta 中 `input_tokens` 丢失的问题
- **WebUI 日志详情加载** (#139)：将 `backend("get_log")` 正确映射到 `GET /api/v1/logs/:id`
- **Provider 图标显示** (#133, #134)：修复新增/编辑 Provider 时图标为 `undefined`，以及自定义空图标显示异常的问题
- **macOS 应用生命周期** (#132)：点击 Dock 图标时重新打开主窗口
- **musl 构建告警** (#130)：消除 musl 构建下的 dead-code warning

#### 重构 / 内部

- 将 proxy ingress 代码按协议拆分到独立子目录 (#135)
- 用类型化 `CapabilitiesSource` preset 替代 `capabilities_source` 字符串处理 (#136)
- 移除 route 处理中的 `route_type` 字段和 endpoint subset filtering (#137)
- 拆分 dispatcher 内部结构，引入 `CallCtx`、`CacheWriteCtx`、`RequestExtras`、`LogBuilder`、integrations hooks、routing strategies 与模块重命名 (#141–#143)
- 新增 IR field-homing 设计骨架，并补充弃用告警、测试与文档清理 (#138, #144)

---

## v1.7.2

> 发布于 2026-05-12

#### 修复

- **musl 静态构建：消除 OpenSSL 依赖** (#125)：为工作区 `reqwest` 依赖添加 `default-features = false` 并切换至 `rustls-tls-native-roots`；根本原因是 `default-tls`（reqwest 默认 feature）会静默引入 `native-tls` → `openssl-sys` 依赖链，导致 `*-unknown-linux-musl` CI 构建失败；同时显式保留 `http2`、`charset`、`macos-system-configuration` 避免功能回归；所有平台的 TLS 引擎均保持 `rustls`，非 musl 目标继续使用系统原生证书库（Windows 证书库 / macOS Keychain / Linux `/etc/ssl/certs`），musl 静态二进制自动回退至打包的 Mozilla 根证书
- **musl 静态构建：sqlite-vec BSD 类型兼容** (#125)：在 CI musl 构建步骤中注入 `CFLAGS_<target>=-Du_int8_t=uint8_t -Du_int16_t=uint16_t -Du_int64_t=uint64_t`；`sqlite-vec v0.1.9` 的 C 源码使用了 POSIX 扩展类型 `u_int*_t`，这些类型在 musl libc 中不存在，通过 cc-rs 的 target-specific CFLAGS 机制注入宏定义使编译正常通过

---

## v1.7.1

> 发布于 2026-05-12

#### 功能

- **Linux musl 静态构建支持** (#123)：新增 `x86_64-unknown-linux-musl` 和 `aarch64-unknown-linux-musl` 发布目标；将 sqlx 切换为 `tls-rustls` feature，彻底消除对 OpenSSL 运行时的依赖；在 `crypto/mod.rs` 中新增 `cfg(target_env = "musl")` 分支，通过环境变量 / 文件路径回退的方式解析主密钥（规避 musl 静态链接 dbus/libsecret 的问题）

#### 修复

- **ARM Linux sqlite-vec 扩展 ABI 修复** (#121)：在 `sqlite3_auto_extension` 注册调用中改用平台原生的 `c_char` / `c_int` 类型，确保符号签名与 aarch64 Linux 上的 libsqlite3-sys ABI 匹配

#### 内部

- 对整个代码库执行 `rustfmt` 统一格式化；在 `Makefile` 中新增 `make fmt` / `make fmt-check` 目标 (#124)

---

## v1.7.0

> 发布于 2026-05-12

#### 功能

- **系统托盘生命周期修复** (#118)：关闭窗口时改为隐藏到托盘而非退出应用；修复 `TrayIcon` 因所有权提前释放导致托盘消失的 bug（通过 `app.manage()` 管理生命周期）；点击托盘图标可恢复窗口
- **nyro-tools proxy 子命令重写** (#111)：将 `--upstream-protocol` + `--upstream-endpoint` 合并为单一 `--url`（`-u`）参数；自动检测并剥离出口 URL 中的客户端版本前缀；仅转发已知 LLM 入口路径；新增结构化 JSON 日志（含 UUID 关联 ID 和四种协议的 SSE 聚合）；新增 `-o/--output` 指定日志输出文件和 `-l/--log-mode`（all|req|resp）过滤模式
- **Claude Code OAuth 重构接入** (#101)：新增 `auth/drivers/claude.rs` PKCE 驱动，通过 vendor-registry 流水线注册；新增 `anthropic/claude-code` channel，auth header 完全由 OAuth 驱动管理；引入 `compose_upstream_headers` 统一处理四处上游调用点的"OAuth 驱动优先于默认 auth"不变量；通过回归测试锁定该不变量
- **Codex OAuth Provider 流程** (#58)：新增完整 OAuth 凭证支持、Codex OAuth channel，并接入代理与 Tauri 运行时
- **三层 CI 测试金字塔** (#84)：Phase 1 — 协议转换单测（tool-call 片段、Anthropic 思考增量、DeepSeek reasoning、Responses 输出项、工具关联）；Phase 2 — 构建产物 job + L3 Ollama E2E（7 链路）；Phase 3 — L2 aimock 静态 E2E（4 隔离实例 / 8 测试用例）；Phase 4 — smoke 测试迁移至 `tests/e2e/`，新增 `storage-backends.yml`（pgvector 每日定时）
- **Protocol / ProtocolEndpoint / Vendor 三概念正交模型** (#89–#97, #119)：用清晰的三层身份体系取代歧义的 `ProtocolFamily`——`Protocol`（枚举：`OpenAICompatible` / `OpenAIResponses` / `AnthropicMessages` / `GoogleGenerativeAI`）表示 wire-format 协议套件，`ProtocolEndpoint`（`{protocol, name, version}`）标识具体 API 端点，`Vendor` 复用现有 `Provider.vendor`；配置/JSON 使用规范短名（`openai-compat`、`openai-resps`、`anthropic-msgs`、`google-genai`）；三层 alias 表保障完全向后兼容（旧 canonical 字符串、遗留品牌名 `openai`/`claude`/`gemini`、短别名），无需数据迁移；`protocol_endpoints` JSON 升级为 protocol-keyed 格式（`base_url` 移至协议层、可选 `endpoints` 子集数组），首次读取时通过 `normalize_endpoints_json` 自动迁移
- **上游响应头捕获入日志**：`call_non_stream` 返回类型升级为 `(Value, u16, HeaderMap)`，在 `.json()` 消费响应前保存响应头；三条代理路径（JSON 非流式、SSE 流式、force-stream）均捕获上游响应头并持久化至日志 `response_headers` 字段
- **根路径健康检查端点**：`GET /` 和 `HEAD /` 均返回 `{"status":"ok"}`，支持默认使用 `HEAD /` 探活的负载均衡器和 Kubernetes liveness probe

#### 重构

- **Provider 层全面重构** (#107)：合并 `ProviderAdapter` + `VendorExtension` 为统一 `Vendor` trait（通过 `VendorRegistry`）；通过 `negotiate()` 激活 PassThrough 快速路径（ingress == egress 协议时跳过 IR 编解码往返）；`dispatch_pipeline` 接口改为 `RawEnvelope` + `AiRequest`；`dispatcher.rs` 拆分为 `mod.rs` + `util.rs` + `accumulator.rs`；`Gateway` 运行时字段从 `RwLock` 迁移至 `ArcSwap`，消除热路径 `.await`；CODEC_SCHEMA_VERSION 升至 2
- **内核稳定化** (#104)：统一 `GatewayError` 分类体系（15 个变体，稳定错误码）；`RequestContext` 生命周期追踪；可观测性与安全鉴权逻辑从 `handler.rs` 拆分至独立模块；`dispatcher.rs` 整合为单一编排层
- **OAuth 凭证存储** (#82)：将 OAuth 凭证拆分至专用 `provider_oauth_credentials` 表，实现 CAS 状态机（`connected` / `refreshing` / `error`）和乐观锁（`status_version`）；`OAuthCredentialStore` trait 提供 8 个方法，实现覆盖 SQLite、PostgreSQL、Memory 三端；将 `access_token` / `refresh_token` / `expires_at` 从 `Provider` 结构体移除；启动时自动迁移现有 OAuth 数据；后台刷新改为 `list_expiring()` + CAS 机制
- **codec 目录按协议重新组织**：移除旧 `codec/openai/`、`codec/anthropic/`、`codec/google/` 目录；替换为完全自包含的 `codec/openai_compatible/`、`codec/openai_responses/`、`codec/anthropic_messages/`、`codec/google_generative/`
- **Trait 与类型重命名**：`ProtocolHandler` → `EndpointHandler`；`ProtocolCapabilities` → `EndpointCapabilities`；`ProtocolRegistration` → `EndpointRegistration`；`list_by_family` → `list_by_protocol`；保留 `pub use` 向后兼容别名；移除 `ProtocolFamily` 和 `VendorScope::Family`
- **authMode 字段规范化** (#73)：预设 JSON 字段 `auth_mode` → `authMode`，值 `"api_key"` → `"apikey"`，覆盖 JSON/DB/Rust/TypeScript；新增 SQLite/Postgres 启动迁移；调整 Provider 创建/编辑 OAuth 面板布局
- **`protocol-id.ts` 替换为 `protocol.ts`**：新增 `PROTOCOL_TABLE`、`PROTOCOL_ALIASES`、`resolveProtocol`、`parseProtocolEndpoint`；`prettyName` 仅返回 Protocol 显示名；Providers/Connect/Routes 页面全面对齐规范 ID

#### 修复

- 修复协议转换过程中思考元数据丢失问题 (#114)
- 修复 Anthropic 完整 usage 字段丢失；为 ZhipuAI/MiniMax 启用原生 passthrough (#115)
- 修复流式 passthrough 错误传播与 `RawEvent` 转发 (#112)
- `passthrough_run` 现正确将虚拟模型别名替换为 `actual_model` (#109)
- 修复 DeepSeek 思考模式下 `reasoning_content` 在代理中丢失的问题 (#98)
- 修复 OpenAI-compat vendor 在 Anthropic egress 时 URL 与鉴权头构造错误 (#105)
- 修复 mlx-lm `reasoning` 字段名处理；非流式响应中正确包含 `reasoning_content` (#103)
- 修复 Anthropic Thinking block 在网关中丢失的问题 (#90)
- 修复 `text-slate-800` 在深色模式下对比度异常 (#100)
- 修复运行时 Docker 构建缺失 lock 文件与目录 (#71)

---

## v1.6.2

> 发布于 2026-04-19

#### 功能

- **请求/响应载荷日志**：扩展 `request_logs` 表结构，新增 `method`、`path`、`request_headers`、`request_body`、`response_headers`、`response_body` 字段，SQLite 与 Postgres 均提供非破坏性迁移（`ensure_request_log_column` / `ALTER TABLE IF NOT EXISTS`）；在 universal、Gemini、Embeddings 代理入口统一捕获入口请求方法/路径/头部/Body；流式响应聚合为完整 JSON 后持久化为 `response_body`；所有早退出路径（解码失败、无路由、鉴权失败、上游错误、缓存回退）均落库完整上下文；缓存命中路径同样携带完整请求/响应 Body；Embeddings 解析 `usage.prompt_tokens` 作为 `input_tokens`
- **日志查看器重构**：紧凑 7 列列表（时间 / 状态 / 模型 / 协议 / 延迟 / Token / 类型），行点击打开详情；新增 `LogDetailDialog`，含元信息头部与 4 个可复制的请求/响应头与 Body 面板，通过 `get_log(id)` 按需懒加载完整载荷并格式化 JSON；Token 以 IN/OUT 标签与 K/M 格式展示（<1000 原值、<1M 保留一位 K、≥1M 保留两位 M）；SSE（绿）/JSON（天蓝）类型徽标替代布尔 stream 列；设置页将日志配置拆分为独立半宽卡片，与代理配置并列，新增 HelpCircle 提示
- **日志载荷持久化开关**：新增 `log_record_payloads` 设置项（默认 `true`），可在敏感数据场景下关闭请求/响应 Body 的存储

#### 改进

- **Standalone Provider 配置体验优化**：`default_protocol` 改为可选，未设置时自动取 `endpoints` 声明顺序的首个协议；新增别名 `protocol`（对应 `default_protocol`）与 `apikey`（对应 `api_key`）；`endpoints` 切换为 `IndexMap` 保留 YAML 声明顺序；通过 `YamlProviderRaw` + `TryFrom` 在反序列化阶段拒绝规范名与别名同时出现（`default_protocol` + `protocol`、`api_key` + `apikey`）；多 endpoint 下未显式声明协议时输出 WARN 日志提示
- **日志保留默认值调整**：`DEFAULT_RETENTION_DAYS` 30 → 7 天、批大小 64 → 32、清理周期 60s → 600s，降低存储增长与清理抖动
- **日志接口拆分**：列表 `query_logs` 现已剔除重型字段（bodies/headers 置 NULL）；新增 `get_log(id)` 接口按需拉取完整载荷

#### 修复

- 修复 `release-server` 工作流编译期缺失 `webui/dist` 的问题：`#[derive(RustEmbed)]` 展开失败导致 `WebUiAssets::get` 缺失；新增 Node 20 + pnpm 9 安装步骤，并在 `cargo build` 之前执行 `pnpm -C webui install/build`

---

## v1.6.1

> 发布于 2026-04-14

#### 功能

- **流式缓存重放 TPS 限速**：在 `ExactCacheConfig` 和 `SemanticCacheConfig` 中新增 `stream_replay_tps`（默认 100）；设为 `0` 可禁用限速并恢复即时输出行为；实现 `split_text_deltas` 辅助函数，将较大的 `TextDelta`/`ReasoningDelta` 切分为约 1 Token 的小块以平滑逐 Token 输出节奏；首个 SSE chunk 始终立即下发，保证 TTFT 为零
- **独立缓存响应头控制**：在两种缓存配置中新增 `expose_headers`（默认 `true`），可分别控制 exact 和 semantic 缓存命中时是否下发 `X-NYRO-CACHE-*` 响应头；响应头统一改为全大写：`X-NYRO-CACHE` / `X-NYRO-CACHE-KEY` / `X-NYRO-CACHE-SCORE`
- **WebUI 内嵌至服务端二进制**：移除 `--webui-dir` CLI 参数，通过 `rust-embed` 将 `webui/dist` 直接内嵌到二进制文件中；新增 `--log-level` 参数（环境变量 `NYRO_LOG_LEVEL`）替代硬编码的 tracing 过滤规则；关键参数支持环境变量（`NYRO_PROXY_HOST`、`NYRO_ADMIN_TOKEN` 等）
- **浏览器 Token 鉴权**：新增 `/login` 页面及浏览器 WebUI 的 Token 鉴权流程；管理 Token 生效时 WebUI 顶栏显示登出图标（Tauri IPC 路径不受影响）
- **资源启用/禁用切换**：在 WebUI 的 Provider、路由、API Key 列表页新增启用/禁用切换按钮；仅在资源被禁用时显示危险徽标

#### 改进

- **服务端 CLI 精简**：CLI 参数从 27 个减少至 18 个；`--admin-key` 重命名为 `--admin-token`，`--storage-dsn-env` 重命名为 `--postgres-dsn`；PostgreSQL 连接池参数统一加 `postgres-` 前缀；`--sqlite-migrate-on-start` 重命名为 `--migrate-on-start`；移除 9 个缓存相关 CLI 参数（现通过 Admin API / WebUI + DB 管理）
- **状态字段统一**：将 `providers.is_active`、`routes.is_active`、`api_keys.status` 统一重命名为 `is_enabled`（BOOLEAN 类型），覆盖所有存储后端、SQL 查询、Rust 模型与 WebUI；同时为 SQLite 和 PostgreSQL 提供非破坏性 schema 迁移

#### 修复

- 修复 feat #45 引入的两个新字段 `stream_replay_tps` 和 `expose_headers` 未在 nyro-server 缓存配置初始化代码（`main.rs` 和 `yaml_config.rs`）中添加，导致 CI 编译失败
- 修复 standalone 模式下代理 host/port 优先级 bug：CLI 传入值被默认值静默覆盖
- 修复 `backend.ts` 中 null-data bug：当 `data` 为 `null` 时，`json.data ?? json` 返回完整响应对象，导致 Provider 与 Settings 页面调用 `.trim()` 时崩溃

#### 文档

- 将 8 个过时设计文档合并为单一 `docs/design/architecture.md`
- 新增 `docs/standalone/` 目录，包含完整的 Standalone 模式使用指南及缓存章节
- 删除 `examples/` 目录（内容已整合至 standalone 文档）
- 修复 `docs/server/README.md`、`README.md`、`README_CN.md` 中的过时 CLI 参数说明

---

## v1.6.0

> 发布于 2026-04-12

#### 功能

- **端到端缓存系统**：实现模块化 exact/semantic 缓存后端，支持流式响应 SSE 缓存重放与 singleflight 请求合并，防止并发场景下的缓存穿透
- **路由入口别名**：新增无版本前缀的路由别名（`/chat/completions`、`/messages`、`/responses`、`/models/:model_action`），提升客户端兼容性
- **OpenAI 兼容模型列表接口**：新增 `/v1/models` 和 `/models` 接口，支持按路由返回模型列表，可按 API Key 过滤已绑定模型，并在 Key 无效时优雅降级为公开列表
- **语义向量维度生命周期管理**：Embedding 维度变更时自动重建向量表，持久化当前维度到配置，PostgreSQL 后端支持事务性重建并提供权限不足时的清晰引导说明

#### 改进

- **缓存系统统一**：统一 exact/semantic 缓存运行时配置与热重载行为；强制 chat/embedding 路由类型隔离，通过 OpenAI 端点校验保障路由安全；同步更新 WebUI 路由与设置流程
- **设置页保存体验升级**：重构设置模块为显式保存操作，增加未保存变更提示；将 API Key 状态拆分为管理状态与有效性标识；对齐 SQLite 语义缓存相似度评分与余弦距离预期
- **全局缓存/代理联动完善**：路由列表徽标与 Provider 列表代理徽标现根据全局开关联动；路由表单在全局缓存关闭时隐藏缓存控件，Provider 表单在全局代理关闭时隐藏代理开关；已保存配置在重新开启时自动恢复
- **语义缓存配置联动**：关闭语义缓存开关时自动清空 `embedding_route` 字段；被语义缓存引用的 embedding 路由禁止删除，并弹出错误提示引导用户先解除依赖

#### 修复

- 修复缓存命中日志中模型名显示不一致：在缓存条目中持久化 `actual_model`，保证命中日志上报真实的上游模型
- 修复全局缓存/代理开关与路由列表徽标及 Provider 代理徽标缺乏联动的问题
- 修复全局代理关闭后，启用了 `use_proxy=true` 的路由返回 502 的问题；改为自动降级为直连 HTTP 客户端
- 统一 UI 与文档中的缓存术语表述，不改变现有配置 Key 名称

#### 重构与清理

- **移除 MySQL 后端**：下线 MySQL 存储实现、配置/CLI 路径及 sqlx mysql feature；当前支持后端为 SQLite / PostgreSQL / Memory
- **GitHub 组织名更新**：将所有 `NYRO-WAY` 引用统一更新为 `nyroway`，涵盖配置、脚本、文档、安装脚本与前端代码
- 更新 Zai Provider 默认能力来源配置

---

## v1.5.0

> 发布于 2026-04-02

#### 功能

- **存储后端能力扩展**：新增多后端存储抽象，并在服务端提供 SQLite / MySQL / PostgreSQL 后端配置能力
- **多目标路由能力演进**：引入多目标路由选择与 weighted/priority 策略链路，支持 `weight=0` 作为显式禁用值
- **网关协议架构升级**：支持多协议 Provider、协议无关的路由行为，以及 standalone YAML 路由/Provider 加载
- **代理扩展性增强**：抽离 `ProviderAdapter`，并对齐 Provider 级代理控制逻辑，便于后续接入扩展

#### 改进

- **废弃字段统一清理**：移除路由/Provider/日志/存储中的历史遗留字段，简化现行路由链路相关 schema 与查询逻辑
- **网关错误类型标准化**：统一 proxy/auth 返回中的错误 `type` 为 `NYRO_*` 命名，便于客户端稳定识别与处理
- **CLI 接入体验优化**：改进 Web CLI 配置预览，并在 Claude Code 同步配置中加入 `CLAUDE_CODE_NO_FLICKER=1`
- **仓库迁移一致性完善**：将项目与发版引用统一迁移到 `NYRO-WAY` 组织，并同步 updater/发版脚本路径
- **构建与运行时结构整理**：拆分 Docker runtime 镜像与开发容器结构，降低 CI/CD 维护复杂度

#### 测试与文档

- 更新 smoke 测试与文档内容，使其与协议无关路由及最新 route/provider 数据模型保持一致

## v1.4.0

> 发布于 2026-03-21

#### 功能

- **协议归一化层升级**：新增语义级内部响应归一化，并在 Responses API 链路输出 item 级 reasoning / function-call 结果
- **Provider 预设能力统一**：统一 Provider 预设与能力源解析逻辑，并内置 models.dev 快照用于离线元数据
- **Connect CLI 流程增强**：Codex/OpenCode 同步输出与运行时默认配置对齐，优化路由状态锚定与配置动作体验

#### 改进

- **WebUI 配置交互优化**：细化 Provider 预设行为与路由编辑模型交互，提升管理流可预期性
- **管理端错误一致性增强**：后端返回结构化 provider/route 冲突信息，前端同步完成冲突错误本地化
- **CLI 面板布局打磨**：调整 API Key 与更新配置区块顺序，保持动作区半宽布局，并统一预览提示对齐与间距
- **本地体验默认值优化**：初始语言默认 `en-US`，日志页面请求时间按本地时区显示

#### 修复

- 修复跨协议工具调用语义问题：加强 tool-call/result 关联，并统一各适配器的 thinking/text delta 处理
- 修复 Google 模型发现鉴权路径与模型归一化问题，恢复管理端发现链路稳定性

#### 测试与文档

- 新增协议回归覆盖：tool id、finish reason、schema 映射与 provider-policy 移除相关行为
- 新增协议架构加固设计文档，并更新 README 与控制台 UI 截图

## v1.3.0

> 发布于 2026-03-18

#### 功能

- **新增 OpenAI Responses 转换链路**：支持 `/v1/responses` 请求/响应转换，提升与新一代 OpenAI 风格客户端的兼容性
- **Provider 模型测试流程升级**：引入分阶段测试流程，并统一 Provider 管理页动作反馈
- **新增 Ollama 能力探测**：按供应商与模型能力动态检查，自动处理工具调用支持差异
- **Gemini cURL 示例优化**：Connect 页生成 Gemini 示例时保留模型名中的 `:`（如 `gemma3:1b`）

#### 改进

- **Provider 交互一致性优化**：改进供应商/渠道同步逻辑，稳定路由编辑态重置行为
- **Route 模型发现行为优化**：仅在 Provider 配置了可用模型端点时启用发现下拉
- **管理页错误体验优化**：统一后端错误本地化与失败弹窗展示策略

#### 修复

- 修复 MiniMax + Codex 互通问题：针对 Responses API 入口规范化指令消息，避免上游因 `system` 角色拒绝请求
- 修复 OpenRouter 模型发现行为，并恢复 Provider 创建后的自动测试流程
- 修复 Windows 桌面端下拉/搜索选择异常：解决标题栏拖拽捕获与下拉点击事件冲突

## v1.2.0

> 发布于 2026-03-15

#### 功能

- **新增「接入」模块**：增加 `Connect` 页面与 `代码接入` / `CLI 接入` 双标签，支持按协议选择路由并生成 Python / TypeScript / cURL 示例
- **桌面端 CLI 集成**：支持 Claude Code、Codex CLI、Gemini CLI、OpenCode 的就绪检测与配置同步/恢复
- **CLI 配置预览与复制优化**：按文件展示将更新的片段，并在预览区域内置复制能力
- **API Key 权限模型升级**：受控路由改为默认拒绝（需显式绑定路由才可访问），并统一为 `sk-<32位hex>` 密钥格式
- **配额能力扩展**：新增 `RPD`（每天请求数），打通 API Key 数据模型、管理接口、前端表单与代理鉴权限流

#### 改进

- **API Key 页面重构**：创建/编辑表单按三段式重排（基本信息、访问权限、访问限额），统一宽度策略，编辑态下有效期与 Key 不可修改
- **Provider 表单优化**：API Key 输入支持显示/隐藏，修复编辑态 API Key 回显，并对齐创建/编辑布局行为
- **Route 表单一致性**：编辑布局与创建布局对齐，单行输入/下拉保持半宽展示
- **统计时间范围统一生效**：小时筛选覆盖概览、模型、提供商统计，并在 WebUI + 后端 + Tauri 命令链路保持一致

#### 修复

- 修复 `build-and-smoke` CI 对新鉴权流程的不兼容（移除过时 `--proxy-key`，改为创建并绑定 smoke API Key）
- 修复 CLI 同步参数命名不一致（`toolId` / `apiKey`），并增强前端错误信息解析
- 回退 Codex `wire_api` 为 `responses` 以兼容最新 CLI 行为
- 优化表单下拉/搜索面板视觉一致性与访问控制开关布局细节

#### CI 与发版

- 桌面端发版流程支持自动计算并回写 Homebrew Cask 校验和
- 更新路由/API Key 设计文档与安装说明，保持文档与实现一致

---

## v1.1.0

> 发布于 2026-03-13

#### 功能

- **路由匹配重构**：从模糊 `match_pattern` 切换为 `(ingress_protocol, virtual_model)` 精确匹配，支持 OpenAI / Anthropic / Gemini 接入
- **全新 API Key 体系**：新增 `api_keys` + `api_key_routes` 数据模型及完整 CRUD，默认密钥格式为 `sk-<32位hex>`
- **路由级访问控制**：先匹配路由，再在 `access_control` 开启时校验 API Key；支持按路由绑定或全局生效
- **API Key 配额能力**：在代理鉴权链路中新增 `RPM`、`TPM`、`TPD`、状态与过期时间校验

#### 改进

- **后端迁移与兼容处理**：
  - 新增并回填路由/Provider/日志字段（`ingress_protocol`、`virtual_model`、`access_control`、`channel`、`api_key_id`）
  - 现行流程移除旧的路由/Provider fallback 与 priority 机制
- **管理接口扩展**：服务端与 Tauri 管理 API/命令新增 API Key 管理能力
- **WebUI 路由与密钥体验升级**：
  - 新增 API Keys 页面，支持可搜索多选绑定路由
  - 创建路由时将提供商/模型同排展示，并自动将目标模型回填到虚拟模型
  - Provider 创建/编辑流程持久化并自动锚定供应商与渠道标识
- **UI 组件标准化**：引入并统一使用 shadcn 风格 `Badge`、`Switch`、`Checkbox`、`Dialog`、`Combobox`、`Command`、`Popover`、`MultiSelect`、`Tabs` 等组件
- **Provider 图标策略优化**：Provider 列表主图标优先展示供应商图标（亮色彩色、暗色纯色），协议胶囊图标保持协议维度
- **版本展示自动化**：设置页版本改为构建时注入，不再写死

#### 修复

- 修复搜索下拉面板背景透明导致内容混叠的问题
- 修复自定义下拉搜索过滤与 hover/高亮反馈问题
- Homebrew 安装文档改为标准 `brew install --cask nyro` 流程

#### 文档

- 新增路由与 API Key 设计文档：`docs/design/route-apikey.md`
- 新增 Provider Base URL/渠道设计说明：`docs/design/provider-base-urls.md`
- 更新 `README.md` 与 `README_CN.md` 安装命令及相关说明

---

## v1.0.1

> 发布于 2026-03-10

#### 改进

- **全平台 ARM64 / aarch64 原生构建**：使用 GitHub Actions ARM runner（`ubuntu-24.04-arm`、`windows-11-arm`、`macos-latest`）原生构建，零交叉编译
  - 桌面端：Linux aarch64 AppImage、Windows ARM64 NSIS 安装包
  - 服务端：Linux aarch64、macOS aarch64、Windows ARM64 二进制
- **macOS Intel 原生构建**：使用 `macos-15-intel` runner 原生编译，不再依赖 ARM 交叉编译
- **Homebrew Cask 支持**：`brew tap shuaijinchao/nyro && brew install --cask nyro`（独立 `homebrew-nyro` tap 仓库，发版自动同步版本）
- **一键安装脚本**：macOS/Linux（`install.sh`）和 Windows（`install.ps1`），macOS 自动移除隔离属性
- **前端 chunk 拆分**：Vite `manualChunks` 拆分 react/query/charts，消除 >500kB 打包警告

#### 修复

- **CI**：`cargo check --workspace` 排除 `nyro-desktop`，避免 Linux CI 依赖 GTK
- **CI**：移除 `cargo tauri build` 不支持的 `--manifest-path` 参数
- **CI**：添加 `pkg-config` 和 `libssl-dev` 依赖

#### 清理

- 移除桌面发布中的 MSI 和 deb 包（仅保留 NSIS + AppImage）
- 移除桌面 SHA256SUMS.txt（updater `.sig` 文件已提供完整性校验）
- Homebrew Cask 迁移至独立 `homebrew-nyro` 仓库
- 修复安装脚本和 README 中 `main` → `master` 分支引用

---

## v1.0.0

> 发布于 2026-03-09

Nyro AI Gateway 首个公开版本 — 从原 OpenResty/Lua API Gateway 完整重构为纯 Rust 本地 AI 协议网关。

#### 功能

- **多协议入口**：支持 OpenAI（`/v1/chat/completions`）、Anthropic（`/v1/messages`）、Gemini（`/v1beta/models/*/generateContent`），全协议支持流式（SSE）和非流式响应
- **任意上游出口**：可路由到任意 OpenAI 兼容、Anthropic、Gemini Provider
- **Provider 管理**：创建、编辑、删除 Provider，含 base URL 和加密 API Key
- **路由规则管理**：基于优先级的路由规则，支持模型覆盖和 Fallback Provider
- **请求日志持久化**：SQLite 存储，含协议、模型、延迟、状态码、Token 用量
- **用量统计看板**：概览仪表盘，含按小时/天图表和 Provider/模型维度分布
- **API Key 加密存储**：AES-256-GCM 加密静态存储
- **Bearer Token 鉴权**：代理层和管理层支持独立鉴权配置
- **桌面应用**：基于 Tauri v2 的跨平台桌面应用（macOS / Windows / Linux）
  - 系统托盘及快捷菜单
  - 可选开机自启
  - 应用内自动更新（Tauri updater）
  - macOS 原生标题栏融合
  - 深色/浅色模式切换
  - 中文/英文语言切换
- **服务端二进制**：独立 `nyro-server` 二进制，支持服务器部署，通过 HTTP 访问 WebUI
  - 代理端口和管理端口独立绑定地址配置
  - CORS 来源白名单配置
  - 非本地绑定时强制要求鉴权 Key
- **CI/CD**：GitHub Actions 自动化构建，支持跨平台桌面安装包和服务端二进制发布
