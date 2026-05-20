# IR 字段归属决策表（Field Homing）

> **版本**: v1.0 · **生成依据**: SDK 2026-05-14 快照 (openai@6.37.0 / @anthropic-ai/sdk@0.96.0 / @google/genai@2.2.0)  
> **用途**: PR-1 编码前的字段归属仲裁。每个字段只能有一个主归属。有争议的字段在本文件讨论并锁定，不在 PR review 中再争。

---

## 目录

1. [决策规则](#1-决策规则)
2. [归属标签速查](#2-归属标签速查)
3. [OpenAI Chat 字段归属](#3-openai-chat-字段归属)
4. [OpenAI Responses 字段归属](#4-openai-responses-字段归属)
5. [Anthropic Messages 字段归属](#5-anthropic-messages-字段归属)
6. [Google Gemini 字段归属](#6-google-gemini-字段归属)
7. [ContentBlock 跨协议映射](#7-contentblock-跨协议映射)
8. [响应字段归属](#8-响应字段归属)
9. [错误规范化映射](#9-错误规范化映射)
10. [缓存控制跨协议映射](#10-缓存控制跨协议映射)
11. [争议字段决策记录](#11-争议字段决策记录)

---

## 1. 决策规则

字段归属的优先级排序（从高到低）：

| 优先级 | 规则 | 归属到 |
|--------|------|--------|
| R1 | 被 Nyro 网关基础组件消费（路由、限流、配额、缓存 key、重试判断等） | **IR 核心** |
| R2 | ≥ 2 个协议族有语义等价字段，且行为一致 | **IR 核心** |
| R3 | 影响 Codec 行为（编解码时需要读取），但只在 1 个协议族存在 | **ProtocolExt** |
| R4 | 纯透传（下游不改语义、不读取），客户端写什么 egress 发什么 | **VendorBag.passthrough_safe** |
| R5 | 只在 SDK 层存在，不进入 HTTP wire | **DROP（不进 IR）** |
| R6 | 已废弃但需要兼容 | **VendorBag.passthrough_safe** + 文档标注 DEPR |

**Ext vs VendorBag 判断标准**：  
- 如果 Codec 编码时需要 `if let Some(ext) = req.ext.downcast::<OpenAIChatExt>() { ... }` 才能处理该字段 → 归 **ProtocolExt**  
- 如果 Encoder 只需 `serde_json::to_value(bag.passthrough_safe)` 合并到 body → 归 **VendorBag**

---

## 2. 归属标签速查

| 标签 | 含义 | Rust 类型 |
|------|------|-----------|
| `IR` | `AiRequest` 核心字段 | `AiRequest::field` |
| `OAIChat` | OpenAI Chat Ext | `OpenAIChatExt::field` |
| `OAIResp` | OpenAI Responses Ext | `OpenAIResponsesExt::field` |
| `ANT` | Anthropic Ext | `AnthropicExt::field` |
| `GGL` | Google Gemini Ext | `GoogleExt::field` |
| `BAG↓pass` | 透传 bag（egress 原样注入） | `VendorExtensions::passthrough_safe` |
| `BAG↓ing` | 入口 bag（Decoder 专用，不透传） | `VendorExtensions::ingress` |
| `DROP` | SDK 专有字段，不进 IR | — |
| `DEPR` | 已废弃，仅兼容 | 标注 deprecated |

---

## 3. OpenAI Chat 字段归属

**Wire**: `POST /v1/chat/completions` · SDK: openai@6.37.0

### 请求体字段

| 字段 | 归属 | IR 映射路径 | 说明 |
|------|------|------------|------|
| `messages` | IR | `AiRequest.messages` | 消息列表，含多模态 content parts |
| `model` | IR | `AiRequest.model` | |
| `temperature` | IR | `AiRequest.generation.temperature` | 0-2 范围 |
| `top_p` | IR | `AiRequest.generation.top_p` | |
| `max_completion_tokens` | IR | `AiRequest.generation.max_tokens` | 规范字段名 |
| `max_tokens` | IR | `AiRequest.generation.max_tokens` | `max_completion_tokens` 的别名；Decoder 合并 |
| `stop` | IR | `AiRequest.generation.stop` | `String \| String[]` → `Vec<String>` |
| `seed` | IR | `AiRequest.generation.seed` | |
| `presence_penalty` | IR | `AiRequest.generation.presence_penalty` | 与 Google `presencePenalty` 语义等价 |
| `frequency_penalty` | IR | `AiRequest.generation.frequency_penalty` | 与 Google `frequencyPenalty` 语义等价 |
| `tools` | IR | `AiRequest.tools` | `FunctionDefinition[]` → `Vec<ToolSpec>` |
| `tool_choice` | IR | `AiRequest.tool_config.choice` | `none/auto/required/function{}` → `ToolChoice` enum |
| `parallel_tool_calls` | IR | `AiRequest.tool_config.parallel` | 与 Responses API 语义等价 |
| `reasoning_effort` | IR | `AiRequest.reasoning.effort` | `none/low/medium/high/xhigh` → `ReasoningEffort` enum |
| `response_format` | IR | `AiRequest.response_format` | `text / json_object / json_schema` → `ResponseFormat` |
| `stream` | IR | `AiRequest.stream` | |
| `audio` | OAIChat | `OpenAIChatExt.audio` | 音频输出参数，仅此协议 |
| `logit_bias` | OAIChat | `OpenAIChatExt.logit_bias` | token 偏置，OAI 专有 |
| `logprobs` | OAIChat | `OpenAIChatExt.logprobs` | Encoder 专读 |
| `top_logprobs` | OAIChat | `OpenAIChatExt.top_logprobs` | 依赖 logprobs |
| `modalities` | OAIChat | `OpenAIChatExt.modalities` | `["text","audio"]` 多模态输出 |
| `n` | OAIChat | `OpenAIChatExt.n` | 多候选数，GGL `candidateCount` 不对齐（分类方式不同） |
| `prediction` | OAIChat | `OpenAIChatExt.prediction` | 推测解码 |
| `prompt_cache_retention` | OAIChat | `OpenAIChatExt.prompt_cache_retention` | `in_memory \| 24h`，仅 OAI |
| `stream_options` | OAIChat | `OpenAIChatExt.stream_options` | `include_usage` 等 |
| `verbosity` | OAIChat | `OpenAIChatExt.verbosity` | `low/medium/high` 输出详略 |
| `web_search_options` | OAIChat | `OpenAIChatExt.web_search_options` | Chat API 内置 web search |
| `metadata` | BAG↓pass | `VendorExtensions.passthrough_safe["metadata"]` | OAI KV store，Nyro 不读取 |
| `prompt_cache_key` | BAG↓pass | `VendorExtensions.passthrough_safe["prompt_cache_key"]` | OAI cache hit 优化 key |
| `safety_identifier` | BAG↓pass | `VendorExtensions.passthrough_safe["safety_identifier"]` | 用户哈希 ID |
| `service_tier` | BAG↓pass | `VendorExtensions.passthrough_safe["service_tier"]` | `auto/default/flex/scale/priority` |
| `store` | BAG↓pass | `VendorExtensions.passthrough_safe["store"]` | 数据保留 flag |
| `user` | BAG↓pass | `VendorExtensions.passthrough_safe["user"]` | 用户标识（被 safety_identifier 替代） |
| `function_call` | BAG↓pass | `VendorExtensions.passthrough_safe["function_call"]` | DEPR: 旧版函数调用 |
| `functions` | BAG↓pass | `VendorExtensions.passthrough_safe["functions"]` | DEPR: 旧版函数定义 |

### 消息 Content Part 映射

| OAI Content Part | IR ContentBlock variant |
|-----------------|------------------------|
| `ChatCompletionContentPartText` | `ContentBlock::Text` |
| `ChatCompletionContentPartImage` | `ContentBlock::Image` |
| `ChatCompletionContentPartInputAudio` | `ContentBlock::Audio` |
| `File` (in ContentPart) | `ContentBlock::File` |
| `tool_calls[].function` (assistant) | `ContentBlock::ToolUse` |
| tool message (role=tool) | `ContentBlock::ToolResult` |
| `ChatCompletionContentPartRefusal` | `ContentBlock::Refusal` |

---

## 4. OpenAI Responses 字段归属

**Wire**: `POST /v1/responses` · SDK: openai@6.37.0

### 请求体字段

| 字段 | 归属 | IR 映射路径 | 说明 |
|------|------|------------|------|
| `model` | IR | `AiRequest.model` | |
| `input` | IR | `AiRequest.messages` | `string \| ResponseInput` → 消息列表；Decoder 展开 |
| `instructions` | IR | `AiRequest.system` | 系统消息 |
| `temperature` | IR | `AiRequest.generation.temperature` | |
| `top_p` | IR | `AiRequest.generation.top_p` | |
| `max_output_tokens` | IR | `AiRequest.generation.max_tokens` | 同 `max_completion_tokens` |
| `tools` | IR | `AiRequest.tools` | function + built-in tools |
| `tool_choice` | IR | `AiRequest.tool_config.choice` | |
| `parallel_tool_calls` | IR | `AiRequest.tool_config.parallel` | |
| `reasoning` | IR | `AiRequest.reasoning` | `{effort, summary}` → `ReasoningConfig` |
| `text` | IR | `AiRequest.response_format` | `ResponseTextConfig.format` → `ResponseFormat` |
| `stream` | IR | `AiRequest.stream` | |
| `background` | OAIResp | `OpenAIResponsesExt.background` | 后台模式，Encoder 写入 |
| `context_management` | OAIResp | `OpenAIResponsesExt.context_management` | 上下文压缩策略 |
| `conversation` | OAIResp | `OpenAIResponsesExt.conversation` | 有状态会话 ID |
| `include` | OAIResp | `OpenAIResponsesExt.include` | 额外输出项，Encoder 写入 |
| `previous_response_id` | OAIResp | `OpenAIResponsesExt.previous_response_id` | 多轮对话链 |
| `prompt` | OAIResp | `OpenAIResponsesExt.prompt` | prompt 模板引用 |
| `prompt_cache_retention` | OAIResp | `OpenAIResponsesExt.prompt_cache_retention` | 与 Chat API 相同语义但不合并 IR（不跨协议通用） |
| `stream_options` | OAIResp | `OpenAIResponsesExt.stream_options` | |
| `top_logprobs` | OAIResp | `OpenAIResponsesExt.top_logprobs` | |
| `truncation` | OAIResp | `OpenAIResponsesExt.truncation` | `auto \| disabled` 上下文截断策略 |
| `metadata` | BAG↓pass | `VendorExtensions.passthrough_safe["metadata"]` | |
| `prompt_cache_key` | BAG↓pass | `VendorExtensions.passthrough_safe["prompt_cache_key"]` | |
| `safety_identifier` | BAG↓pass | `VendorExtensions.passthrough_safe["safety_identifier"]` | |
| `service_tier` | BAG↓pass | `VendorExtensions.passthrough_safe["service_tier"]` | |
| `store` | BAG↓pass | `VendorExtensions.passthrough_safe["store"]` | |
| `user` | BAG↓pass | `VendorExtensions.passthrough_safe["user"]` | |

### 流式事件 (53 个) 归属原则

Responses API 流式事件不归属到 `AiRequest`，由 `StreamParser` 消费并映射到 `AiStreamDelta` 变体：

| 事件组 | AiStreamDelta 映射 |
|--------|-------------------|
| `response.created` / `response.in_progress` / `response.queued` | `AiStreamDelta::ResponseLifecycle` |
| `response.output_item.added` | `AiStreamDelta::ItemStart { index, item_type }` |
| `response.text.delta` / `response.reasoning_text.delta` | `AiStreamDelta::TextDelta { delta, content_index }` |
| `response.function_call_arguments.delta` | `AiStreamDelta::ToolCallArgsDelta { delta, call_id }` |
| `response.function_call_arguments.done` | `AiStreamDelta::ToolCallArgsDone { call_id, name, arguments }` |
| `response.output_item.done` | `AiStreamDelta::ItemDone { index }` |
| `response.completed` | `AiStreamDelta::Done { usage }` |
| `response.failed` / `response.incomplete` | `AiStreamDelta::StreamError { error }` |
| `response.error` | `AiStreamDelta::StreamError { error }` |
| 其他 built-in tool 事件 (web_search/file_search/code_interpreter/mcp/image_gen) | `AiStreamDelta::ServerToolEvent { event_type, item_id, payload }` |

---

## 5. Anthropic Messages 字段归属

**Wire**: `POST /v1/messages` · SDK: @anthropic-ai/sdk@0.96.0

### 请求体字段

| 字段 | 归属 | IR 映射路径 | 说明 |
|------|------|------------|------|
| `model` | IR | `AiRequest.model` | |
| `messages` | IR | `AiRequest.messages` | `ContentBlockParam[]` union（16+ 类型） |
| `max_tokens` | IR | `AiRequest.generation.max_tokens` | |
| `system` | IR | `AiRequest.system` | `string \| TextBlockParam[]`（支持 cache_control） |
| `stream` | IR | `AiRequest.stream` | |
| `temperature` | IR | `AiRequest.generation.temperature` | |
| `top_p` | IR | `AiRequest.generation.top_p` | |
| `tools` | IR | `AiRequest.tools` | user `Tool` → `ToolSpec`；server tools → `AnthropicExt.server_tools` |
| `tool_choice` | IR | `AiRequest.tool_config.choice` | `ToolChoiceAuto/Any/Tool/None` → `ToolChoice` |
| `thinking` | IR | `AiRequest.reasoning` | `ThinkingConfigParam` → `ReasoningConfig {enabled, budget_tokens, display}` |
| `stop_sequences` | IR | `AiRequest.generation.stop` | |
| `top_k` | ANT | `AnthropicExt.top_k` | Anthropic 专有采样参数 |
| `container` | ANT | `AnthropicExt.container` | 代码执行容器配置 |
| `inference_geo` | ANT | `AnthropicExt.inference_geo` | 地理位置路由 |
| `output_config` | ANT | `AnthropicExt.output_config` | `{effort, format: json_schema}` 结构化输出 |
| `service_tier` | ANT | `AnthropicExt.service_tier` | Anthropic 有自己的 tier 定义，不与 OAI 合并 |
| `metadata` | BAG↓pass | `VendorExtensions.passthrough_safe["metadata"]` | `{user_id}` |
| (beta headers) | BAG↓ing | `VendorExtensions.ingress["anthropic-beta"]` | Decoder 读取 request header |

### ContentBlockParam 映射

Anthropic 的 `ContentBlockParam` 是一个 16 类型联合体，全部映射到 `ContentBlock` enum：

| Anthropic ContentBlockParam | IR ContentBlock variant | cache_control 携带 |
|-----------------------------|------------------------|-------------------|
| `TextBlockParam` | `ContentBlock::Text` | ✓ `cache_control` 字段 |
| `ImageBlockParam` | `ContentBlock::Image` | ✓ |
| `DocumentBlockParam` | `ContentBlock::Document` | ✓ |
| `SearchResultBlockParam` | `ContentBlock::SearchResult` | ✓ |
| `ThinkingBlockParam` | `ContentBlock::Thinking` | ✗ |
| `RedactedThinkingBlockParam` | `ContentBlock::RedactedThinking` | ✗ |
| `ToolUseBlockParam` | `ContentBlock::ToolUse` | ✓ |
| `ToolResultBlockParam` | `ContentBlock::ToolResult` | ✓ |
| `ServerToolUseBlockParam` | `ContentBlock::ServerToolUse` | ✓ |
| `WebSearchToolResultBlockParam` | `ContentBlock::ServerToolResult { server_type: WebSearch }` | ✓ |
| `WebFetchToolResultBlockParam` | `ContentBlock::ServerToolResult { server_type: WebFetch }` | ✓ |
| `CodeExecutionToolResultBlockParam` | `ContentBlock::ServerToolResult { server_type: CodeExecution }` | ✓ |
| `BashCodeExecutionToolResultBlockParam` | `ContentBlock::ServerToolResult { server_type: BashCodeExecution }` | ✓ |
| `TextEditorCodeExecutionToolResultBlockParam` | `ContentBlock::ServerToolResult { server_type: TextEditor }` | ✓ |
| `ToolSearchToolResultBlockParam` | `ContentBlock::ServerToolResult { server_type: ToolSearch }` | ✓ |
| `ContainerUploadBlockParam` | `ContentBlock::ContainerUpload` | ✓ |

### Server Tools 归属

Anthropic 的 server tool 规范（`ToolBash`, `WebSearchTool`, `CodeExecutionTool` 等）进入 `AnthropicExt.server_tools`，不映射到 `AiRequest.tools`（后者仅存放用户定义工具）：

| Anthropic Server Tool Type | 归属 |
|---------------------------|------|
| `Tool` (custom, type=custom) | IR: `AiRequest.tools` → `ToolSpec` |
| `ToolBash20250124` | ANT: `AnthropicExt.server_tools` |
| `CodeExecutionTool20250522/20250825/20260120` | ANT: `AnthropicExt.server_tools` |
| `MemoryTool20250818` | ANT: `AnthropicExt.server_tools` |
| `ToolTextEditor20250124/20250429/20250728` | ANT: `AnthropicExt.server_tools` |
| `WebSearchTool20250305/20260209` | ANT: `AnthropicExt.server_tools` |
| `WebFetchTool20250910/20260209/20260309` | ANT: `AnthropicExt.server_tools` |
| `ToolSearchToolBm25_20251119` | ANT: `AnthropicExt.server_tools` |
| `ToolSearchToolRegex20251119` | ANT: `AnthropicExt.server_tools` |

---

## 6. Google Gemini 字段归属

**Wire**: `POST /v1beta/{model}:generateContent` · SDK: @google/genai@2.2.0  
注：SDK `config: GenerateContentConfig` 是 wrapper；wire 层字段嵌套在 `generationConfig` 对象中。

### 请求体字段

| SDK 字段 | Wire 字段 | 归属 | IR 映射路径 | 说明 |
|---------|-----------|------|------------|------|
| `model` | (URL path) | IR | `AiRequest.model` | 写入 URL，不在 body |
| `contents` | `contents` | IR | `AiRequest.messages` | `Content[]` → messages；`Content.role` = user/model |
| `systemInstruction` | `systemInstruction` | IR | `AiRequest.system` | `Content` 格式 |
| `temperature` | `generationConfig.temperature` | IR | `AiRequest.generation.temperature` | |
| `topP` | `generationConfig.topP` | IR | `AiRequest.generation.top_p` | camelCase |
| `maxOutputTokens` | `generationConfig.maxOutputTokens` | IR | `AiRequest.generation.max_tokens` | |
| `stopSequences` | `generationConfig.stopSequences` | IR | `AiRequest.generation.stop` | |
| `presencePenalty` | `generationConfig.presencePenalty` | IR | `AiRequest.generation.presence_penalty` | |
| `frequencyPenalty` | `generationConfig.frequencyPenalty` | IR | `AiRequest.generation.frequency_penalty` | |
| `seed` | `generationConfig.seed` | IR | `AiRequest.generation.seed` | |
| `responseSchema` | `generationConfig.responseSchema` | IR | `AiRequest.response_format` | Schema 大写 type 需归一化 |
| `safetySettings` | `safetySettings` | IR | `AiRequest.safety_settings` | `Vec<SafetySetting>` |
| `tools` | `tools` | IR | `AiRequest.tools` | `functionDeclarations[]` → `ToolSpec` |
| `topK` | `generationConfig.topK` | GGL | `GoogleExt.top_k` | |
| `candidateCount` | `generationConfig.candidateCount` | GGL | `GoogleExt.candidate_count` | |
| `responseLogprobs` | `generationConfig.responseLogprobs` | GGL | `GoogleExt.response_logprobs` | |
| `logprobs` | `generationConfig.logprobs` | GGL | `GoogleExt.logprobs` | |
| `responseMimeType` | `generationConfig.responseMimeType` | GGL | `GoogleExt.response_mime_type` | |
| `responseJsonSchema` | `generationConfig.responseJsonSchema` | GGL | `GoogleExt.response_json_schema` | responseSchema 的 JSON Schema 变体 |
| `toolConfig` | `toolConfig` | GGL | `GoogleExt.tool_config` | function calling mode |
| `cachedContent` | `cachedContent` | GGL | `GoogleExt.cached_content` | GGL 服务端缓存引用 |
| `responseModalities` | `generationConfig.responseModalities` | GGL | `GoogleExt.response_modalities` | `["TEXT","IMAGE","AUDIO"]` |
| `thinkingConfig` | `generationConfig.thinkingConfig` | GGL | `GoogleExt.thinking_config` | 思考预算（与 ANT thinking 不合并，枚举不对齐） |
| `imageConfig` | `generationConfig.imageConfig` | GGL | `GoogleExt.image_config` | 图像生成配置 |
| `routingConfig` | `generationConfig.routingConfig` | BAG↓pass | `VendorExtensions.passthrough_safe["routing_config"]` | auto/manual 路由 |
| `modelSelectionConfig` | `generationConfig.modelSelectionConfig` | BAG↓pass | `VendorExtensions.passthrough_safe["model_selection_config"]` | |
| `labels` | `labels` | BAG↓pass | `VendorExtensions.passthrough_safe["labels"]` | billing labels |
| `mediaResolution` | `generationConfig.mediaResolution` | BAG↓pass | `VendorExtensions.passthrough_safe["media_resolution"]` | |
| `speechConfig` | `generationConfig.speechConfig` | BAG↓pass | `VendorExtensions.passthrough_safe["speech_config"]` | |
| `audioTimestamp` | `generationConfig.audioTimestamp` | BAG↓pass | `VendorExtensions.passthrough_safe["audio_timestamp"]` | |
| `enableEnhancedCivicAnswers` | `generationConfig.enableEnhancedCivicAnswers` | BAG↓pass | `VendorExtensions.passthrough_safe["enable_enhanced_civic_answers"]` | |
| `modelArmorConfig` | `modelArmorConfig` | BAG↓pass | `VendorExtensions.passthrough_safe["model_armor_config"]` | |
| `serviceTier` | `generationConfig.serviceTier` | BAG↓pass | `VendorExtensions.passthrough_safe["service_tier"]` | |
| `httpOptions` | — | DROP | — | SDK 层配置，不进 IR |
| `abortSignal` | — | DROP | — | SDK 层，不进 IR |
| `automaticFunctionCalling` | — | DROP | — | SDK 自动函数调用循环，不进 IR |

### Google Part → ContentBlock 映射

| Google `Part` 字段 | IR ContentBlock variant |
|-------------------|------------------------|
| `Part.text` (role=user/model) | `ContentBlock::Text` |
| `Part.inlineData` (image/audio MIME) | `ContentBlock::Image` / `ContentBlock::Audio` |
| `Part.fileData` | `ContentBlock::File` |
| `Part.functionCall` | `ContentBlock::ToolUse` |
| `Part.functionResponse` | `ContentBlock::ToolResult` |
| `Part.thought=true` + `Part.text` | `ContentBlock::Thinking` |
| `Part.thoughtSignature` | `ContentBlock::Thinking.signature` |
| `Part.executableCode` | `ContentBlock::ExecutableCode` |
| `Part.codeExecutionResult` | `ContentBlock::CodeExecutionResult` |
| `Part.toolCall` (server-side) | `ContentBlock::ServerToolUse` |
| `Part.toolResponse` (server-side) | `ContentBlock::ServerToolResult` |

---

## 7. ContentBlock 跨协议映射

`ContentBlock` 是 IR 的核心枚举，Decoder 负责将各协议的 content 结构归一化为该枚举。

| ContentBlock variant | OAI Chat | OAI Responses | Anthropic | Google |
|---------------------|----------|---------------|-----------|--------|
| `Text { text, cache_control? }` | `ContentPartText` | `ResponseInputText` / `ResponseOutputText` | `TextBlockParam` | `Part.text` |
| `Image { source, cache_control? }` | `ContentPartImage` | `ResponseInputImage` | `ImageBlockParam` | `Part.inlineData`(img) |
| `Audio { source }` | `ContentPartInputAudio` | `ResponseInputAudio` | — | `Part.inlineData`(audio) |
| `File { source, detail? }` | `File` in content | `ResponseInputFile` | `DocumentBlockParam` | `Part.fileData` |
| `Document { source, title?, context?, citations? }` | — | — | `DocumentBlockParam` (distinct from File) | — |
| `SearchResult { content, source, title }` | — | — | `SearchResultBlockParam` | — |
| `ToolUse { id, name, input, cache_control? }` | `tool_calls[n].function` | `ResponseFunctionToolCall` | `ToolUseBlockParam` | `Part.functionCall` |
| `ToolResult { tool_use_id, content, is_error?, cache_control? }` | tool role msg | `ResponseFunctionToolCallOutput` | `ToolResultBlockParam` | `Part.functionResponse` |
| `Thinking { thinking, signature }` | — | `ResponseReasoningItem` | `ThinkingBlockParam` | `Part{thought=true}` + text |
| `RedactedThinking { data }` | — | `reasoning.encrypted_content` | `RedactedThinkingBlockParam` | — |
| `ServerToolUse { id, name, input, server_type }` | (built-in calls) | built-in tool call items | `ServerToolUseBlockParam` | `Part.toolCall` |
| `ServerToolResult { tool_use_id, content, server_type }` | (built-in outputs) | built-in tool output items | `WebSearch/WebFetch/CodeExec/...ResultBlockParam` | `Part.toolResponse` |
| `Citation { source, location, cited_text }` | — | `ResponseOutputTextAnnotation` | TextBlock.citations | — |
| `ContainerUpload { file_id, cache_control? }` | — | `ResponseInputFile`(container) | `ContainerUploadBlockParam` | — |
| `ExecutableCode { code, language, id? }` | — | `ResponseCodeInterpreterCall` | (via server tool result) | `Part.executableCode` |
| `CodeExecutionResult { return_code, stdout, stderr, id? }` | — | code interpreter outputs | `CodeExecutionResultBlockParam` | `Part.codeExecutionResult` |
| `Refusal { refusal }` | `ContentPartRefusal` | `ResponseOutputRefusal` | stop_reason=refusal | — |

**编解码方向说明**：
- Decoder（ingress）：协议原生格式 → `ContentBlock` enum  
- Encoder（egress）：`ContentBlock` enum → 目标协议格式  
- Parser（response）：模型响应 → `ContentBlock` / `AiStreamDelta`

---

## 8. 响应字段归属

### AiResponse 核心字段

| 语义 | OAI Chat | OAI Responses | Anthropic | Google | IR 字段 |
|------|----------|---------------|-----------|--------|---------|
| 响应 ID | `id` | `id` | `id` | `responseId` | `AiResponse.id` |
| 使用模型 | `model` | `model` | `model` | `modelVersion` | `AiResponse.model` |
| 停止原因 | `choices[].finish_reason` | `status` / item.status | `stop_reason` | `candidates[].finishReason` | `AiResponse.stop_reason` |
| 输出内容 | `choices[].message.content` | `output[]` | `content[]` | `candidates[].content` | `AiResponse.content: Vec<ContentBlock>` |
| 工具调用 | `choices[].message.tool_calls` | `output[].function_call` | `content[tool_use]` | `Part.functionCall` | 嵌入 `AiResponse.content` |
| Prompt token | `usage.prompt_tokens` | `usage.input_tokens` | `usage.input_tokens` | `usageMetadata.promptTokenCount` | `AiResponse.usage.input_tokens` |
| Completion token | `usage.completion_tokens` | `usage.output_tokens` | `usage.output_tokens` | `usageMetadata.candidatesTokenCount` | `AiResponse.usage.output_tokens` |
| 缓存命中 tokens | `usage.prompt_tokens_details.cached_tokens` | `usage.input_tokens_details.cached_tokens` | `usage.cache_read_input_tokens` | `usageMetadata.cachedContentTokenCount` | `AiResponse.usage.cache_read_tokens` |
| 推理 tokens | `usage.completion_tokens_details.reasoning_tokens` | `usage.output_tokens_details.reasoning_tokens` | (thinking tokens in input) | `usageMetadata.thoughtsTokenCount` | `AiResponse.usage.reasoning_tokens` |

### 响应中的 VendorBag 字段（透传回客户端）

| 字段 | 协议 | VendorBag 路径 |
|------|------|---------------|
| `system_fingerprint` | OAI Chat | `VendorExtensions.passthrough_safe["system_fingerprint"]` |
| `service_tier` | OAI Chat/Responses | `VendorExtensions.passthrough_safe["service_tier"]` |
| `created` / `created_at` | OAI | `VendorExtensions.passthrough_safe["created"]` |
| `conversation` (response) | OAI Responses | `VendorExtensions.passthrough_safe["conversation"]` |
| `promptFeedback` | Google | `VendorExtensions.passthrough_safe["prompt_feedback"]` |
| `modelStatus` | Google | `VendorExtensions.passthrough_safe["model_status"]` |

---

## 9. 错误规范化映射

`AiError` / `AiErrorKind` 是 IR 的错误公民，所有协议的错误均归一化到此类型。

### AiErrorKind 枚举与协议映射

| AiErrorKind | OAI HTTP 状态/code | ANT HTTP 状态/type | GGL HTTP 状态/reason | is_retryable |
|-------------|-------------------|-------------------|---------------------|-------------|
| `AuthenticationError` | 401 | 401/authentication_error | 401 | false |
| `AuthorizationError` | 403 | 403/permission_error | 403 | false |
| `NotFoundError` | 404 | 404/not_found_error | 404 | false |
| `RateLimitError` | 429 | 429/rate_limit_error | 429 | **true** |
| `QuotaExceeded` | 429 (type=quota) | 529/overloaded_error | 429 (quotaExceeded) | false |
| `InvalidRequest` | 400 | 400/invalid_request_error | 400 | false |
| `ServerError` | 500 | 500/api_error | 500 | **true** |
| `ServiceUnavailable` | 503 | 529/overloaded_error | 503 | **true** |
| `Timeout` | 408/504 | 408 | 408 | **true** |
| `ContentFiltered` | 400 (content_filter) | — | 200 with `promptFeedback.blockReason` | false |
| `ContextLengthExceeded` | 400 (context_length) | 400/context_length_exceeded | 400 | false |
| `ModelNotAvailable` | 404/503 | 404/not_found_error | 404 | **true** (临时) |
| `StreamMidError` | data: {error:{...}} in SSE | event: error\ndata: {...} | `promptFeedback` in chunk or EOF | 按 kind 判断 |
| `UnexpectedEof` | stream 被截断 | stream 被截断 | stream 被截断 | **true** |
| `Unknown` | 其他 | 其他 | 其他 | false |

### 流式中途错误检测

| 协议 | 错误信号 | 检测位置 |
|------|---------|---------|
| OAI Chat | `data: {"error":{"message":"...","type":"...","code":"..."}}` | StreamParser: 每 SSE line |
| OAI Responses | `event: error` + `data: {"code":"...","message":"..."}` 或 `response.failed` event | StreamParser: event type 检测 |
| Anthropic | `event: error` + `data: {"type":"error","error":{...}}` | StreamParser: event type 检测 |
| Google | stream body 中 `promptFeedback.blockReason` 或 EOF without `[DONE]` | StreamParser: 第一 chunk + EOF 检测 |

---

## 10. 缓存控制跨协议映射

### 请求侧 CacheControl 注入

| 协议 | 方式 | 最小 token 阈值 | cache_control 挂载点 |
|------|------|----------------|---------------------|
| Anthropic | 每个 ContentBlock / ToolSpec 的 `cache_control` 字段 | 1024 tokens (ephemeral 5m) / 2048 tokens (ephemeral 1h) | `ContentBlock.cache_control` |
| OAI Chat | 自动（prefix-based，client 无法显式标注） | 1024 tokens | 无字段；通过 `prompt_cache_key` + `prompt_cache_retention` 控制 |
| OAI Responses | 同 OAI Chat | 1024 tokens | 同上 |
| Google | `cachedContent` 引用名（服务端预缓存） | — | `GoogleExt.cached_content` |

### IR 中的 CacheControl 表示

```
CacheControl {
    ttl: CacheTtl,          // Ephemeral5m | Ephemeral1h | Auto
    breakpoint_priority: u8, // 多断点时的优先级排序，0=最低
}
```

**注入策略**（由 Encoder 实现）：
- Anthropic 出口：从 `ContentBlock.cache_control` 读取并写入 wire 字段
- OAI 出口：忽略 `ContentBlock.cache_control`，仅透传 `prompt_cache_key` / `prompt_cache_retention`（如 VendorBag 中有）
- Google 出口：透传 `GoogleExt.cached_content`

---

## 11. 争议字段决策记录

以下字段有争议，决策已锁定，不在 PR review 重新讨论。

| 字段 | 争议 | 决策 | 理由 |
|------|------|------|------|
| `n` (OAI) vs `candidateCount` (GGL) | 语义相同，是否合并到 IR？ | **不合并，各入 ProtocolExt** | 两者响应结构差异大（choices vs candidates），合并 IR 对 Parser 意义不大，会污染核心 |
| `thinkingConfig` (GGL) vs `thinking` (ANT) vs `reasoning` (OAI) | 三协议思考配置，是否合并？ | **ANT + OAI 合并到 `AiRequest.reasoning`；GGL 单独 `GoogleExt.thinking_config`** | ANT budget_tokens + OAI effort 可互操；GGL thinking 是实验性且结构不同 |
| `service_tier` (OAI/ANT/GGL 都有) | 是否合并到 IR？ | **不合并，各入 BAG↓pass（OAI/GGL）或 AnthropicExt（ANT）** | 各协议的 tier 枚举值完全不同，合并为 IR 字段没有语义意义 |
| `tool_choice` (OAI 有 `allowed_tools`/MCP 变体) | 是否把所有 OAI 变体存 IR？ | **IR 存规范化的 `ToolChoice` 枚举（none/auto/required/forced{name}）；OAI 特有变体（allowed_tools/MCP）存 `OpenAIResponsesExt.tool_choice_ext`** | 避免 IR 被 OAI 特有扩展污染 |
| `prompt_cache_retention` 在 OAIChat 和 OAIResp 各出现一次 | 合并到 IR 还是各 Ext？ | **各入对应 ProtocolExt** | 虽语义相同，但是否合并不影响跨协议互操作，保持 Ext 边界清晰 |
| `disable_parallel_tool_use` (ANT ToolChoice) | 是否归 IR？ | **归 IR `AiRequest.tool_config.disable_parallel`** | 与 OAI `parallel_tool_calls` 语义等价 |
| `strict` (OAI FunctionDefinition, ANT Tool) | 归 IR ToolSpec 还是 Ext？ | **归 IR `ToolSpec.strict: Option<bool>`** | ≥2 协议有，且 Encoder 需要读取 |
| Google `responseSchema` vs `responseJsonSchema` | 两者并存时如何处理？ | **`responseSchema` → `AiRequest.response_format`；`responseJsonSchema` → `GoogleExt.response_json_schema`；Encoder 优先 `response_json_schema` 如果存在** | responseJsonSchema 是 JSON Schema 标准格式，更推荐 |
| ANT `top_k` | 归 IR 还是 ANT Ext？ | **归 `AnthropicExt.top_k`** | 只有 Anthropic 支持，Google 虽有但在 GoogleExt |
| Google `topK` | 归 IR 还是 GGL Ext？ | **归 `GoogleExt.top_k`** | 与 ANT top_k 语义相同但不跨协议通用 |
