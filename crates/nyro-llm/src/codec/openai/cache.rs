//! OpenAI reports cache reads/writes within its inclusive input count.
use super::{CodecError, convert, invalid};
use crate::ir::{CacheCreationUsage, Usage};
use nyro_protocol::openai::chat;

pub(super) fn decode(mut wire: chat::Usage) -> Result<Usage, CodecError> {
    let written = wire
        .prompt_tokens_details
        .as_mut()
        .and_then(|d| d.cache_write_tokens.take());
    let mut usage: Usage = convert(wire)?;
    usage.cache_creation = written.map(|input_tokens| {
        Box::new(CacheCreationUsage {
            input_tokens,
            ephemeral_5m_input_tokens: None,
            ephemeral_1h_input_tokens: None,
        })
    });
    validate(&usage)?;
    usage.cache_creation = usage.cache_creation.filter(|c| c.input_tokens != 0);
    Ok(usage)
}

pub(crate) fn validate(usage: &Usage) -> Result<(), CodecError> {
    // Keep the existing observation/quota policy for legacy usage without
    // writes; this codec validates the newly supported write metadata.
    let Some(creation) = &usage.cache_creation else {
        return Ok(());
    };
    let read = usage
        .prompt_tokens_details
        .as_ref()
        .and_then(|d| d.cached_tokens)
        .unwrap_or(0);
    let written = creation.input_tokens;
    if usage.prompt_tokens.checked_add(usage.completion_tokens) != Some(usage.total_tokens)
        || read
            .checked_add(written)
            .is_none_or(|n| n > usage.prompt_tokens)
        || creation.ephemeral_5m_input_tokens.is_some()
        || creation.ephemeral_1h_input_tokens.is_some()
    {
        return Err(invalid(
            "invalid or unrepresentable OpenAI cache/token usage",
        ));
    }
    Ok(())
}

pub(super) fn encode(usage: &Usage) -> Result<chat::Usage, CodecError> {
    validate(usage)?;
    let mut plain = usage.clone();
    let creation = plain.cache_creation.take();
    let mut wire: chat::Usage = convert(plain)?;
    if let Some(creation) = creation {
        wire.prompt_tokens_details
            .get_or_insert(chat::PromptTokensDetails {
                cached_tokens: None,
                audio_tokens: None,
                cache_write_tokens: None,
            })
            .cache_write_tokens = Some(creation.input_tokens);
    }
    Ok(wire)
}
