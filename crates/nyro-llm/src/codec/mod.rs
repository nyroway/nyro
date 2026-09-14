mod image;
pub mod openai;

/// A rejected or unsupported payload; HTTP ingress sanitizes details before responding.
#[derive(Debug, thiserror::Error)]
#[error("unsupported or invalid protocol payload: {0}")]
pub struct CodecError(pub String);

impl From<serde_json::Error> for CodecError {
    fn from(error: serde_json::Error) -> Self {
        Self(error.to_string())
    }
}

pub mod anthropic;
pub mod gemini;

/// The foundation migrates storage, not the accepted ordered-output subset.
fn validate_message_items(items: &[crate::ir::MessageItem]) -> Result<(), CodecError> {
    for (index, item) in items.iter().enumerate() {
        if matches!(item, crate::ir::MessageItem::Content(_)) && index != 0 {
            return Err(CodecError(
                "multiple or interleaved content groups are not supported".into(),
            ));
        }
    }
    Ok(())
}

/// Validate field slots without inventing an order between OpenAI Chat fields.
fn validate_position(event: &crate::ir::PositionedDelta) -> Result<(), CodecError> {
    use crate::ir::{PartDelta as D, StreamItem as I};
    let valid = match (&event.position.item, &event.delta) {
        (I::OpenAiMessage, D::Text(_)) => event.position.part == 0,
        (I::OpenAiMessage, D::Refusal(_)) => event.position.part == 1,
        (I::OpenAiTool(index), D::ToolCall(call)) => {
            event.position.part == 0 && *index == call.index
        }
        (I::Ordered(_), D::ToolCall(_) | D::GeminiText(_) | D::AnthropicThinking(_)) => {
            event.position.part == 0
        }
        (I::Ordered(_), _) => true,
        _ => false,
    };
    if valid {
        Ok(())
    } else {
        Err(CodecError(
            "stream position does not match its payload".into(),
        ))
    }
}

/// Wire API selection is independent of workload and provider credentials.
#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub(crate) enum ChatFormat {
    OpenAiChat,
    OpenAiResponses,
    Anthropic,
    Gemini,
}
