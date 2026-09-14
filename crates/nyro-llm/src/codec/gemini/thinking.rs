use super::*;

pub(super) fn annotated(p: &w::Part) -> bool {
    p.thought.is_some() || p.thought_signature.is_some()
}
pub(super) fn text(p: w::Part) -> ContentPart {
    if annotated(&p) {
        ContentPart::GeminiText(GeminiText {
            text: p.text,
            thought: p.thought,
            thought_signature: p.thought_signature,
        })
    } else {
        ContentPart::Text {
            text: p.text.unwrap_or_default(),
            anthropic_cache_control: None,
            prompt_cache_breakpoint: None,
        }
    }
}
pub(super) fn wire_text(p: &GeminiText) -> Result<w::Part, CodecError> {
    let p = w::Part {
        text: p.text.clone(),
        thought: p.thought,
        thought_signature: p.thought_signature.clone(),
        ..Default::default()
    };
    check_part(&p)?;
    Ok(p)
}
pub(super) fn call_metadata(p: &w::Part) -> Option<GeminiCall> {
    annotated(p).then(|| GeminiCall {
        thought: p.thought,
        thought_signature: p.thought_signature.clone(),
        omit_id: p.function_call.as_ref().is_some_and(|c| c.id.is_none()),
    })
}
pub(super) fn wire_call(
    id: &str,
    function: &FunctionCall,
    meta: Option<&GeminiCall>,
) -> Result<w::Part, CodecError> {
    let args: Value = serde_json::from_str(&function.arguments)?;
    if id.is_empty() || function.name.is_empty() || !args.is_object() {
        return Err(bad("invalid function call"));
    }
    let part = w::Part {
        thought: meta.and_then(|m| m.thought),
        thought_signature: meta.and_then(|m| m.thought_signature.clone()),
        function_call: Some(w::FunctionCall {
            id: (!meta.is_some_and(|m| m.omit_id)).then(|| id.to_owned()),
            name: function.name.clone(),
            args,
        }),
        ..Default::default()
    };
    check_part(&part)?;
    Ok(part)
}
pub(super) fn portable(r: &ChatRequest) -> Result<ChatRequest, CodecError> {
    validate_config(r.gemini_thinking.as_ref())?;
    let mut portable = r.clone();
    portable.gemini_thinking = None;
    for m in &mut portable.messages {
        if let Some(Content::Parts(parts)) = &mut m.content {
            for part in parts {
                if let ContentPart::GeminiText(t) = part {
                    if m.role != Role::Assistant {
                        return Err(bad("Gemini thinking parts require assistant role"));
                    }
                    wire_text(t)?;
                    *part = ContentPart::Text {
                        text: t.text.clone().unwrap_or_default(),
                        anthropic_cache_control: None,
                        prompt_cache_breakpoint: None,
                    };
                }
            }
        }
        for ToolCall::Function { gemini, .. } in m.tool_calls.iter_mut().flatten() {
            *gemini = None;
        }
    }
    Ok(portable)
}
pub(super) fn validate_config(config: Option<&w::ThinkingConfig>) -> Result<(), CodecError> {
    if let Some(c) = config
        && (c.thinking_budget.is_some_and(|n| n < -1)
            || (c.thinking_budget.is_some() && c.thinking_level.is_some()))
    {
        return Err(bad("invalid or conflicting Gemini thinking controls"));
    }
    Ok(())
}
