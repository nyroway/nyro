//! Anthropic input categories are disjoint; TTL counters subdivide cache writes.
use crate::{codec::CodecError, ir::*};
use nyro_protocol::anthropic as wire;
fn bad() -> CodecError {
    CodecError("invalid Anthropic cache/token usage".into())
}
pub(super) fn decode(u: &wire::Usage) -> Result<Usage, CodecError> {
    let created = u.cache_creation_input_tokens.unwrap_or(0);
    if let Some(parts) = &u.cache_creation
        && parts
            .ephemeral_5m_input_tokens
            .checked_add(parts.ephemeral_1h_input_tokens)
            != Some(created)
    {
        return Err(bad());
    }
    let prompt = u
        .input_tokens
        .checked_add(created)
        .and_then(|n| n.checked_add(u.cache_read_input_tokens.unwrap_or(0)))
        .ok_or_else(bad)?;
    Ok(Usage {
        prompt_tokens: prompt,
        completion_tokens: u.output_tokens,
        total_tokens: prompt.checked_add(u.output_tokens).ok_or_else(bad)?,
        cache_creation: (created != 0).then(|| {
            Box::new(CacheCreationUsage {
                input_tokens: created,
                ephemeral_5m_input_tokens: u
                    .cache_creation
                    .as_ref()
                    .map(|p| p.ephemeral_5m_input_tokens),
                ephemeral_1h_input_tokens: u
                    .cache_creation
                    .as_ref()
                    .map(|p| p.ephemeral_1h_input_tokens),
            })
        }),
        prompt_tokens_details: u.cache_read_input_tokens.map(|n| PromptTokensDetails {
            cached_tokens: Some(n),
            audio_tokens: None,
        }),
        completion_tokens_details: None,
    })
}
pub(super) fn encode(u: &Usage) -> Result<wire::Usage, CodecError> {
    if u.prompt_tokens.checked_add(u.completion_tokens) != Some(u.total_tokens)
        || u.completion_tokens_details.is_some()
        || u.prompt_tokens_details
            .as_ref()
            .is_some_and(|d| d.audio_tokens.is_some())
    {
        return Err(bad());
    }
    let read = u
        .prompt_tokens_details
        .as_ref()
        .and_then(|d| d.cached_tokens);
    let created = u.cache_creation.as_ref().map(|c| c.input_tokens);
    let input = u
        .prompt_tokens
        .checked_sub(read.unwrap_or(0))
        .and_then(|n| n.checked_sub(created.unwrap_or(0)))
        .ok_or_else(bad)?;
    let breakdown = match u
        .cache_creation
        .as_ref()
        .map(|c| (c.ephemeral_5m_input_tokens, c.ephemeral_1h_input_tokens))
    {
        None | Some((None, None)) => None,
        Some((Some(five), Some(hour))) if five.checked_add(hour) == created => {
            Some(wire::CacheCreation {
                ephemeral_5m_input_tokens: five,
                ephemeral_1h_input_tokens: hour,
            })
        }
        _ => return Err(bad()),
    };
    Ok(wire::Usage {
        input_tokens: input,
        output_tokens: u.completion_tokens,
        cache_read_input_tokens: read,
        cache_creation_input_tokens: created,
        cache_creation: breakdown,
    })
}
pub(super) fn progress(previous: &wire::Usage, next: &wire::Usage) -> Result<(), CodecError> {
    for (old, new) in [
        (previous.input_tokens, next.input_tokens),
        (previous.output_tokens, next.output_tokens),
        (
            previous.cache_read_input_tokens.unwrap_or(0),
            next.cache_read_input_tokens.unwrap_or(0),
        ),
        (
            previous.cache_creation_input_tokens.unwrap_or(0),
            next.cache_creation_input_tokens.unwrap_or(0),
        ),
        (
            previous
                .cache_creation
                .as_ref()
                .map_or(0, |p| p.ephemeral_5m_input_tokens),
            next.cache_creation
                .as_ref()
                .map_or(0, |p| p.ephemeral_5m_input_tokens),
        ),
        (
            previous
                .cache_creation
                .as_ref()
                .map_or(0, |p| p.ephemeral_1h_input_tokens),
            next.cache_creation
                .as_ref()
                .map_or(0, |p| p.ephemeral_1h_input_tokens),
        ),
    ] {
        if new < old {
            return Err(bad());
        }
    }
    Ok(())
}
