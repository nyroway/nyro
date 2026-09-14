//! Stateful validation for ordered upstreams projected onto Chat field slots.
use super::*;
use std::collections::BTreeMap;

#[derive(Default)]
struct ChoiceState {
    active: BTreeMap<StreamPosition, StreamPartKind>,
    containers: BTreeMap<StreamItem, bool>,
    tools: BTreeMap<u32, StreamPosition>,
    last_start: Option<StreamPosition>,
    last_container: Option<StreamItem>,
    ordered: Option<bool>,
    saw_tool: bool,
    saw_refusal: bool,
    finished: bool,
}

/// Chat has no total order between its fields. Ordered text following a call
/// cannot be projected faithfully, even when it arrives in a later SSE frame.
pub struct StreamEncoder {
    model: String,
    limit: usize,
    choices: BTreeMap<u32, ChoiceState>,
    done: bool,
    failed: bool,
}

impl StreamEncoder {
    pub fn with_limit(model: String, limit: usize) -> Self {
        Self {
            model,
            limit,
            choices: BTreeMap::new(),
            done: false,
            failed: false,
        }
    }
    pub fn push(&mut self, event: &ChatEvent) -> Result<String, CodecError> {
        if self.done || self.failed {
            return Err(invalid("Chat stream already ended"));
        }
        let result = self.push_inner(event);
        if result.is_err() {
            self.failed = true;
        }
        result
    }
    fn push_inner(&mut self, event: &ChatEvent) -> Result<String, CodecError> {
        if let ChatEvent::Chunk(chunk) = event {
            for choice in &chunk.choices {
                if !self.choices.contains_key(&choice.index)
                    && self
                        .choices
                        .len()
                        .saturating_add(1)
                        .saturating_mul(std::mem::size_of::<ChoiceState>() + 64)
                        > self.limit
                {
                    return Err(invalid("Chat stream choice state exceeds limit"));
                }
                let state = self.choices.entry(choice.index).or_default();
                if state.finished
                    && (!choice.delta.events.is_empty() || choice.finish_reason.is_some())
                {
                    return Err(invalid("Chat output after choice completion"));
                }
                for part in &choice.delta.events {
                    super::super::validate_position(part)?;
                    let position = part.position;
                    let ordered = matches!(position.item, StreamItem::Ordered(_));
                    if state.ordered.is_some_and(|mode| mode != ordered) {
                        return Err(invalid(
                            "Chat cannot mix ordered parts and native field slots",
                        ));
                    }
                    state.ordered = Some(ordered);
                    match &part.delta {
                        PartDelta::ResponsesItemStart(start) => {
                            let (id, tool) = match start {
                                ResponsesItemStart::Message { id } => (id, false),
                                ResponsesItemStart::FunctionCall { id } => (id, true),
                            };
                            if id.trim().is_empty()
                                || state
                                    .last_container
                                    .is_some_and(|last| position.item <= last)
                                || state
                                    .last_start
                                    .is_some_and(|last| position.item <= last.item)
                                || state.containers.insert(position.item, tool).is_some()
                            {
                                return Err(invalid("invalid Chat source item start"));
                            }
                            state.last_container = Some(position.item);
                        }
                        PartDelta::ResponsesItemEnd(status) => {
                            if *status == nyro_protocol::openai::responses::ItemStatus::InProgress
                                || state.active.keys().any(|p| p.item == position.item)
                                || state.containers.remove(&position.item).is_none()
                            {
                                return Err(invalid("invalid Chat source item end"));
                            }
                        }
                        PartDelta::Start(kind) => {
                            if state.last_container.is_some()
                                && !state.containers.contains_key(&position.item)
                            {
                                return Err(invalid("Chat source part outside active container"));
                            }
                            if state.last_start.is_some_and(|old| position <= old)
                                || state.active.insert(position, *kind).is_some()
                            {
                                return Err(invalid("duplicate or regressing Chat source part"));
                            }
                            if let Some(tool) = state.containers.get(&position.item)
                                && *tool != (*kind == StreamPartKind::ToolCall)
                            {
                                return Err(invalid(
                                    "Chat source part does not match its container",
                                ));
                            }
                            state.last_start = Some(position);
                            if matches!(kind, StreamPartKind::Text | StreamPartKind::Refusal)
                                && state.saw_tool
                            {
                                return Err(invalid(
                                    "Chat cannot represent ordered content after tool calls",
                                ));
                            }
                        }
                        PartDelta::End => {
                            if state.active.remove(&position).is_none() {
                                return Err(invalid("Chat source part ended without start"));
                            }
                        }
                        PartDelta::Text(_) | PartDelta::Refusal(_) | PartDelta::ToolCall(_) => {
                            let kind = match &part.delta {
                                PartDelta::Text(_) => StreamPartKind::Text,
                                PartDelta::Refusal(_) => StreamPartKind::Refusal,
                                _ => StreamPartKind::ToolCall,
                            };
                            if matches!(position.item, StreamItem::Ordered(_)) {
                                if state.active.get(&position) != Some(&kind) {
                                    return Err(invalid(
                                        "Chat source payload outside matching part",
                                    ));
                                }
                                if state.active.iter().any(|(earlier, previous)| {
                                    *earlier < position
                                        && (kind != StreamPartKind::ToolCall
                                            || *previous != StreamPartKind::ToolCall)
                                }) || state.containers.iter().any(|(item, tool)| {
                                    *item < position.item
                                        && (kind != StreamPartKind::ToolCall || !*tool)
                                }) {
                                    return Err(invalid(
                                        "Chat cannot preserve overlapping ordered content",
                                    ));
                                }
                                if kind != StreamPartKind::ToolCall && state.saw_tool
                                    || kind == StreamPartKind::Text && state.saw_refusal
                                {
                                    return Err(invalid(
                                        "Chat cannot preserve interleaved source content",
                                    ));
                                }
                            }
                            match &part.delta {
                                PartDelta::ToolCall(call) => {
                                    if state.tools.get(&call.index).is_some_and(|p| *p != position)
                                        || state
                                            .tools
                                            .iter()
                                            .any(|(i, p)| *i != call.index && *p == position)
                                    {
                                        return Err(invalid("Chat source tool changed position"));
                                    }
                                    state.tools.insert(call.index, position);
                                    state.saw_tool = true;
                                }
                                PartDelta::Refusal(_) => state.saw_refusal = true,
                                _ => {}
                            }
                        }
                        _ => {
                            return Err(invalid(
                                "Chat cannot represent protocol-specific reasoning",
                            ));
                        }
                    }
                    let entries = state
                        .active
                        .len()
                        .saturating_add(state.containers.len())
                        .saturating_add(state.tools.len());
                    if entries.saturating_mul(96) > self.limit {
                        return Err(invalid("Chat stream position state exceeds limit"));
                    }
                }
                if choice.finish_reason.is_some() {
                    if !state.active.is_empty() || !state.containers.is_empty() {
                        return Err(invalid("Chat source completed with unfinished parts"));
                    }
                    state.finished = true;
                }
                let retained = self.choices.values().fold(0usize, |total, state| {
                    total
                        .saturating_add(std::mem::size_of::<ChoiceState>() + 64)
                        .saturating_add(
                            state
                                .active
                                .len()
                                .saturating_add(state.containers.len())
                                .saturating_add(state.tools.len())
                                .saturating_mul(96),
                        )
                });
                if retained > self.limit {
                    return Err(invalid("Chat stream total state exceeds limit"));
                }
            }
            if chunk.usage.is_none()
                && !chunk.choices.is_empty()
                && chunk.choices.iter().all(|c| {
                    c.finish_reason.is_none()
                        && c.delta.role.is_none()
                        && !c.delta.events.is_empty()
                        && c.delta.events.iter().all(|e| {
                            matches!(
                                e.delta,
                                PartDelta::Start(_)
                                    | PartDelta::End
                                    | PartDelta::ResponsesItemStart(_)
                                    | PartDelta::ResponsesItemEnd(_)
                            )
                        })
                })
            {
                return Ok(String::new());
            }
        } else {
            if self
                .choices
                .values()
                .any(|s| !s.active.is_empty() || !s.containers.is_empty())
            {
                return Err(invalid("Chat source ended with unfinished parts"));
            }
            self.done = true;
        }
        encode_chat_event(event, &self.model)
    }
}
