//! Responses repeats full output at termination: retained snapshots share the frame bound.
use super::*;
use nyro_protocol::framing::Event;
use std::collections::{BTreeMap, VecDeque};
const DEFAULT_LIMIT: usize = 1024 * 1024;

struct ItemState {
    item: wire::OutputItem,
    tool_index: Option<u32>,
    // 0 = accumulating, 1 = text/refusal done, 2 = content_part done.
    parts: Vec<u8>,
    arguments_done: bool,
    done: bool,
}
#[derive(Default)]
struct Identity {
    id: String,
    model: String,
    created: u64,
}
impl Identity {
    fn response(r: &wire::Response) -> Self {
        Self {
            id: r.id.clone(),
            model: r.model.clone(),
            created: r.created_at,
        }
    }
    fn matches(&self, r: &wire::Response) -> bool {
        self.id == r.id && self.model == r.model && self.created == r.created_at
    }
    fn chunk(&self, delta: Delta, finish: Option<String>, usage: Option<Usage>) -> ChatEvent {
        ChatEvent::Chunk(Box::new(ChatChunk {
            id: self.id.clone(),
            object: "chat.completion.chunk".into(),
            created: self.created,
            model: self.model.clone(),
            choices: vec![StreamChoice {
                index: 0,
                delta,
                finish_reason: finish,
                logprobs: None,
            }],
            usage,
            system_fingerprint: None,
            service_tier: None,
            obfuscation: None,
        }))
    }
}
pub struct StreamDecoder {
    identity: Option<Identity>,
    items: Vec<ItemState>,
    sequence: u64,
    in_progress: bool,
    terminal: bool,
    failed: bool,
    max_bytes: usize,
    retained: usize,
}
impl Default for StreamDecoder {
    fn default() -> Self {
        Self::new()
    }
}
impl StreamDecoder {
    pub fn new() -> Self {
        Self::with_limit(DEFAULT_LIMIT)
    }
    pub fn with_limit(max_bytes: usize) -> Self {
        Self {
            identity: None,
            items: Vec::new(),
            sequence: 0,
            in_progress: false,
            terminal: false,
            failed: false,
            max_bytes,
            retained: 0,
        }
    }
    pub fn push(&mut self, event: &Event) -> Result<Vec<ChatEvent>, CodecError> {
        if self.failed || self.terminal {
            return Err(bad("Responses stream is already terminal/failed"));
        }
        let result = self.push_inner(event);
        if result.is_err() {
            self.failed = true;
        }
        result
    }
    fn reserve(&mut self, n: usize) -> Result<(), CodecError> {
        self.retained = self
            .retained
            .checked_add(n)
            .filter(|n| *n <= self.max_bytes)
            .ok_or_else(|| bad("Responses snapshot exceeds byte limit"))?;
        Ok(())
    }
    fn push_inner(&mut self, event: &Event) -> Result<Vec<ChatEvent>, CodecError> {
        if event.data.len() > self.max_bytes {
            return Err(bad("Responses frame exceeds byte limit"));
        }
        let value: Value = serde_json::from_str(&event.data)?;
        validate_event_fields(&value)?;
        let e: wire::StreamEvent = serde_json::from_value(value)?;
        if event.event.as_deref().is_some_and(|name| name != e.r#type)
            || e.sequence_number != self.sequence
            || !e.logprobs.is_empty()
        {
            return Err(bad("Responses event name/sequence/logprobs mismatch"));
        }
        self.sequence = self
            .sequence
            .checked_add(1)
            .ok_or_else(|| bad("Responses sequence overflow"))?;
        let mut out = Vec::new();
        match e.r#type.as_str() {
            "response.created" | "response.in_progress" => {
                let r = e
                    .response
                    .ok_or_else(|| bad("missing Responses envelope"))?;
                validate_envelope(&r)?;
                if r.status != "in_progress"
                    || !r.output.is_empty()
                    || r.incomplete_details.is_some()
                {
                    return Err(bad("invalid Responses initial snapshot"));
                }
                if e.r#type == "response.created" {
                    if self.identity.is_some() {
                        return Err(bad("duplicate response.created"));
                    }
                    self.reserve(r.id.len() + r.model.len())?;
                    self.identity = Some(Identity::response(&r));
                    out.push(self.identity.as_ref().unwrap().chunk(
                        Delta {
                            role: Some(Role::Assistant),
                            ..Default::default()
                        },
                        None,
                        None,
                    ));
                } else {
                    if self.in_progress
                        || !self.items.is_empty()
                        || self.identity.as_ref().is_none_or(|id| !id.matches(&r))
                    {
                        return Err(bad("invalid response.in_progress identity/lifecycle"));
                    }
                    self.in_progress = true;
                }
            }
            "response.output_item.added" => {
                self.started()?;
                let index = e.output_index.ok_or_else(|| bad("missing output_index"))?;
                let item = e.item.ok_or_else(|| bad("missing output item"))?;
                if index != self.items.len()
                    || (!matches!(&item, wire::OutputItem::Reasoning(r) if r.status.is_none())
                        && item.status() != "in_progress")
                    || self.items.iter().any(|i| i.item.id() == item.id())
                {
                    return Err(bad("invalid output item index/identity/status"));
                }
                validate_items(std::slice::from_ref(&item), false)?;
                self.reserve(serde_json::to_vec(&item)?.len())?;
                let tool_index = match &item {
                    wire::OutputItem::Reasoning(r) => {
                        if !r.summary.is_empty() {
                            return Err(bad("unsupported initial reasoning content/output order"));
                        }
                        out.push(self.started()?.chunk(
                            positioned(
                                e.output_index.unwrap(),
                                0,
                                PartDelta::ResponsesReasoning(ResponsesReasoningDelta::Start {
                                    item: r.clone(),
                                }),
                            )?,
                            None,
                            None,
                        ));
                        None
                    }
                    wire::OutputItem::Message { id, content, .. } => {
                        if !content.is_empty() {
                            return Err(bad("unsupported initial message content/output order"));
                        }
                        out.push(self.started()?.chunk(
                            positioned(
                                index,
                                0,
                                PartDelta::ResponsesItemStart(ResponsesItemStart::Message {
                                    id: id.clone(),
                                }),
                            )?,
                            None,
                            None,
                        ));
                        None
                    }
                    wire::OutputItem::FunctionCall {
                        id,
                        call_id,
                        name,
                        arguments,
                        ..
                    } => {
                        let identity = self.started()?;
                        out.reserve_exact(3);
                        let each = std::mem::size_of::<ChatChunk>()
                            + std::mem::size_of::<StreamChoice>()
                            + std::mem::size_of::<PositionedDelta>()
                            + identity.id.len()
                            + identity.model.len()
                            + "chat.completion.chunk".len();
                        let expanded = each
                            .saturating_mul(3)
                            .saturating_add(
                                out.capacity()
                                    .saturating_mul(std::mem::size_of::<ChatEvent>()),
                            )
                            .saturating_add(id.len())
                            .saturating_add(call_id.len())
                            .saturating_add(name.len())
                            .saturating_add(arguments.len());
                        if expanded > self.max_bytes {
                            return Err(bad(
                                "Responses function start expansion exceeds byte limit",
                            ));
                        }
                        out.push(self.started()?.chunk(
                            positioned(
                                index,
                                0,
                                PartDelta::ResponsesItemStart(ResponsesItemStart::FunctionCall {
                                    id: id.clone(),
                                }),
                            )?,
                            None,
                            None,
                        ));
                        out.push(self.started()?.chunk(
                            positioned(index, 0, PartDelta::Start(StreamPartKind::ToolCall))?,
                            None,
                            None,
                        ));
                        if self.items.iter().any(|i|matches!(&i.item,wire::OutputItem::FunctionCall { call_id:id,.. } if id==call_id)) { return Err(bad("duplicate function call identity")); }
                        let index = u32::try_from(
                            self.items.iter().filter(|i| i.tool_index.is_some()).count(),
                        )
                        .map_err(|_| bad("too many tool calls"))?;
                        out.push(self.started()?.chunk(
                            positioned(
                                e.output_index.unwrap(),
                                0,
                                PartDelta::ToolCall(ToolCallDelta {
                                    gemini: None,
                                    index,
                                    id: Some(call_id.clone()),
                                    r#type: Some(FunctionType::Function),
                                    function: Some(FunctionDelta {
                                        name: Some(name.clone()),
                                        arguments: Some(arguments.clone()),
                                    }),
                                }),
                            )?,
                            None,
                            None,
                        ));
                        Some(index)
                    }
                };
                self.items.push(ItemState {
                    item,
                    tool_index,
                    parts: Vec::new(),
                    arguments_done: false,
                    done: false,
                });
            }
            "response.reasoning_summary_part.added"
            | "response.reasoning_summary_text.delta"
            | "response.reasoning_summary_text.done"
            | "response.reasoning_summary_part.done" => {
                let index = e
                    .summary_index
                    .ok_or_else(|| bad("missing summary_index"))?;
                self.reserve(e.delta.as_ref().map_or(0, String::len))?;
                if e.r#type == "response.reasoning_summary_part.added" {
                    self.reserve(std::mem::size_of::<wire::SummaryPart>() + 1)?;
                }
                let state = self.item_mut(e.output_index, e.item_id.as_deref())?;
                let wire::OutputItem::Reasoning(item) = &mut state.item else {
                    return Err(bad("summary on non-reasoning item"));
                };
                let delta = match e.r#type.as_str() {
                    "response.reasoning_summary_part.added" => {
                        let Some(wire::StreamPart::Summary(part)) = e.part else {
                            return Err(bad("missing summary part"));
                        };
                        if index != item.summary.len() || !part.text().is_empty() {
                            return Err(bad("invalid summary part start"));
                        }
                        item.summary.push(part);
                        state.parts.push(0);
                        ResponsesReasoningDelta::SummaryStart
                    }
                    "response.reasoning_summary_text.delta" => {
                        let text = e.delta.ok_or_else(|| bad("missing summary delta"))?;
                        if state.parts.get(index) != Some(&0) {
                            return Err(bad("summary delta outside active part"));
                        }
                        item.summary[index].text_mut().push_str(&text);
                        ResponsesReasoningDelta::SummaryText { text }
                    }
                    "response.reasoning_summary_text.done" => {
                        if state.parts.get(index) != Some(&0)
                            || e.text.as_deref() != Some(item.summary[index].text())
                        {
                            return Err(bad("summary text done conflicts with deltas"));
                        }
                        state.parts[index] = 1;
                        ResponsesReasoningDelta::SummaryTextDone
                    }
                    _ => {
                        let Some(wire::StreamPart::Summary(part)) = e.part else {
                            return Err(bad("missing summary part"));
                        };
                        if state.parts.get(index) != Some(&1)
                            || item.summary.get(index) != Some(&part)
                            || e.status.as_deref().is_some_and(|s| s != "incomplete")
                        {
                            return Err(bad("summary part done conflicts with lifecycle/content"));
                        }
                        state.parts[index] = 2;
                        ResponsesReasoningDelta::SummaryDone {
                            incomplete: e.status.is_some(),
                        }
                    }
                };
                out.push(self.started()?.chunk(
                    positioned(
                        e.output_index.unwrap(),
                        index,
                        PartDelta::ResponsesReasoning(delta),
                    )?,
                    None,
                    None,
                ));
            }
            "response.content_part.added" => {
                let Some(wire::StreamPart::Output(part)) = e.part else {
                    return Err(bad("missing/invalid content part"));
                };
                validate_part(&part)?;
                if !part_text(&part).is_empty() {
                    return Err(bad("initial Responses content part must be empty"));
                }
                self.reserve(serde_json::to_vec(&part)?.len())?;
                let state = self.item_mut(e.output_index, e.item_id.as_deref())?;
                let wire::OutputItem::Message { content, .. } = &mut state.item else {
                    return Err(bad("content on function call"));
                };
                if e.content_index != Some(content.len()) {
                    return Err(bad("invalid content part lifecycle/index"));
                }
                let kind = match &part {
                    wire::OutputPart::OutputText { .. } => StreamPartKind::Text,
                    wire::OutputPart::Refusal { .. } => StreamPartKind::Refusal,
                };
                content.push(part);
                state.parts.push(0);
                out.push(self.started()?.chunk(
                    positioned(
                        e.output_index.unwrap(),
                        e.content_index.unwrap(),
                        PartDelta::Start(kind),
                    )?,
                    None,
                    None,
                ));
            }
            "response.output_text.delta" | "response.refusal.delta" => {
                let delta = e.delta.ok_or_else(|| bad("missing content delta"))?;
                self.reserve(delta.len())?;
                let state = self.item_mut(e.output_index, e.item_id.as_deref())?;
                let index = e
                    .content_index
                    .ok_or_else(|| bad("missing content_index"))?;
                if state.parts.get(index) != Some(&0) {
                    return Err(bad("content delta outside active part"));
                }
                let wire::OutputItem::Message { content, .. } = &mut state.item else {
                    return Err(bad("content on function call"));
                };
                let part = content
                    .get_mut(index)
                    .ok_or_else(|| bad("unknown content part"))?;
                let d = match part {
                    wire::OutputPart::OutputText { text, .. }
                        if e.r#type == "response.output_text.delta" =>
                    {
                        text.push_str(&delta);
                        positioned(e.output_index.unwrap(), index, PartDelta::Text(delta))?
                    }
                    wire::OutputPart::Refusal { refusal }
                        if e.r#type == "response.refusal.delta" =>
                    {
                        refusal.push_str(&delta);
                        positioned(e.output_index.unwrap(), index, PartDelta::Refusal(delta))?
                    }
                    _ => return Err(bad("content delta kind mismatch")),
                };
                out.push(self.started()?.chunk(d, None, None));
            }
            "response.output_text.done" | "response.refusal.done" => {
                let state = self.item_mut(e.output_index, e.item_id.as_deref())?;
                let index = e
                    .content_index
                    .ok_or_else(|| bad("missing content_index"))?;
                if state.parts.get(index) != Some(&0) {
                    return Err(bad("duplicate/missing content done"));
                }
                let wire::OutputItem::Message { content, .. } = &state.item else {
                    return Err(bad("content on function call"));
                };
                let agrees = match &content[index] {
                    wire::OutputPart::OutputText { text, .. } => {
                        e.r#type == "response.output_text.done" && e.text.as_ref() == Some(text)
                    }
                    wire::OutputPart::Refusal { refusal } => {
                        e.r#type == "response.refusal.done" && e.refusal.as_ref() == Some(refusal)
                    }
                };
                if !agrees {
                    return Err(bad("content done conflicts with deltas"));
                }
                state.parts[index] = 1;
            }
            "response.content_part.done" => {
                let Some(wire::StreamPart::Output(part)) = e.part else {
                    return Err(bad("missing/invalid content part"));
                };
                validate_part(&part)?;
                let state = self.item_mut(e.output_index, e.item_id.as_deref())?;
                let index = e
                    .content_index
                    .ok_or_else(|| bad("missing content_index"))?;
                let wire::OutputItem::Message { content, .. } = &state.item else {
                    return Err(bad("content on function call"));
                };
                if state.parts.get(index) != Some(&1) || content.get(index) != Some(&part) {
                    return Err(bad("content_part.done conflicts with lifecycle/content"));
                }
                state.parts[index] = 2;
                out.push(self.started()?.chunk(
                    positioned(e.output_index.unwrap(), index, PartDelta::End)?,
                    None,
                    None,
                ));
            }
            "response.function_call_arguments.delta" => {
                let delta = e.delta.ok_or_else(|| bad("missing argument delta"))?;
                self.reserve(delta.len())?;
                let state = self.item_mut(e.output_index, e.item_id.as_deref())?;
                if state.arguments_done {
                    return Err(bad("arguments after done"));
                }
                let wire::OutputItem::FunctionCall { arguments, .. } = &mut state.item else {
                    return Err(bad("arguments on message"));
                };
                arguments.push_str(&delta);
                let index = state.tool_index.unwrap();
                out.push(self.started()?.chunk(
                    positioned(
                        e.output_index.unwrap(),
                        0,
                        PartDelta::ToolCall(ToolCallDelta {
                            gemini: None,
                            index,
                            id: None,
                            r#type: None,
                            function: Some(FunctionDelta {
                                name: None,
                                arguments: Some(delta),
                            }),
                        }),
                    )?,
                    None,
                    None,
                ));
            }
            "response.function_call_arguments.done" => {
                let state = self.item_mut(e.output_index, e.item_id.as_deref())?;
                let wire::OutputItem::FunctionCall {
                    arguments, name, ..
                } = &state.item
                else {
                    return Err(bad("arguments on message"));
                };
                if state.arguments_done
                    || e.arguments.as_ref() != Some(arguments)
                    || e.name.as_ref().is_some_and(|n| n != name)
                {
                    return Err(bad("arguments done conflicts with deltas/identity"));
                }
                state.arguments_done = true;
                out.push(self.started()?.chunk(
                    positioned(e.output_index.unwrap(), 0, PartDelta::End)?,
                    None,
                    None,
                ));
            }
            "response.output_item.done" => {
                let item = e.item.ok_or_else(|| bad("missing output item"))?;
                validate_items(std::slice::from_ref(&item), true)?;
                if let wire::OutputItem::Reasoning(r) = &item {
                    // The added item's ciphertext may be partial. Charge and preserve the
                    // final ciphertext from done; only summary/identity must match deltas.
                    self.reserve(r.encrypted_content.as_ref().map_or(0, String::len))?;
                }
                let state = self.item_mut(e.output_index, Some(item.id()))?;
                if state.parts.iter().any(|p| *p != 2)
                    || (state.tool_index.is_some() && !state.arguments_done)
                {
                    return Err(bad("output item done before content/arguments done"));
                }
                let reasoning =
                    if let (wire::OutputItem::Reasoning(old), wire::OutputItem::Reasoning(new)) =
                        (&mut state.item, &item)
                    {
                        if old.id != new.id || old.summary != new.summary {
                            return Err(bad("reasoning snapshot conflicts with deltas"));
                        }
                        *old = new.clone();
                        Some(new.clone())
                    } else {
                        set_status(&mut state.item, item.status());
                        None
                    };
                if state.item != item {
                    return Err(bad("output item snapshot conflicts with deltas"));
                }
                state.done = true;
                if let Some(item) = reasoning {
                    out.push(self.started()?.chunk(
                        positioned(
                            e.output_index.unwrap(),
                            0,
                            PartDelta::ResponsesReasoning(ResponsesReasoningDelta::Done { item }),
                        )?,
                        None,
                        None,
                    ));
                } else {
                    out.push(self.started()?.chunk(
                        positioned(
                            e.output_index.unwrap(),
                            0,
                            PartDelta::ResponsesItemEnd(item_status(item.status())?),
                        )?,
                        None,
                        None,
                    ));
                }
            }
            "response.completed" | "response.incomplete" => {
                let r = e.response.ok_or_else(|| bad("missing terminal response"))?;
                validate_envelope(&r)?;
                validate_items(&r.output, true)?;
                if e.r#type != format!("response.{}", r.status) {
                    return Err(bad("terminal event/status mismatch"));
                }
                let finish = finish_reason(&r)?;
                if self.items.is_empty() {
                    // Bound expansion before cloning repeated item/response identities.
                    let count = r.output.iter().fold(2usize, |n, item| {
                        n.saturating_add(match item {
                            wire::OutputItem::Reasoning(r) => {
                                r.summary.len().saturating_mul(4).saturating_add(2)
                            }
                            wire::OutputItem::Message { content, .. } => {
                                content.len().saturating_mul(3).saturating_add(2)
                            }
                            wire::OutputItem::FunctionCall { .. } => 5,
                        })
                    });
                    let each = std::mem::size_of::<ChatEvent>()
                        + std::mem::size_of::<ChatChunk>()
                        + std::mem::size_of::<StreamChoice>()
                        + std::mem::size_of::<PositionedDelta>()
                        + std::mem::size_of::<ResponsesReasoningDelta>()
                        + r.id.len()
                        + r.model.len();
                    let expanded = count
                        .saturating_mul(each)
                        .saturating_add(event.data.len().saturating_mul(2));
                    if expanded > self.max_bytes {
                        return Err(bad("Responses snapshot expansion exceeds byte limit"));
                    }
                }
                if let Some(id) = &self.identity {
                    if !id.matches(&r) {
                        return Err(bad("terminal response identity mismatch"));
                    }
                } else {
                    self.identity = Some(Identity::response(&r));
                    out.push(self.started()?.chunk(
                        Delta {
                            role: Some(Role::Assistant),
                            ..Default::default()
                        },
                        None,
                        None,
                    ));
                }
                if self.items.is_empty() {
                    // Some compatible servers send only the terminal snapshot. Emit all boundaries once.
                    let mut tool_index = 0;
                    for (output_index, item) in r.output.iter().enumerate() {
                        let mut deltas = Vec::new();
                        match item {
                            wire::OutputItem::Reasoning(item) => {
                                for (part_index, delta) in snapshot_deltas(item) {
                                    deltas.push(positioned(
                                        output_index,
                                        part_index,
                                        PartDelta::ResponsesReasoning(delta),
                                    )?);
                                }
                            }
                            wire::OutputItem::Message {
                                id,
                                status,
                                content,
                                ..
                            } => {
                                deltas.push(positioned(
                                    output_index,
                                    0,
                                    PartDelta::ResponsesItemStart(ResponsesItemStart::Message {
                                        id: id.clone(),
                                    }),
                                )?);
                                for (part_index, part) in content.iter().enumerate() {
                                    let kind = match part {
                                        wire::OutputPart::OutputText { .. } => StreamPartKind::Text,
                                        wire::OutputPart::Refusal { .. } => StreamPartKind::Refusal,
                                    };
                                    deltas.push(positioned(
                                        output_index,
                                        part_index,
                                        PartDelta::Start(kind),
                                    )?);
                                    deltas.push(positioned(
                                        output_index,
                                        part_index,
                                        part_delta(part),
                                    )?);
                                    deltas.push(positioned(
                                        output_index,
                                        part_index,
                                        PartDelta::End,
                                    )?);
                                }
                                deltas.push(positioned(
                                    output_index,
                                    0,
                                    PartDelta::ResponsesItemEnd(item_status(status)?),
                                )?);
                            }
                            wire::OutputItem::FunctionCall {
                                id,
                                status,
                                call_id,
                                name,
                                arguments,
                            } => {
                                deltas.push(positioned(
                                    output_index,
                                    0,
                                    PartDelta::ResponsesItemStart(
                                        ResponsesItemStart::FunctionCall { id: id.clone() },
                                    ),
                                )?);
                                deltas.push(positioned(
                                    output_index,
                                    0,
                                    PartDelta::Start(StreamPartKind::ToolCall),
                                )?);
                                deltas.push(positioned(
                                    output_index,
                                    0,
                                    PartDelta::ToolCall(ToolCallDelta {
                                        gemini: None,
                                        index: tool_index,
                                        id: Some(call_id.clone()),
                                        r#type: Some(FunctionType::Function),
                                        function: Some(FunctionDelta {
                                            name: Some(name.clone()),
                                            arguments: Some(arguments.clone()),
                                        }),
                                    }),
                                )?);
                                deltas.push(positioned(output_index, 0, PartDelta::End)?);
                                deltas.push(positioned(
                                    output_index,
                                    0,
                                    PartDelta::ResponsesItemEnd(item_status(status)?),
                                )?);
                                tool_index = tool_index
                                    .checked_add(1)
                                    .ok_or_else(|| bad("too many tool calls"))?;
                            }
                        }
                        for delta in deltas {
                            out.push(self.started()?.chunk(delta, None, None));
                        }
                    }
                } else if self.items.len() != r.output.len()
                    || self
                        .items
                        .iter()
                        .zip(&r.output)
                        .any(|(s, i)| !s.done || s.item != *i)
                {
                    return Err(bad(
                        "terminal snapshot conflicts with streamed output/lifecycle",
                    ));
                }
                let usage = r.usage.map(decode_usage).transpose()?;
                // Terminal-only snapshots may expand to many deltas. Account known
                // usage before a target encoder can reject any of those deltas.
                if let Some(ChatEvent::Chunk(first)) = out.first_mut() {
                    first.usage = usage.clone();
                }
                out.push(
                    self.started()?
                        .chunk(Delta::default(), Some(finish.into()), usage),
                );
                self.terminal = true;
                out.push(ChatEvent::Done);
            }
            _ => return Err(bad("unsupported/failed Responses stream event")),
        }
        Ok(out)
    }
    fn started(&self) -> Result<&Identity, CodecError> {
        self.identity
            .as_ref()
            .ok_or_else(|| bad("Responses event before response.created"))
    }
    fn item_mut(
        &mut self,
        index: Option<usize>,
        id: Option<&str>,
    ) -> Result<&mut ItemState, CodecError> {
        self.started()?;
        let item = self
            .items
            .get_mut(index.ok_or_else(|| bad("missing output_index"))?)
            .ok_or_else(|| bad("unknown output_index"))?;
        if id != Some(item.item.id()) || item.done {
            return Err(bad("invalid item identity/lifecycle"));
        }
        Ok(item)
    }
    pub fn finish(&mut self) -> Result<Vec<ChatEvent>, CodecError> {
        if self.failed || !self.terminal {
            self.failed = true;
            return Err(bad(
                "Responses stream ended without successful terminal event",
            ));
        }
        Ok(Vec::new())
    }
}
fn part_text(part: &wire::OutputPart) -> &str {
    match part {
        wire::OutputPart::OutputText { text, .. } => text,
        wire::OutputPart::Refusal { refusal } => refusal,
    }
}
fn part_delta(part: &wire::OutputPart) -> PartDelta {
    match part {
        wire::OutputPart::OutputText { text, .. } => PartDelta::Text(text.clone()),
        wire::OutputPart::Refusal { refusal } => PartDelta::Refusal(refusal.clone()),
    }
}
fn positioned(item: usize, part: usize, delta: PartDelta) -> Result<Delta, CodecError> {
    Ok(Delta {
        role: None,
        events: vec![PositionedDelta {
            position: StreamPosition {
                item: StreamItem::Ordered(
                    u32::try_from(item).map_err(|_| bad("output index overflow"))?,
                ),
                part: u32::try_from(part).map_err(|_| bad("part index overflow"))?,
            },
            delta,
        }],
    })
}
fn set_status(item: &mut wire::OutputItem, status: &str) {
    match item {
        wire::OutputItem::Reasoning(r) => {
            r.status = Some(match status {
                "in_progress" => wire::ItemStatus::InProgress,
                "incomplete" => wire::ItemStatus::Incomplete,
                _ => wire::ItemStatus::Completed,
            })
        }
        wire::OutputItem::Message { status: s, .. }
        | wire::OutputItem::FunctionCall { status: s, .. } => *s = status.into(),
    }
}

