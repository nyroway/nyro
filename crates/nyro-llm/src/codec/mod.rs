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
