# Nyro IR 设计文档

> 状态：旧实现的设计记录，非本次 Rust 重构的目标规范（2026-09-07）。
>
> 本目录记录旧架构下 `AiRequest` / `AiResponse` 等 Internal Representation（IR）的设计决策与实现背景，代码位于 `crates/nyro-core/src/protocol/ir/`。新目标采用 `nyro_llm::Request` / `nyro_llm::Response` 入口枚举及 Chat / Embedding 配对类型，IR 保留在 `nyro-llm` 内；以 [Rust 目标架构](../architecture.md)（设计定稿，分阶段实施中）为权威依据。本文及目标文档均不表示代码已完成迁移。
>
> 以下正文及目录中的历史决策保留原有语境，不作为本次重构目标的权威依据。

---

## 目录结构

| 文件 | 用途 |
|------|------|
| [FIELD_HOMING.md](./FIELD_HOMING.md) | **字段归属决策表**。4 协议（OpenAI Chat / OpenAI Responses / Anthropic / Google）所有请求字段的归属仲裁：IR 核心、ProtocolExt、VendorBag 还是 DROP。PR-1 编码前的权威参考。 |
| [CHANGELOG.md](./CHANGELOG.md) | **IR 演进日志**。每个 PR 合并后追加，记录新增/变更/删除的字段和类型。 |
| [README.md](./README.md) | 本文件。导航与设计概览。 |

---

## IR 架构概览

```
AiRequest
├── model: String
├── messages: Vec<Message>          ← 统一消息列表，含多模态 ContentBlock
├── system: Option<SystemContent>   ← 系统提示（string 或 ContentBlock[]）
├── tools: Vec<ToolSpec>            ← 用户自定义工具（ProtocolExt 存 server tools）
├── tool_config: ToolConfig         ← choice / parallel / disable_parallel
├── generation: GenerationConfig    ← temperature / top_p / max_tokens / stop / ...
├── reasoning: Option<ReasoningConfig> ← effort / budget_tokens / display（ANT+OAI）
├── response_format: Option<ResponseFormat> ← text / json_object / json_schema
├── safety_settings: Option<Vec<SafetySetting>> ← Google safetySettings
├── stream: bool
├── ext: Option<Arc<dyn ProtocolExt>> ← 协议域强类型扩展（见下方）
└── vendor: VendorExtensions        ← 三段透传袋（ingress / egress / passthrough_safe）

AiResponse
├── id: Option<String>
├── model: Option<String>
├── content: Vec<ContentBlock>      ← 输出内容（含工具调用）
├── stop_reason: Option<StopReason>
├── usage: Option<TokenUsage>
├── error: Option<AiError>          ← 非 2xx 或 content_filter 时填充
└── vendor: VendorExtensions
```

### 协议域 Ext 类型

| Ext 类型 | 协议 | 关键字段 |
|---------|------|---------|
| `OpenAIChatExt` | OAI Chat | `audio`, `logit_bias`, `logprobs`, `top_logprobs`, `modalities`, `n`, `prediction`, `prompt_cache_retention`, `stream_options`, `verbosity`, `web_search_options` |
| `OpenAIResponsesExt` | OAI Responses | `background`, `context_management`, `conversation`, `include`, `previous_response_id`, `prompt`, `prompt_cache_retention`, `stream_options`, `top_logprobs`, `truncation`, `tool_choice_ext` |
| `AnthropicExt` | Anthropic | `top_k`, `container`, `inference_geo`, `output_config`, `service_tier`, `server_tools` |
| `GoogleExt` | Google GenAI | `top_k`, `candidate_count`, `response_logprobs`, `logprobs`, `response_mime_type`, `response_json_schema`, `tool_config`, `cached_content`, `response_modalities`, `thinking_config`, `image_config` |

### ContentBlock 变体（统一内容枚举）

```
ContentBlock
├── Text { text, cache_control? }
├── Image { source, cache_control? }
├── Audio { source }
├── File { source, detail? }
├── Document { source, title?, context?, citations? }
├── SearchResult { content, source, title, cache_control? }
├── ToolUse { id, name, input, cache_control? }
├── ToolResult { tool_use_id, content, is_error?, cache_control? }
├── Thinking { thinking, signature }
├── RedactedThinking { data }
├── ServerToolUse { id, name, input, server_type, cache_control? }
├── ServerToolResult { tool_use_id, content, server_type, cache_control? }
├── Citation { source, location, cited_text }
├── ContainerUpload { file_id, cache_control? }
├── ExecutableCode { code, language, id? }
├── CodeExecutionResult { return_code, stdout, stderr, id? }
└── Refusal { refusal }
```

### AiError / AiErrorKind

```
AiError {
    kind: AiErrorKind,
    message: String,
    status_code: Option<u16>,
    raw: Option<serde_json::Value>,   // 原始 vendor 错误体
}

AiErrorKind::is_retryable() → bool
  retryable:   RateLimitError, ServerError, ServiceUnavailable, Timeout,
               ModelNotAvailable, UnexpectedEof, StreamMidError (视 kind)
  not-retryable: AuthenticationError, AuthorizationError, NotFoundError,
                 InvalidRequest, ContentFiltered, ContextLengthExceeded
```

---

## 迁移路线

| PR | 内容 | 状态 |
|----|------|------|
| PR-0 | 设计文档（本目录） | ✅ done |
| PR-1 | IR 类型重塑：新增 `AiError/Kind`、`CacheControl/CacheTtl`、`SchemaObject`；扩展 `ContentBlock` 变体；`AiStreamDelta` 加错误变体 | pending |
| PR-2 | Codec Decoder 全切到 `AiRequest`；提取 4 协议 Ext；dispatcher 入口移除旧 IR 转换 | pending |
| PR-3 | Codec Encoder + Formatter 切换；实现 `cache_inject`、`error_normalize`、协议契约 | pending |
| PR-4 | Codec Parser + Stream Parser 切换；覆盖 OAI Responses 53 事件；流式中途错误检测 | pending |
| PR-5 | Dispatcher / Provider Adapter / Cache 全切到新 IR；重试逻辑改用 `AiErrorKind::is_retryable` | pending |
| PR-6 | 删除 `InternalRequest/InternalResponse` + `compat.rs`；nyro-tools 同步切换 | pending |

---

## 关键设计决策（摘要）

详细决策见 [FIELD_HOMING.md § 11](./FIELD_HOMING.md#11-争议字段决策记录)。

1. **Hybrid IR 架构**：IR 核心保持瘦身（只存跨协议语义等价字段），协议特有字段进 ProtocolExt，纯透传字段进 VendorBag。
2. **`n` / `candidateCount` 不合并 IR**：响应结构差异大，合并对 Parser 无收益。
3. **Thinking 三协议部分合并**：ANT `thinking` + OAI `reasoning` 合并为 `AiRequest.reasoning`；GGL `thinkingConfig` 保留在 `GoogleExt`。
4. **`service_tier` 不合并 IR**：各协议枚举值不对齐，无跨协议语义。
5. **server tools 归 ProtocolExt**：Anthropic server tools 和 OAI built-in tools 架构差异大，不进 `AiRequest.tools`。
6. **ContentBlock::Document 与 File 分开**：Anthropic `DocumentBlockParam` 有 `context`/`citations` 语义，与 `File` 不同。