#[derive(Clone, Copy, PartialEq, Eq)]
enum OutputKind {
    Message,
    FunctionCall,
    Reasoning,
}
struct OutputPartState {
    index: usize,
    kind: StreamPartKind,
    explicit: bool,
    phase: u8,
}
struct OutputSource {
    index: usize,
    kind: OutputKind,
    explicit: bool,
    started: bool,
    ended: bool,
    parts: BTreeMap<u32, OutputPartState>,
}
struct PendingFrame {
    kind: String,
    fields: String,
    index: usize,
    added: bool,
    cost: usize,
}
pub struct StreamEncoder {
    public_model: String,
    identity: Option<Identity>,
    items: Vec<wire::OutputItem>,
    sources: BTreeMap<StreamItem, OutputSource>,
    tools: BTreeMap<u32, StreamPosition>,
    emitted_items: usize,
    pending_frames: VecDeque<PendingFrame>,
    finish: Option<String>,
    usage: Option<Usage>,
    sequence: u64,
    terminal: bool,
    failed: bool,
    max_bytes: usize,
    retained: usize,
}
impl StreamEncoder {
    pub fn new(public_model: String) -> Self {
        Self::with_limit(public_model, DEFAULT_LIMIT)
    }
    pub fn with_limit(public_model: String, max_bytes: usize) -> Self {
        Self {
            public_model,
            identity: None,
            items: Vec::new(),
            sources: BTreeMap::new(),
            tools: BTreeMap::new(),
            emitted_items: 0,
            pending_frames: VecDeque::new(),
            finish: None,
            usage: None,
            sequence: 0,
            terminal: false,
            failed: false,
            max_bytes,
            retained: 0,
        }
    }
    pub fn push(&mut self, event: &ChatEvent) -> Result<String, CodecError> {
        if self.failed || self.terminal {
            return Err(bad("Responses encoder is terminal/failed"));
        }
        let result = self.push_inner(event);
        if result.is_err() {
            self.failed = true;
        }
        result
    }
    fn reserve(&mut self, n: usize) -> Result<(), CodecError> {
        self.retained = self
            .retained
            .checked_add(n)
            .filter(|n| *n <= self.max_bytes)
            .ok_or_else(|| bad("Responses snapshot exceeds byte limit"))?;
        Ok(())
    }
    fn emit(&mut self, kind: &str, fields: Value, out: &mut String) -> Result<(), CodecError> {
        let added = kind == "response.output_item.added";
        if let Some(index) = fields.get("output_index").and_then(Value::as_u64) {
            let index = usize::try_from(index).map_err(|_| bad("output index overflow"))?;
            // A function's identity may follow its Start. Hold later items until
            // the required output_item.added can be emitted for that function.
            if index > self.emitted_items || (index == self.emitted_items && !added) {
                let fields = serde_json::to_string(&fields)?;
                let cost = fields.len() + kind.len() + 2 * std::mem::size_of::<PendingFrame>();
                self.reserve(cost)?;
                self.pending_frames.push_back(PendingFrame {
                    kind: kind.into(),
                    fields,
                    index,
                    added,
                    cost,
                });
                return Ok(());
            }
        }
        self.emit_ready(kind, fields, out)?;
        if added {
            self.emitted_items += 1;
        }
        while let Some(index) = self.pending_frames.iter().position(|frame| {
            frame.index < self.emitted_items || (frame.added && frame.index == self.emitted_items)
        }) {
            let frame = self.pending_frames.remove(index).unwrap();
            self.retained -= frame.cost;
            self.emit_ready(&frame.kind, serde_json::from_str(&frame.fields)?, out)?;
            if frame.added {
                self.emitted_items += 1;
            }
        }
        Ok(())
    }
    fn emit_ready(
        &mut self,
        kind: &str,
        mut fields: Value,
        out: &mut String,
    ) -> Result<(), CodecError> {
        fields["type"] = json!(kind);
        fields["sequence_number"] = json!(self.sequence);
        self.sequence = self
            .sequence
            .checked_add(1)
            .ok_or_else(|| bad("sequence overflow"))?;
        let data = serde_json::to_string(&fields)?;
        if data.len() > self.max_bytes
            || out
                .len()
                .saturating_add(data.len())
                .saturating_add(kind.len() + 16)
                > self.max_bytes
        {
            return Err(bad("Responses encoded frame batch exceeds limit"));
        }
        out.push_str("event: ");
        out.push_str(kind);
        out.push_str("\ndata: ");
        out.push_str(&data);
        out.push_str("\n\n");
        Ok(())
    }
    fn response(&self, finish: Option<&str>) -> Result<Value, CodecError> {
        let id = self
            .identity
            .as_ref()
            .ok_or_else(|| bad("missing stream identity"))?;
        envelope(
            &id.id,
            id.created,
            &self.public_model,
            json!(self.items),
            finish,
            self.usage.as_ref(),
        )
    }
    fn new_source(
        &mut self,
        key: StreamItem,
        kind: OutputKind,
        id: Option<&str>,
        explicit: bool,
        out: &mut String,
    ) -> Result<usize, CodecError> {
        if self.sources.contains_key(&key) {
            return Err(bad("duplicate output item start"));
        }
        if let StreamItem::Ordered(index) = key
            && self
                .sources
                .keys()
                .any(|key| matches!(key,StreamItem::Ordered(old) if *old>index))
        {
            return Err(bad("output item order regressed"));
        }
        let index = self.items.len();
        let id = id.map(str::to_owned).unwrap_or_else(|| {
            format!(
                "{}_{}_{}",
                if kind == OutputKind::Message {
                    "msg"
                } else {
                    "fc"
                },
                self.identity.as_ref().unwrap().id,
                index
            )
        });
        nonempty(&id)?;
        if self.items.iter().any(|item| item.id() == id) {
            return Err(bad("duplicate output item id"));
        }
        let item = match kind {
            OutputKind::Message => wire::OutputItem::Message {
                id,
                role: "assistant".into(),
                status: "in_progress".into(),
                content: Vec::new(),
            },
            OutputKind::FunctionCall => wire::OutputItem::FunctionCall {
                id,
                call_id: String::new(),
                name: String::new(),
                arguments: String::new(),
                status: "in_progress".into(),
            },
            OutputKind::Reasoning => return Err(bad("reasoning requires a typed start")),
        };
        self.reserve(
            std::mem::size_of::<OutputSource>()
                + std::mem::size_of::<StreamItem>()
                + 32
                + serde_json::to_vec(&item)?.len(),
        )?;
        if kind == OutputKind::Message {
            self.emit(
                "response.output_item.added",
                json!({"output_index":index,"item":item}),
                out,
            )?;
        }
        self.items.push(item);
        self.sources.insert(
            key,
            OutputSource {
                index,
                kind,
                explicit,
                started: kind == OutputKind::Message,
                ended: false,
                parts: BTreeMap::new(),
            },
        );
        Ok(index)
    }
    fn source(&self, key: StreamItem) -> Result<&OutputSource, CodecError> {
        let source = self
            .sources
            .get(&key)
            .ok_or_else(|| bad("unknown output item position"))?;
        if source.ended {
            return Err(bad("event after output item end"));
        }
        Ok(source)
    }
    fn start_part(
        &mut self,
        position: StreamPosition,
        kind: StreamPartKind,
        explicit: bool,
        out: &mut String,
    ) -> Result<(), CodecError> {
        let output_kind = if kind == StreamPartKind::ToolCall {
            OutputKind::FunctionCall
        } else {
            OutputKind::Message
        };
        if !self.sources.contains_key(&position.item) {
            self.new_source(position.item, output_kind, None, false, out)?;
        }
        let source = self.source(position.item)?;
        if source.kind != output_kind
            || source.parts.contains_key(&position.part)
            || (matches!(position.item, StreamItem::Ordered(_))
                && position.part as usize != source.parts.len())
        {
            return Err(bad("invalid content part start/order/kind"));
        }
        let oi = source.index;
        self.reserve(std::mem::size_of::<OutputPartState>() + std::mem::size_of::<u32>() + 32)?;
        let ci = match &mut self.items[oi] {
            wire::OutputItem::Message { content, .. } => {
                let part = if kind == StreamPartKind::Refusal {
                    wire::OutputPart::Refusal {
                        refusal: String::new(),
                    }
                } else {
                    wire::OutputPart::OutputText {
                        text: String::new(),
                        annotations: Vec::new(),
                        logprobs: Vec::new(),
                    }
                };
                let index = content.len();
                content.push(part);
                index
            }
            wire::OutputItem::FunctionCall { .. } => 0,
            _ => return Err(bad("ordinary part inside reasoning item")),
        };
        self.sources.get_mut(&position.item).unwrap().parts.insert(
            position.part,
            OutputPartState {
                index: ci,
                kind,
                explicit,
                phase: 0,
            },
        );
        if let wire::OutputItem::Message { id, content, .. } = &self.items[oi] {
            self.emit(
                "response.content_part.added",
                json!({"output_index":oi,"item_id":id,"content_index":ci,"part":content[ci]}),
                out,
            )?;
        }
        Ok(())
    }
    fn text(
        &mut self,
        position: StreamPosition,
        text: &str,
        refusal: bool,
        out: &mut String,
    ) -> Result<(), CodecError> {
        let kind = if refusal {
            StreamPartKind::Refusal
        } else {
            StreamPartKind::Text
        };
        if !self
            .sources
            .get(&position.item)
            .is_some_and(|s| s.parts.contains_key(&position.part))
        {
            if self.sources.get(&position.item).is_some_and(|s| s.explicit) {
                return Err(bad("text without explicit part start"));
            }
            self.start_part(position, kind, false, out)?;
        }
        let source = self.source(position.item)?;
        let part = source
            .parts
            .get(&position.part)
            .ok_or_else(|| bad("unknown text part"))?;
        if source.kind != OutputKind::Message || part.kind != kind || part.phase != 0 {
            return Err(bad("text outside active matching part"));
        }
        let (oi, ci) = (source.index, part.index);
        self.reserve(text.len())?;
        let wire::OutputItem::Message { id, content, .. } = &mut self.items[oi] else {
            unreachable!()
        };
        match &mut content[ci] {
            wire::OutputPart::OutputText { text: value, .. } => value.push_str(text),
            wire::OutputPart::Refusal { refusal: value } => value.push_str(text),
        }
        let fields = json!({"output_index":oi,"item_id":id,"content_index":ci,"delta":text});
        self.emit(
            if refusal {
                "response.refusal.delta"
            } else {
                "response.output_text.delta"
            },
            fields,
            out,
        )
    }
    fn tool(
        &mut self,
        position: StreamPosition,
        call: &ToolCallDelta,
        out: &mut String,
    ) -> Result<(), CodecError> {
        if call.gemini.is_some() {
            return Err(bad("Responses cannot represent Gemini signatures"));
        }
        if let Some(old) = self.tools.get(&call.index) {
            if *old != position {
                return Err(bad("tool call changed position"));
            }
        } else {
            if self.tools.values().any(|p| *p == position) {
                return Err(bad("tool position reused by another call"));
            }
            if matches!(position.item, StreamItem::OpenAiTool(_)) {
                if call.index as usize != self.tools.len() {
                    return Err(bad("noncontiguous Chat tool index"));
                }
                if self
                    .sources
                    .get(&StreamItem::OpenAiMessage)
                    .is_some_and(|s| !s.ended)
                {
                    self.close_implicit(StreamItem::OpenAiMessage, "completed", out)?;
                }
            }
            self.reserve(std::mem::size_of::<(u32, StreamPosition)>() + 32)?;
            self.tools.insert(call.index, position);
        }
        if !self
            .sources
            .get(&position.item)
            .is_some_and(|s| s.parts.contains_key(&position.part))
        {
            if self.sources.get(&position.item).is_some_and(|s| s.explicit) {
                return Err(bad("tool delta without explicit argument start"));
            }
            self.start_part(position, StreamPartKind::ToolCall, false, out)?;
        }
        let source = self.source(position.item)?;
        let part = source.parts.get(&position.part).unwrap();
        if source.kind != OutputKind::FunctionCall || part.phase != 0 {
            return Err(bad("arguments outside active function"));
        }
        let oi = source.index;
        let first = !source.started;
        self.reserve(
            call.id.as_ref().map_or(0, String::len)
                + call.function.as_ref().map_or(0, |f| {
                    f.name.as_ref().map_or(0, String::len)
                        + f.arguments.as_ref().map_or(0, String::len)
                }),
        )?;
        let wire::OutputItem::FunctionCall {
            call_id,
            name,
            arguments,
            ..
        } = &mut self.items[oi]
        else {
            unreachable!()
        };
        if first {
            *call_id = call
                .id
                .clone()
                .ok_or_else(|| bad("function start requires call_id"))?;
            *name = call
                .function
                .as_ref()
                .and_then(|f| f.name.clone())
                .ok_or_else(|| bad("function start requires name"))?;
            nonempty(call_id)?;
            nonempty(name)?;
        } else if call.id.as_ref().is_some_and(|id| id != call_id)
            || call
                .function
                .as_ref()
                .and_then(|f| f.name.as_ref())
                .is_some_and(|n| n != name)
        {
            return Err(bad("function identity changed"));
        }
        let delta = call.function.as_ref().and_then(|f| f.arguments.as_deref());
        if let Some(delta) = delta {
            arguments.push_str(delta);
        }
        if first {
            let call_id = match &self.items[oi] {
                wire::OutputItem::FunctionCall { call_id, .. } => call_id,
                _ => unreachable!(),
            };
            if self.items.iter().enumerate().any(|(i,item)|i!=oi && matches!(item,wire::OutputItem::FunctionCall {call_id:old,..} if old==call_id)){return Err(bad("duplicate function call_id"));}
            let mut initial = self.items[oi].clone();
            if let wire::OutputItem::FunctionCall { arguments, .. } = &mut initial {
                arguments.clear();
            }
            self.emit(
                "response.output_item.added",
                json!({"output_index":oi,"item":initial}),
                out,
            )?;
            self.sources.get_mut(&position.item).unwrap().started = true;
        }
        if let Some(delta) = delta {
            self.emit(
                "response.function_call_arguments.delta",
                json!({"output_index":oi,"item_id":self.items[oi].id(),"delta":delta}),
                out,
            )?;
        }
        Ok(())
    }
    fn end_part(
        &mut self,
        position: StreamPosition,
        explicit_end: bool,
        out: &mut String,
    ) -> Result<(), CodecError> {
        let source = self.source(position.item)?;
        let part = source
            .parts
            .get(&position.part)
            .ok_or_else(|| bad("part end without start"))?;
        if part.phase != 0 || !source.started {
            return Err(bad("part already ended or missing payload"));
        }
        let (oi, ci, kind) = (source.index, part.index, source.kind);
        match &self.items[oi] {
            wire::OutputItem::Message { id, content, .. } => {
                let part = content[ci].clone();
                let id = id.clone();
                let (event, key) = match &part {
                    wire::OutputPart::OutputText { .. } => ("response.output_text.done", "text"),
                    wire::OutputPart::Refusal { .. } => ("response.refusal.done", "refusal"),
                };
                let mut fields = json!({"output_index":oi,"item_id":id,"content_index":ci});
                fields[key] = json!(part_text(&part));
                self.emit(event, fields, out)?;
                self.emit(
                    "response.content_part.done",
                    json!({"output_index":oi,"item_id":id,"content_index":ci,"part":part}),
                    out,
                )?;
            }
            wire::OutputItem::FunctionCall {
                id,
                name,
                arguments,
                ..
            } => self.emit(
                "response.function_call_arguments.done",
                json!({"output_index":oi,"item_id":id,"name":name,"arguments":arguments}),
                out,
            )?,
            _ => return Err(bad("ordinary part end on reasoning item")),
        }
        self.sources
            .get_mut(&position.item)
            .unwrap()
            .parts
            .get_mut(&position.part)
            .unwrap()
            .phase = 2;
        if explicit_end && !self.sources[&position.item].explicit {
            self.end_item(position.item, "completed", out)?;
        } else if kind == OutputKind::Reasoning {
            return Err(bad("invalid part end"));
        }
        Ok(())
    }
    fn end_item(
        &mut self,
        key: StreamItem,
        state: &str,
        out: &mut String,
    ) -> Result<(), CodecError> {
        if !matches!(state, "completed" | "incomplete") {
            return Err(bad("nonterminal output item end"));
        }
        let source = self.source(key)?;
        if !source.started || source.parts.values().any(|p| p.phase != 2) {
            return Err(bad("item ended before all parts"));
        }
        let oi = source.index;
        set_status(&mut self.items[oi], state);
        self.emit(
            "response.output_item.done",
            json!({"output_index":oi,"item":self.items[oi]}),
            out,
        )?;
        self.sources.get_mut(&key).unwrap().ended = true;
        Ok(())
    }
    fn close_implicit(
        &mut self,
        key: StreamItem,
        state: &str,
        out: &mut String,
    ) -> Result<(), CodecError> {
        let source = self.source(key)?;
        if source.explicit
            || source
                .parts
                .values()
                .any(|part| part.explicit && part.phase != 2)
        {
            return Err(bad("missing explicit item/part end"));
        }
        let parts: Vec<_> = source
            .parts
            .iter()
            .filter(|(_, p)| p.phase != 2)
            .map(|(part, _)| *part)
            .collect();
        for part in parts {
            self.end_part(StreamPosition { item: key, part }, false, out)?;
        }
        self.end_item(key, state, out)
    }
    fn reasoning(
        &mut self,
        position: StreamPosition,
        delta: &ResponsesReasoningDelta,
        out: &mut String,
    ) -> Result<(), CodecError> {
        use ResponsesReasoningDelta as D;
        if let D::Start { item } = delta {
            validate_reasoning(item, false)?;
            if position.part != 0
                || !item.summary.is_empty()
                || item
                    .status
                    .as_ref()
                    .is_some_and(|s| *s != wire::ItemStatus::InProgress)
                || self.sources.contains_key(&position.item)
                || self.items.iter().any(|old| old.id() == item.id)
            {
                return Err(bad("invalid reasoning start/identity"));
            }
            if let StreamItem::Ordered(index) = position.item
                && self
                    .sources
                    .keys()
                    .any(|key| matches!(key,StreamItem::Ordered(old) if *old>index))
            {
                return Err(bad("reasoning item order regressed"));
            }
            self.reserve(
                std::mem::size_of::<OutputSource>() + 32 + serde_json::to_vec(item)?.len(),
            )?;
            let oi = self.items.len();
            let item = wire::OutputItem::Reasoning(item.clone());
            self.emit(
                "response.output_item.added",
                json!({"output_index":oi,"item":item}),
                out,
            )?;
            self.items.push(item);
            self.sources.insert(
                position.item,
                OutputSource {
                    index: oi,
                    kind: OutputKind::Reasoning,
                    explicit: true,
                    started: true,
                    ended: false,
                    parts: BTreeMap::new(),
                },
            );
            return Ok(());
        }
        let source = self.source(position.item)?;
        if source.kind != OutputKind::Reasoning {
            return Err(bad("reasoning delta changed item"));
        }
        let oi = source.index;
        match delta {
            D::SummaryStart => {
                if source.parts.contains_key(&position.part)
                    || (matches!(position.item, StreamItem::Ordered(_))
                        && position.part as usize != source.parts.len())
                {
                    return Err(bad("invalid summary part index"));
                }
                self.reserve(
                    std::mem::size_of::<OutputPartState>()
                        + 32
                        + std::mem::size_of::<wire::SummaryPart>(),
                )?;
                let wire::OutputItem::Reasoning(item) = &mut self.items[oi] else {
                    unreachable!()
                };
                let part = wire::SummaryPart::SummaryText {
                    text: String::new(),
                };
                let si = item.summary.len();
                item.summary.push(part.clone());
                let fields =
                    json!({"output_index":oi,"item_id":item.id,"summary_index":si,"part":part});
                self.sources.get_mut(&position.item).unwrap().parts.insert(
                    position.part,
                    OutputPartState {
                        index: si,
                        kind: StreamPartKind::Text,
                        explicit: true,
                        phase: 0,
                    },
                );
                self.emit("response.reasoning_summary_part.added", fields, out)
            }
            D::Done { item: final_item } => {
                validate_reasoning(final_item, true)?;
                if position.part != 0 || source.parts.values().any(|p| p.phase != 2) {
                    return Err(bad("unfinished reasoning summary"));
                }
                let wire::OutputItem::Reasoning(item) = &self.items[oi] else {
                    unreachable!()
                };
                if item.id != final_item.id || item.summary != final_item.summary {
                    return Err(bad("reasoning done conflicts with deltas"));
                }
                self.reserve(final_item.encrypted_content.as_ref().map_or(0, String::len))?;
                self.items[oi] = wire::OutputItem::Reasoning(final_item.clone());
                self.emit(
                    "response.output_item.done",
                    json!({"output_index":oi,"item":self.items[oi]}),
                    out,
                )?;
                self.sources.get_mut(&position.item).unwrap().ended = true;
                Ok(())
            }
            _ => {
                let part = source
                    .parts
                    .get(&position.part)
                    .ok_or_else(|| bad("summary outside active part"))?;
                let si = part.index;
                let phase = part.phase;
                if let D::SummaryText { text } = delta {
                    self.reserve(text.len())?;
                }
                let wire::OutputItem::Reasoning(item) = &mut self.items[oi] else {
                    unreachable!()
                };
                let mut fields = json!({"output_index":oi,"item_id":item.id,"summary_index":si});
                let (event, next) = match delta {
                    D::SummaryText { text } if phase == 0 => {
                        item.summary[si].text_mut().push_str(text);
                        fields["delta"] = json!(text);
                        ("response.reasoning_summary_text.delta", 0)
                    }
                    D::SummaryTextDone if phase == 0 => {
                        fields["text"] = json!(item.summary[si].text());
                        ("response.reasoning_summary_text.done", 1)
                    }
                    D::SummaryDone { incomplete } if phase == 1 => {
                        fields["part"] = json!(item.summary[si]);
                        if *incomplete {
                            fields["status"] = json!("incomplete");
                        }
                        ("response.reasoning_summary_part.done", 2)
                    }
                    _ => return Err(bad("invalid reasoning summary lifecycle")),
                };
                self.sources
                    .get_mut(&position.item)
                    .unwrap()
                    .parts
                    .get_mut(&position.part)
                    .unwrap()
                    .phase = next;
                self.emit(event, fields, out)
            }
        }
    }
    fn push_inner(&mut self, event: &ChatEvent) -> Result<String, CodecError> {
        let mut out = String::new();
        match event {
            ChatEvent::Chunk(c) => {
                if c.system_fingerprint.is_some() {
                    return Err(bad("Responses cannot represent system_fingerprint"));
                }
                if self.identity.is_none() {
                    nonempty(&c.id)?;
                    nonempty(&self.public_model)?;
                    self.reserve(c.id.len() + self.public_model.len())?;
                    self.identity = Some(Identity {
                        id: c.id.clone(),
                        created: c.created,
                        model: c.model.clone(),
                    });
                    self.emit(
                        "response.created",
                        json!({"response":self.response(None)?}),
                        &mut out,
                    )?;
                    self.emit(
                        "response.in_progress",
                        json!({"response":self.response(None)?}),
                        &mut out,
                    )?;
                }
                let id = self.identity.as_ref().unwrap();
                if c.id != id.id
                    || c.created != id.created
                    || c.model != id.model
                    || c.choices.len() > 1
                    || c.choices.first().is_some_and(|c| c.index != 0)
                {
                    return Err(bad("Responses requires stable identity and one choice"));
                }
                if let Some(usage) = &c.usage {
                    encode_usage(usage)?;
                    self.usage = Some(usage.clone());
                }
                for choice in &c.choices {
                    if self.finish.is_some()
                        || choice.logprobs.is_some()
                        || choice
                            .delta
                            .role
                            .as_ref()
                            .is_some_and(|r| *r != Role::Assistant)
                    {
                        return Err(bad("invalid canonical stream lifecycle/role/logprobs"));
                    }
                    for event in &choice.delta.events {
                        crate::codec::validate_position(event)?;
                        let position = event.position;
                        match &event.delta {
                            PartDelta::ResponsesItemStart(start) => {
                                if position.part != 0 {
                                    return Err(bad("item start requires part zero"));
                                }
                                let (kind, id) = match start {
                                    ResponsesItemStart::Message { id } => (OutputKind::Message, id),
                                    ResponsesItemStart::FunctionCall { id } => {
                                        (OutputKind::FunctionCall, id)
                                    }
                                };
                                self.new_source(position.item, kind, Some(id), true, &mut out)?;
                            }
                            PartDelta::ResponsesItemEnd(state) => {
                                if position.part != 0 || !self.source(position.item)?.explicit {
                                    return Err(bad("unexpected explicit item end"));
                                }
                                self.end_item(position.item, state.as_str(), &mut out)?;
                            }
                            PartDelta::Start(kind) => {
                                self.start_part(position, *kind, true, &mut out)?
                            }
                            PartDelta::End => self.end_part(position, true, &mut out)?,
                            PartDelta::Text(text) => self.text(position, text, false, &mut out)?,
                            PartDelta::Refusal(text) => {
                                self.text(position, text, true, &mut out)?
                            }
                            PartDelta::ToolCall(call) => self.tool(position, call, &mut out)?,
                            PartDelta::ResponsesReasoning(delta) => {
                                self.reasoning(position, delta, &mut out)?
                            }
                            PartDelta::AnthropicThinking(_) | PartDelta::GeminiText(_) => {
                                return Err(bad("Responses cannot represent vendor thinking"));
                            }
                        }
                    }
                    if let Some(finish) = &choice.finish_reason {
                        let (state, _) = status(finish)?;
                        if state == "completed"
                            && self.sources.values().any(|source| {
                                source.ended && self.items[source.index].status() != "completed"
                            })
                        {
                            return Err(bad("completed response contains incomplete item"));
                        }
                        if self.sources.values().any(|s| {
                            !s.ended
                                && (s.explicit
                                    || s.parts.values().any(|p| p.explicit && p.phase != 2))
                        }) {
                            return Err(bad("finish before explicit output ended"));
                        }
                        self.finish = Some(finish.clone());
                    }
                }
            }
            ChatEvent::Done => {
                let finish = self
                    .finish
                    .clone()
                    .ok_or_else(|| bad("stream ended without finish reason"))?;
                let (state, _) = status(&finish)?;
                let mut pending: Vec<_> = self
                    .sources
                    .iter()
                    .filter(|(_, s)| !s.ended)
                    .map(|(key, s)| (s.index, *key))
                    .collect();
                pending.sort_by_key(|(index, _)| *index);
                for (_, key) in pending {
                    self.close_implicit(key, state, &mut out)?;
                }
                if state == "completed" && self.items.iter().any(|i| i.status() != "completed") {
                    return Err(bad("completed response contains incomplete item"));
                }
                if !self.pending_frames.is_empty() || self.emitted_items != self.items.len() {
                    return Err(bad("stream ended before output item identity"));
                }
                self.emit(
                    &format!("response.{state}"),
                    json!({"response":self.response(Some(&finish))?}),
                    &mut out,
                )?;
                self.terminal = true;
            }
        }
        Ok(out)
    }
}

