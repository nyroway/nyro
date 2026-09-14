//! Responses repeats full output at termination: retained snapshots share the frame bound.
use super::*;
use nyro_protocol::framing::Event;
use std::collections::BTreeMap;
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
                if self
                    .items
                    .iter()
                    .any(|s| !matches!(s.item, wire::OutputItem::FunctionCall { .. }) && !s.done)
                {
                    return Err(bad(
                        "overlapping Responses message items cannot preserve output order",
                    ));
                }
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
                        if !r.summary.is_empty()
                            || self
                                .items
                                .iter()
                                .any(|s| !matches!(s.item, wire::OutputItem::Reasoning(_)))
                        {
                            return Err(bad("unsupported initial reasoning content/output order"));
                        }
                        out.push(self.started()?.chunk(
                            reasoning_delta(ResponsesReasoningDelta::Start { item: r.clone() }),
                            None,
                            None,
                        ));
                        None
                    }
                    wire::OutputItem::Message { content, .. } => {
                        if !content.is_empty() || self.items.iter().any(|i| i.tool_index.is_some())
                        {
                            return Err(bad("unsupported initial message content/output order"));
                        }
                        None
                    }
                    wire::OutputItem::FunctionCall {
                        call_id,
                        name,
                        arguments,
                        ..
                    } => {
                        if self.items.iter().any(|i|matches!(&i.item,wire::OutputItem::FunctionCall { call_id:id,.. } if id==call_id)) { return Err(bad("duplicate function call identity")); }
                        let index = u32::try_from(
                            self.items.iter().filter(|i| i.tool_index.is_some()).count(),
                        )
                        .map_err(|_| bad("too many tool calls"))?;
                        out.push(self.started()?.chunk(
                            Delta {
                                tool_calls: Some(vec![ToolCallDelta {
                                    gemini: None,
                                    index,
                                    id: Some(call_id.clone()),
                                    r#type: Some(FunctionType::Function),
                                    function: Some(FunctionDelta {
                                        name: Some(name.clone()),
                                        arguments: Some(arguments.clone()),
                                    }),
                                }]),
                                ..Default::default()
                            },
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
                        if index != item.summary.len()
                            || !part.text().is_empty()
                            || state.parts.last().is_some_and(|p| *p != 2)
                        {
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
                out.push(self.started()?.chunk(reasoning_delta(delta), None, None));
            }
            "response.content_part.added" => {
                let Some(wire::StreamPart::Output(part)) = e.part else {
                    return Err(bad("missing/invalid content part"));
                };
                validate_part(&part)?;
                if matches!(part,wire::OutputPart::OutputText { .. }) && self.items.iter().any(|s| matches!(&s.item,wire::OutputItem::Message { content,.. } if content.iter().any(|p| matches!(p,wire::OutputPart::Refusal { .. })))) { return Err(bad("text after refusal cannot be represented")); }
                if !part_text(&part).is_empty() {
                    return Err(bad("initial Responses content part must be empty"));
                }
                self.reserve(serde_json::to_vec(&part)?.len())?;
                let state = self.item_mut(e.output_index, e.item_id.as_deref())?;
                let wire::OutputItem::Message { content, .. } = &mut state.item else {
                    return Err(bad("content on function call"));
                };
                if e.content_index != Some(content.len())
                    || state.parts.last().is_some_and(|phase| *phase != 2)
                {
                    return Err(bad("invalid content part lifecycle/index"));
                }
                content.push(part);
                state.parts.push(0);
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
                        Delta {
                            content: Some(delta),
                            ..Default::default()
                        }
                    }
                    wire::OutputPart::Refusal { refusal }
                        if e.r#type == "response.refusal.delta" =>
                    {
                        refusal.push_str(&delta);
                        Delta {
                            refusal: Some(delta),
                            ..Default::default()
                        }
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
                    Delta {
                        tool_calls: Some(vec![ToolCallDelta {
                            gemini: None,
                            index,
                            id: None,
                            r#type: None,
                            function: Some(FunctionDelta {
                                name: None,
                                arguments: Some(delta),
                            }),
                        }]),
                        ..Default::default()
                    },
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
                        reasoning_delta(ResponsesReasoningDelta::Done { item }),
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
                            wire::OutputItem::Message { content, .. } => content.len(),
                            wire::OutputItem::FunctionCall { .. } => 1,
                        })
                    });
                    let each = std::mem::size_of::<ChatEvent>()
                        + std::mem::size_of::<ChatChunk>()
                        + std::mem::size_of::<StreamChoice>()
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
                    // Some compatible servers send only the terminal snapshot. Emit it once.
                    let mut tool_index = 0;
                    for item in &r.output {
                        match item {
                            wire::OutputItem::Reasoning(item) => {
                                for delta in snapshot_deltas(item) {
                                    out.push(self.started()?.chunk(
                                        reasoning_delta(delta),
                                        None,
                                        None,
                                    ));
                                }
                            }
                            wire::OutputItem::Message { content, .. } => {
                                for part in content {
                                    out.push(self.started()?.chunk(part_delta(part), None, None));
                                }
                            }
                            wire::OutputItem::FunctionCall {
                                call_id,
                                name,
                                arguments,
                                ..
                            } => {
                                out.push(self.started()?.chunk(
                                    Delta {
                                        tool_calls: Some(vec![ToolCallDelta {
                                            gemini: None,
                                            index: tool_index,
                                            id: Some(call_id.clone()),
                                            r#type: Some(FunctionType::Function),
                                            function: Some(FunctionDelta {
                                                name: Some(name.clone()),
                                                arguments: Some(arguments.clone()),
                                            }),
                                        }]),
                                        ..Default::default()
                                    },
                                    None,
                                    None,
                                ));
                                tool_index += 1;
                            }
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
fn part_delta(part: &wire::OutputPart) -> Delta {
    match part {
        wire::OutputPart::OutputText { text, .. } => Delta {
            content: Some(text.clone()),
            ..Default::default()
        },
        wire::OutputPart::Refusal { refusal } => Delta {
            refusal: Some(refusal.clone()),
            ..Default::default()
        },
    }
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

pub struct StreamEncoder {
    public_model: String,
    identity: Option<Identity>,
    items: Vec<wire::OutputItem>,
    tools: BTreeMap<u32, usize>,
    active_part: Option<(usize, usize)>,
    message_done: Option<usize>,
    reasoning_active: Option<(usize, Vec<u8>)>,
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
            tools: BTreeMap::new(),
            active_part: None,
            message_done: None,
            reasoning_active: None,
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
    fn emit(&mut self, kind: &str, mut v: Value, out: &mut String) -> Result<(), CodecError> {
        v["type"] = json!(kind);
        v["sequence_number"] = json!(self.sequence);
        let data = serde_json::to_string(&v)?;
        if data.len() > self.max_bytes {
            return Err(bad("Responses frame exceeds byte limit"));
        }
        self.sequence = self
            .sequence
            .checked_add(1)
            .ok_or_else(|| bad("Responses sequence overflow"))?;
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
            .ok_or_else(|| bad("Responses stream has no identity"))?;
        envelope(
            &id.id,
            id.created,
            &self.public_model,
            json!(self.items),
            finish,
            self.usage.as_ref(),
        )
    }
    fn close_part(&mut self, out: &mut String) -> Result<(), CodecError> {
        if let Some((oi, ci)) = self.active_part.take() {
            let wire::OutputItem::Message { id, content, .. } = &self.items[oi] else {
                unreachable!()
            };
            let part = content[ci].clone();
            let id = id.clone();
            let (kind, key) = match part {
                wire::OutputPart::OutputText { .. } => ("response.output_text.done", "text"),
                wire::OutputPart::Refusal { .. } => ("response.refusal.done", "refusal"),
            };
            let mut v = json!({"item_id":id,"output_index":oi,"content_index":ci});
            v[key] = json!(part_text(&part));
            self.emit(kind, v, out)?;
            self.emit(
                "response.content_part.done",
                json!({"item_id":id,"output_index":oi,"content_index":ci,"part":part}),
                out,
            )?;
        }
        Ok(())
    }
    fn text(&mut self, delta: &str, refusal: bool, out: &mut String) -> Result<(), CodecError> {
        if !self.tools.is_empty() {
            return Err(bad("text after function calls cannot be represented"));
        }
        if !refusal && self.items.iter().any(|i|matches!(i,wire::OutputItem::Message { content,.. } if content.iter().any(|p|matches!(p,wire::OutputPart::Refusal { .. })))) { return Err(bad("text after refusal cannot be represented")); }
        self.reserve(delta.len())?;
        let same=self.active_part.is_some_and(|(oi,ci)| matches!(&self.items[oi],wire::OutputItem::Message { content,.. } if matches!(&content[ci],wire::OutputPart::Refusal { .. })==refusal));
        if !same {
            self.close_part(out)?;
            let oi = if let Some(i) = self
                .items
                .iter()
                .position(|i| matches!(i, wire::OutputItem::Message { .. }))
            {
                i
            } else {
                let i = self.items.len();
                let item = wire::OutputItem::Message {
                    id: format!("msg_{}_{}", self.identity.as_ref().unwrap().id, i),
                    role: "assistant".into(),
                    status: "in_progress".into(),
                    content: Vec::new(),
                };
                self.reserve(serde_json::to_vec(&item)?.len())?;
                self.emit(
                    "response.output_item.added",
                    json!({"output_index":i,"item":item}),
                    out,
                )?;
                self.items.push(item);
                i
            };
            let part = if refusal {
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
            self.reserve(serde_json::to_vec(&part)?.len())?;
            let wire::OutputItem::Message { id, content, .. } = &mut self.items[oi] else {
                unreachable!()
            };
            let ci = content.len();
            let id = id.clone();
            content.push(part.clone());
            self.emit(
                "response.content_part.added",
                json!({"output_index":oi,"content_index":ci,"item_id":id,"part":part}),
                out,
            )?;
            self.active_part = Some((oi, ci));
        }
        let (oi, ci) = self.active_part.unwrap();
        let wire::OutputItem::Message { id, content, .. } = &mut self.items[oi] else {
            unreachable!()
        };
        match &mut content[ci] {
            wire::OutputPart::OutputText { text, .. } => text.push_str(delta),
            wire::OutputPart::Refusal { refusal } => refusal.push_str(delta),
        }
        let id = id.clone();
        self.emit(
            if refusal {
                "response.refusal.delta"
            } else {
                "response.output_text.delta"
            },
            json!({"item_id":id,"output_index":oi,"content_index":ci,"delta":delta}),
            out,
        )
    }
    fn tool(&mut self, call: &ToolCallDelta, out: &mut String) -> Result<(), CodecError> {
        self.close_part(out)?;
        if self.message_done.is_none()
            && let Some(oi) = self
                .items
                .iter()
                .position(|i| matches!(i, wire::OutputItem::Message { .. }))
        {
            set_status(&mut self.items[oi], "completed");
            self.emit(
                "response.output_item.done",
                json!({"output_index":oi,"item":self.items[oi]}),
                out,
            )?;
            self.message_done = Some(oi);
        }
        let oi =
            if let Some(index) = self.tools.get(&call.index) {
                *index
            } else {
                if call.index as usize != self.tools.len() {
                    return Err(bad("noncontiguous canonical tool index"));
                }
                let id = call
                    .id
                    .as_ref()
                    .ok_or_else(|| bad("new function call requires call_id"))?;
                let name = call
                    .function
                    .as_ref()
                    .and_then(|f| f.name.as_ref())
                    .ok_or_else(|| bad("new function call requires name"))?;
                nonempty(id)?;
                nonempty(name)?;
                if self.items.iter().any(
                    |i| matches!(i,wire::OutputItem::FunctionCall { call_id,.. } if call_id==id),
                ) {
                    return Err(bad("duplicate function call_id"));
                }
                let oi = self.items.len();
                let item = wire::OutputItem::FunctionCall {
                    id: format!("fc_{}_{}", self.identity.as_ref().unwrap().id, oi),
                    call_id: id.clone(),
                    name: name.clone(),
                    arguments: String::new(),
                    status: "in_progress".into(),
                };
                self.reserve(serde_json::to_vec(&item)?.len())?;
                self.emit(
                    "response.output_item.added",
                    json!({"output_index":oi,"item":item}),
                    out,
                )?;
                self.items.push(item);
                self.tools.insert(call.index, oi);
                oi
            };
        if let Some(arguments) = call.function.as_ref().and_then(|f| f.arguments.as_ref()) {
            self.reserve(arguments.len())?;
        }
        let wire::OutputItem::FunctionCall {
            id,
            call_id,
            name,
            arguments,
            ..
        } = &mut self.items[oi]
        else {
            unreachable!()
        };
        if call.id.as_ref().is_some_and(|s| s != call_id)
            || call
                .function
                .as_ref()
                .and_then(|f| f.name.as_ref())
                .is_some_and(|s| s != name)
        {
            return Err(bad("canonical function identity changed"));
        }
        let id = id.clone();
        if let Some(delta) = call.function.as_ref().and_then(|f| f.arguments.as_ref()) {
            arguments.push_str(delta);
            self.emit(
                "response.function_call_arguments.delta",
                json!({"output_index":oi,"item_id":id,"delta":delta}),
                out,
            )?;
        }
        Ok(())
    }
    fn reasoning(
        &mut self,
        delta: &ResponsesReasoningDelta,
        out: &mut String,
    ) -> Result<(), CodecError> {
        use ResponsesReasoningDelta as D;
        if let D::Start { item } = delta {
            validate_reasoning(item, false)?;
            if self.reasoning_active.is_some()
                || !item.summary.is_empty()
                || item
                    .status
                    .as_ref()
                    .is_some_and(|s| *s != wire::ItemStatus::InProgress)
                || self
                    .items
                    .iter()
                    .any(|i| !matches!(i, wire::OutputItem::Reasoning(_)) || i.id() == item.id)
            {
                return Err(bad("invalid reasoning start/order/identity"));
            }
            let oi = self.items.len();
            let item = wire::OutputItem::Reasoning(item.clone());
            self.reserve(serde_json::to_vec(&item)?.len())?;
            self.emit(
                "response.output_item.added",
                json!({"output_index":oi,"item":item}),
                out,
            )?;
            self.items.push(item);
            self.reasoning_active = Some((oi, Vec::new()));
            return Ok(());
        }
        let n = match delta {
            D::SummaryStart => std::mem::size_of::<wire::SummaryPart>() + 1,
            D::SummaryText { text } => text.len(),
            D::Done { item } => item.encrypted_content.as_ref().map_or(0, String::len),
            _ => 0,
        };
        self.reserve(n)?;
        let (oi, phases) = self
            .reasoning_active
            .as_mut()
            .ok_or_else(|| bad("reasoning delta outside active item"))?;
        let oi = *oi;
        let wire::OutputItem::Reasoning(item) = &mut self.items[oi] else {
            unreachable!()
        };
        let si = item.summary.len().saturating_sub(1);
        let mut fields = json!({"output_index":oi,"item_id":item.id,"summary_index":si});
        let kind = match delta {
            D::SummaryStart => {
                if phases.last().is_some_and(|p| *p != 2) {
                    return Err(bad("overlapping summary parts"));
                }
                fields["summary_index"] = json!(item.summary.len());
                let part = wire::SummaryPart::SummaryText {
                    text: String::new(),
                };
                fields["part"] = json!(part);
                item.summary.push(part);
                phases.push(0);
                "response.reasoning_summary_part.added"
            }
            D::SummaryText { text } => {
                if phases.last() != Some(&0) {
                    return Err(bad("summary text outside active part"));
                }
                item.summary[si].text_mut().push_str(text);
                fields["delta"] = json!(text);
                "response.reasoning_summary_text.delta"
            }
            D::SummaryTextDone => {
                if phases.last() != Some(&0) {
                    return Err(bad("duplicate/missing summary text done"));
                }
                phases[si] = 1;
                fields["text"] = json!(item.summary[si].text());
                "response.reasoning_summary_text.done"
            }
            D::SummaryDone { incomplete } => {
                if phases.last() != Some(&1) {
                    return Err(bad("duplicate/missing summary part done"));
                }
                phases[si] = 2;
                fields["part"] = json!(item.summary[si]);
                if *incomplete {
                    fields["status"] = json!("incomplete");
                }
                "response.reasoning_summary_part.done"
            }
            D::Done { item: final_item } => {
                validate_reasoning(final_item, true)?;
                if phases.iter().any(|p| *p != 2)
                    || item.id != final_item.id
                    || item.summary != final_item.summary
                {
                    return Err(bad("reasoning done conflicts with deltas/lifecycle"));
                }
                *item = final_item.clone();
                fields = json!({"output_index":oi,"item":self.items[oi]});
                self.reasoning_active = None;
                "response.output_item.done"
            }
            D::Start { .. } => unreachable!(),
        };
        self.emit(kind, fields, out)
    }
    fn push_inner(&mut self, event: &ChatEvent) -> Result<String, CodecError> {
        let mut out = String::new();
        match event {
            ChatEvent::Chunk(c) => {
                if c.choices.iter().any(|c| {
                    c.delta.anthropic_thinking.is_some()
                        || c.delta.gemini_text.is_some()
                        || c.delta
                            .tool_calls
                            .iter()
                            .flatten()
                            .any(|t| t.gemini.is_some())
                }) {
                    return Err(bad("Responses cannot represent vendor thinking"));
                }
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
                        return Err(bad(
                            "invalid canonical Responses stream lifecycle/role/logprobs",
                        ));
                    }
                    if let Some(delta) = &choice.delta.responses_reasoning {
                        if choice.delta.content.is_some()
                            || choice.delta.refusal.is_some()
                            || choice.delta.tool_calls.is_some()
                            || choice.finish_reason.is_some()
                        {
                            return Err(bad("mixed reasoning and ordinary delta"));
                        }
                        self.reasoning(delta, &mut out)?;
                    }
                    if self.reasoning_active.is_some()
                        && (choice.delta.content.is_some()
                            || choice.delta.refusal.is_some()
                            || choice.delta.tool_calls.is_some()
                            || choice.finish_reason.is_some())
                    {
                        return Err(bad("ordinary content/finish inside reasoning item"));
                    }
                    if let Some(text) = &choice.delta.content {
                        self.text(text, false, &mut out)?;
                    }
                    if let Some(refusal) = &choice.delta.refusal {
                        self.text(refusal, true, &mut out)?;
                    }
                    for call in choice.delta.tool_calls.iter().flatten() {
                        self.tool(call, &mut out)?;
                    }
                    if let Some(finish) = &choice.finish_reason {
                        status(finish)?;
                        self.finish = Some(finish.clone());
                    }
                }
            }
            ChatEvent::Done => {
                let finish = self
                    .finish
                    .clone()
                    .ok_or_else(|| bad("canonical stream ended without finish reason"))?;
                if self.reasoning_active.is_some() {
                    return Err(bad("unfinished reasoning item"));
                }
                self.close_part(&mut out)?;
                let (state, _) = status(&finish)?;
                for oi in 0..self.items.len() {
                    if self.message_done == Some(oi)
                        || matches!(self.items[oi], wire::OutputItem::Reasoning(_))
                    {
                        continue;
                    }
                    if let wire::OutputItem::FunctionCall {
                        id,
                        name,
                        arguments,
                        ..
                    } = &self.items[oi]
                    {
                        let v = json!({"output_index":oi,"item_id":id,"name":name,"arguments":arguments});
                        self.emit("response.function_call_arguments.done", v, &mut out)?;
                    }
                    set_status(&mut self.items[oi], state);
                    self.emit(
                        "response.output_item.done",
                        json!({"output_index":oi,"item":self.items[oi]}),
                        &mut out,
                    )?;
                }
                validate_items(&self.items, true)?;
                if state == "completed" && self.items.iter().any(|i| i.status() != "completed") {
                    return Err(bad("completed response contains incomplete item"));
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

fn reasoning_delta(delta: ResponsesReasoningDelta) -> Delta {
    Delta {
        responses_reasoning: Some(delta),
        ..Default::default()
    }
}
fn snapshot_deltas(item: &wire::ReasoningItem) -> Vec<ResponsesReasoningDelta> {
    let start = wire::ReasoningItem {
        id: item.id.clone(),
        summary: Vec::new(),
        encrypted_content: None,
        status: Some(wire::ItemStatus::InProgress),
        content: item.content.clone(),
    };
    let mut out = vec![ResponsesReasoningDelta::Start { item: start }];
    for part in &item.summary {
        out.extend([
            ResponsesReasoningDelta::SummaryStart,
            ResponsesReasoningDelta::SummaryText {
                text: part.text().into(),
            },
            ResponsesReasoningDelta::SummaryTextDone,
            ResponsesReasoningDelta::SummaryDone { incomplete: false },
        ]);
    }
    out.push(ResponsesReasoningDelta::Done { item: item.clone() });
    out
}
