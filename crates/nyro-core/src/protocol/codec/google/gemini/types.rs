use serde::{Deserialize, Serialize};
use serde_json::Value;

// ── Top-level request ─────────────────────────────────────────────────────────

#[derive(Debug, Deserialize, Serialize)]
pub struct GoogleRequest {
    pub contents: Vec<GoogleContent>,
    #[serde(rename = "systemInstruction", skip_serializing_if = "Option::is_none")]
    pub system_instruction: Option<GoogleContent>,
    #[serde(rename = "generationConfig", skip_serializing_if = "Option::is_none")]
    pub generation_config: Option<GoogleGenerationConfig>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub tools: Option<Vec<GoogleToolEntry>>,
    #[serde(rename = "toolConfig", skip_serializing_if = "Option::is_none")]
    pub tool_config: Option<Value>,
    /// Safety settings per harm category.
    #[serde(rename = "safetySettings", skip_serializing_if = "Option::is_none")]
    pub safety_settings: Option<Vec<SafetySetting>>,
    /// Name of a previously created cached content object.
    #[serde(rename = "cachedContent", skip_serializing_if = "Option::is_none")]
    pub cached_content: Option<String>,
}

// ── Content / parts ───────────────────────────────────────────────────────────

#[derive(Debug, Deserialize, Serialize)]
pub struct GoogleContent {
    pub role: Option<String>,
    pub parts: Vec<GooglePart>,
}

/// All known Gemini Part shapes.  `Other` is a catch-all so the decoder never
/// rejects unknown part types introduced in future API versions.
#[derive(Debug, Deserialize, Serialize)]
#[serde(untagged)]
pub enum GooglePart {
    Text {
        text: String,
    },
    InlineData {
        #[serde(rename = "inlineData")]
        inline_data: GoogleInlineData,
    },
    FileData {
        #[serde(rename = "fileData")]
        file_data: GoogleFileData,
    },
    FunctionCall {
        #[serde(rename = "functionCall")]
        function_call: GoogleFunctionCall,
    },
    FunctionResponse {
        #[serde(rename = "functionResponse")]
        function_response: GoogleFunctionResponse,
    },
    ExecutableCode {
        #[serde(rename = "executableCode")]
        executable_code: GoogleExecutableCode,
    },
    CodeExecutionResult {
        #[serde(rename = "codeExecutionResult")]
        code_execution_result: GoogleCodeExecutionResult,
    },
    /// Passthrough for any unrecognised part shape (e.g. `thought`, video).
    Other(Value),
}

// ── Part subtypes ─────────────────────────────────────────────────────────────

#[derive(Debug, Deserialize, Serialize)]
pub struct GoogleInlineData {
    #[serde(rename = "mimeType")]
    pub mime_type: String,
    pub data: String,
}

#[derive(Debug, Deserialize, Serialize)]
pub struct GoogleFileData {
    #[serde(rename = "mimeType", skip_serializing_if = "Option::is_none")]
    pub mime_type: Option<String>,
    #[serde(rename = "fileUri")]
    pub file_uri: String,
}

#[derive(Debug, Clone, Deserialize, Serialize)]
pub struct GoogleFunctionCall {
    pub name: String,
    pub args: Value,
}

#[derive(Debug, Deserialize, Serialize)]
pub struct GoogleFunctionResponse {
    pub name: String,
    pub response: Value,
}

#[derive(Debug, Deserialize, Serialize)]
pub struct GoogleExecutableCode {
    pub language: Option<String>,
    pub code: String,
}

#[derive(Debug, Deserialize, Serialize)]
pub struct GoogleCodeExecutionResult {
    pub outcome: Option<String>,
    pub output: Option<String>,
}

// ── generationConfig ──────────────────────────────────────────────────────────

#[derive(Debug, Default, Deserialize, Serialize)]
pub struct GoogleGenerationConfig {
    // Core fields shared with InternalRequest ──────────────────────────────────
    #[serde(skip_serializing_if = "Option::is_none")]
    pub temperature: Option<f64>,
    #[serde(rename = "maxOutputTokens", skip_serializing_if = "Option::is_none")]
    pub max_output_tokens: Option<u32>,
    #[serde(rename = "topP", skip_serializing_if = "Option::is_none")]
    pub top_p: Option<f64>,

    // PR-11 additions ─────────────────────────────────────────────────────────
    #[serde(rename = "topK", skip_serializing_if = "Option::is_none")]
    pub top_k: Option<f64>,
    #[serde(rename = "candidateCount", skip_serializing_if = "Option::is_none")]
    pub candidate_count: Option<u32>,
    #[serde(rename = "stopSequences", skip_serializing_if = "Option::is_none")]
    pub stop_sequences: Option<Vec<String>>,
    #[serde(rename = "responseMimeType", skip_serializing_if = "Option::is_none")]
    pub response_mime_type: Option<String>,
    #[serde(rename = "responseSchema", skip_serializing_if = "Option::is_none")]
    pub response_schema: Option<Value>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub seed: Option<u32>,
    #[serde(rename = "presencePenalty", skip_serializing_if = "Option::is_none")]
    pub presence_penalty: Option<f64>,
    #[serde(rename = "frequencyPenalty", skip_serializing_if = "Option::is_none")]
    pub frequency_penalty: Option<f64>,
    #[serde(rename = "responseLogprobs", skip_serializing_if = "Option::is_none")]
    pub response_logprobs: Option<bool>,
    #[serde(rename = "logprobs", skip_serializing_if = "Option::is_none")]
    pub logprobs: Option<i32>,
    /// Gemini 2.5 thinking budget: `{ "thinkingBudget": N }`.
    #[serde(rename = "thinkingConfig", skip_serializing_if = "Option::is_none")]
    pub thinking_config: Option<Value>,
    #[serde(rename = "audioTimestamp", skip_serializing_if = "Option::is_none")]
    pub audio_timestamp: Option<bool>,
    #[serde(rename = "mediaResolution", skip_serializing_if = "Option::is_none")]
    pub media_resolution: Option<String>,
    #[serde(rename = "routingConfig", skip_serializing_if = "Option::is_none")]
    pub routing_config: Option<Value>,
}

// ── Tools ─────────────────────────────────────────────────────────────────────

/// A single entry in the `tools` array.  May contain function declarations
/// and/or built-in tool flags such as `googleSearch` and `codeExecution`.
#[derive(Debug, Default, Deserialize, Serialize)]
pub struct GoogleToolEntry {
    #[serde(
        rename = "functionDeclarations",
        skip_serializing_if = "Option::is_none"
    )]
    pub function_declarations: Option<Vec<GoogleFunctionDecl>>,
    #[serde(rename = "googleSearch", skip_serializing_if = "Option::is_none")]
    pub google_search: Option<Value>,
    #[serde(rename = "codeExecution", skip_serializing_if = "Option::is_none")]
    pub code_execution: Option<Value>,
    #[serde(
        rename = "googleSearchRetrieval",
        skip_serializing_if = "Option::is_none"
    )]
    pub google_search_retrieval: Option<Value>,
}

#[derive(Debug, Deserialize, Serialize)]
pub struct GoogleFunctionDecl {
    pub name: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub description: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub parameters: Option<Value>,
}

// ── Safety ────────────────────────────────────────────────────────────────────

#[derive(Debug, Clone, Deserialize, Serialize)]
pub struct SafetySetting {
    pub category: String,
    pub threshold: String,
}