fn validate_event_fields(v: &Value) -> Result<(), CodecError> {
    let object = v
        .as_object()
        .ok_or_else(|| bad("Responses event must be an object"))?;
    let kind = v["type"]
        .as_str()
        .ok_or_else(|| bad("missing Responses event type"))?;
    let fields: &[&str] = match kind {
        "response.created"
        | "response.in_progress"
        | "response.completed"
        | "response.incomplete" => &["response"],
        "response.output_item.added" | "response.output_item.done" => &["output_index", "item"],
        "response.reasoning_summary_part.added" => {
            &["output_index", "summary_index", "item_id", "part"]
        }
        "response.reasoning_summary_part.done" => {
            &["output_index", "summary_index", "item_id", "part", "status"]
        }
        "response.reasoning_summary_text.delta" => {
            &["output_index", "summary_index", "item_id", "delta"]
        }
        "response.reasoning_summary_text.done" => {
            &["output_index", "summary_index", "item_id", "text"]
        }
        "response.content_part.added" | "response.content_part.done" => {
            &["output_index", "content_index", "item_id", "part"]
        }
        "response.output_text.delta" | "response.refusal.delta" => &[
            "output_index",
            "content_index",
            "item_id",
            "delta",
            "logprobs",
            "obfuscation",
        ],
        "response.output_text.done" => &[
            "output_index",
            "content_index",
            "item_id",
            "text",
            "logprobs",
        ],
        "response.refusal.done" => &["output_index", "content_index", "item_id", "refusal"],
        "response.function_call_arguments.delta" => {
            &["output_index", "item_id", "delta", "obfuscation"]
        }
        "response.function_call_arguments.done" => {
            &["output_index", "item_id", "arguments", "name"]
        }
        _ => return Err(bad("unsupported/failed Responses event type")),
    };
    if object
        .keys()
        .any(|k| !matches!(k.as_str(), "type" | "sequence_number") && !fields.contains(&k.as_str()))
    {
        return Err(bad("field does not belong to Responses event type"));
    }
    Ok(())
}

fn snapshot_deltas(item: &wire::ReasoningItem) -> Vec<(usize, ResponsesReasoningDelta)> {
    let start = wire::ReasoningItem {
        id: item.id.clone(),
        summary: Vec::new(),
        encrypted_content: None,
        status: Some(wire::ItemStatus::InProgress),
        content: item.content.clone(),
    };
    let mut out = vec![(0, ResponsesReasoningDelta::Start { item: start })];
    for (index, part) in item.summary.iter().enumerate() {
        out.extend(
            [
                ResponsesReasoningDelta::SummaryStart,
                ResponsesReasoningDelta::SummaryText {
                    text: part.text().into(),
                },
                ResponsesReasoningDelta::SummaryTextDone,
                ResponsesReasoningDelta::SummaryDone { incomplete: false },
            ]
            .into_iter()
            .map(|delta| (index, delta)),
        );
    }
    out.push((0, ResponsesReasoningDelta::Done { item: item.clone() }));
    out
}
