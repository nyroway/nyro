//! Explicit source parts wait only behind unfinished earlier parts.
use super::*;
use std::collections::VecDeque;

struct Part {
    position: StreamPosition,
    kind: StreamPartKind,
    call: PendingCall,
    tool_index: Option<u32>,
    buffered: Vec<w::Part>,
    buffered_bytes: usize,
    bytes: usize,
    complete: bool,
    has_payload: bool,
    signed: bool,
}
struct Container {
    tool: bool,
    last_part: Option<u32>,
}

pub(super) struct Encoder {
    parts: VecDeque<Part>,
    containers: BTreeMap<u32, Container>,
    last_container: Option<u32>,
    last_part: Option<StreamPosition>,
    next_tool: u32,
    active: bool,
    implicit: bool,
    bytes: usize,
    limit: usize,
}
impl Encoder {
    pub(super) fn new(limit: usize) -> Self {
        Self {
            parts: VecDeque::new(),
            containers: BTreeMap::new(),
            last_container: None,
            last_part: None,
            next_tool: 0,
            active: false,
            implicit: false,
            bytes: 0,
            limit,
        }
    }
    pub(super) fn finish(&self) -> Result<(), CodecError> {
        if !self.parts.is_empty() || !self.containers.is_empty() {
            return Err(bad("unfinished Gemini source part or container"));
        }
        Ok(())
    }
    fn reserve(&mut self, n: usize) -> Result<(), CodecError> {
        self.bytes = self
            .bytes
            .checked_add(n)
            .filter(|n| *n <= self.limit)
            .ok_or_else(|| bad("Gemini ordered output exceeds limit"))?;
        Ok(())
    }
    pub(super) fn push(
        &mut self,
        event: &PositionedDelta,
        out: &mut Vec<w::Part>,
    ) -> Result<bool, CodecError> {
        let StreamItem::Ordered(item) = event.position.item else {
            if self.active {
                return Err(bad("mixed ordered and Chat stream positions"));
            }
            self.implicit = true;
            return Ok(false);
        };
        let position = event.position;
        if self.implicit
            && matches!(
                event.delta,
                PartDelta::Start(_) | PartDelta::ResponsesItemStart(_)
            )
        {
            return Err(bad("explicit parts after an implicit stream"));
        }
        match &event.delta {
            PartDelta::ResponsesItemStart(start) => {
                let (id, tool) = match start {
                    ResponsesItemStart::Message { id } => (id, false),
                    ResponsesItemStart::FunctionCall { id } => (id, true),
                };
                if id.trim().is_empty() || self.last_container.is_some_and(|last| item <= last) {
                    return Err(bad("invalid or reused Responses container"));
                }
                self.reserve(std::mem::size_of::<(u32, Container)>() + 32)?;
                self.containers.insert(
                    item,
                    Container {
                        tool,
                        last_part: None,
                    },
                );
                self.last_container = Some(item);
                self.active = true;
            }
            PartDelta::ResponsesItemEnd(status) => {
                if *status == nyro_protocol::openai::responses::ItemStatus::InProgress
                    || self
                        .parts
                        .iter()
                        .any(|part| part.position.item == position.item && !part.complete)
                    || self.containers.remove(&item).is_none()
                {
                    return Err(bad("unfinished or unknown Responses container"));
                }
                self.bytes -= std::mem::size_of::<(u32, Container)>() + 32;
                self.drain(out)?;
            }
            PartDelta::Start(kind) => {
                if *kind == StreamPartKind::Refusal
                    || (self.last_container.is_none()
                        && self.last_part.is_some_and(|last| position <= last))
                {
                    return Err(bad("unsupported or reused Gemini source part"));
                }
                if self.last_container.is_some()
                    && self.containers.get(&item).is_none_or(|container| {
                        container.tool != (*kind == StreamPartKind::ToolCall)
                            || container
                                .last_part
                                .is_some_and(|last| position.part <= last)
                    })
                {
                    return Err(bad("source part does not belong to its container"));
                }
                let bytes = std::mem::size_of::<Part>();
                self.reserve(bytes)?;
                let index = if self.last_container.is_some() {
                    self.parts
                        .iter()
                        .position(|part| part.position > position)
                        .unwrap_or(self.parts.len())
                } else {
                    self.parts.len()
                };
                self.parts.insert(
                    index,
                    Part {
                        position,
                        kind: *kind,
                        call: PendingCall::default(),
                        tool_index: None,
                        buffered: vec![],
                        buffered_bytes: 0,
                        bytes,
                        complete: false,
                        has_payload: false,
                        signed: false,
                    },
                );
                if let Some(container) = self.containers.get_mut(&item) {
                    container.last_part = Some(position.part);
                }
                self.last_part = Some(position);
                self.active = true;
            }
            _ if !self.active => {
                self.implicit = true;
                return Ok(false);
            }
            payload => {
                let index = self
                    .parts
                    .iter()
                    .position(|part| part.position == position && !part.complete)
                    .ok_or_else(|| bad("delta outside its source part"))?;
                let text_bytes = match payload {
                    PartDelta::Text(text) => Some(text.len()),
                    PartDelta::GeminiText(text) => Some(
                        text.text
                            .as_ref()
                            .map_or(0, String::len)
                            .saturating_add(text.thought_signature.as_ref().map_or(0, String::len)),
                    ),
                    _ => None,
                };
                if let Some(bytes) =
                    text_bytes.map(|bytes| bytes.saturating_add(std::mem::size_of::<w::Part>()))
                    && (bytes > self.limit
                        || (self.blocked(index) && self.bytes.saturating_add(bytes) > self.limit))
                {
                    return Err(bad("Gemini ordered text exceeds limit"));
                }
                match payload {
                    PartDelta::Text(text) => self.text(
                        index,
                        w::Part {
                            text: Some(text.clone()),
                            ..Default::default()
                        },
                        false,
                        out,
                    )?,
                    PartDelta::GeminiText(text) => {
                        self.text(index, thinking::wire_text(text)?, true, out)?
                    }
                    PartDelta::ToolCall(delta) => {
                        if self.parts[index].kind != StreamPartKind::ToolCall {
                            return Err(bad("function delta in a text part"));
                        }
                        match self.parts[index].tool_index {
                            Some(index) if index != delta.index => {
                                return Err(bad("tool index changed within source part"));
                            }
                            None => {
                                if delta.index != self.next_tool {
                                    return Err(bad(
                                        "tool index does not match source start order",
                                    ));
                                }
                                self.next_tool = self
                                    .next_tool
                                    .checked_add(1)
                                    .ok_or_else(|| bad("tool index overflow"))?;
                                self.parts[index].tool_index = Some(delta.index);
                            }
                            _ => {}
                        }
                        let added = delta
                            .id
                            .as_ref()
                            .map_or(0, String::len)
                            .saturating_add(
                                delta
                                    .gemini
                                    .as_ref()
                                    .and_then(|m| m.thought_signature.as_ref())
                                    .map_or(0, String::len),
                            )
                            .saturating_add(delta.function.as_ref().map_or(0, |f| {
                                f.name
                                    .as_ref()
                                    .map_or(0, String::len)
                                    .saturating_add(f.arguments.as_ref().map_or(0, String::len))
                            }));
                        self.reserve(added)?;
                        let part = &mut self.parts[index];
                        part.bytes += added;
                        part.has_payload = true;
                        let call = &mut part.call;
                        if let Some(meta) = &delta.gemini {
                            if call.gemini.is_some() {
                                return Err(bad("duplicate Gemini call metadata"));
                            }
                            call.gemini = Some(meta.clone());
                        }
                        if let Some(id) = &delta.id {
                            if !call.id.is_empty() {
                                return Err(bad("duplicate tool id"));
                            }
                            call.id = id.clone();
                        }
                        if let Some(function) = &delta.function {
                            if let Some(name) = &function.name {
                                if !call.name.is_empty() {
                                    return Err(bad("duplicate tool name"));
                                }
                                call.name = name.clone();
                            }
                            if let Some(args) = &function.arguments {
                                call.args.push_str(args);
                            }
                        }
                    }
                    PartDelta::End => {
                        if self.parts[index].kind == StreamPartKind::ToolCall {
                            let call = &self.parts[index].call;
                            if call.id.is_empty()
                                || call.name.is_empty()
                                || !serde_json::from_str::<Value>(&call.args)?.is_object()
                            {
                                return Err(bad("incomplete function source part"));
                            }
                        } else if !self.parts[index].has_payload {
                            self.text(
                                index,
                                w::Part {
                                    text: Some(String::new()),
                                    ..Default::default()
                                },
                                false,
                                out,
                            )?;
                        }
                        self.parts[index].complete = true;
                    }
                    _ => return Err(bad("unsupported Gemini source part payload")),
                }
                self.drain(out)?;
            }
        }
        Ok(true)
    }
    fn text(
        &mut self,
        index: usize,
        wire: w::Part,
        signed: bool,
        out: &mut Vec<w::Part>,
    ) -> Result<(), CodecError> {
        let part = &self.parts[index];
        if part.kind != StreamPartKind::Text || part.signed || (signed && part.has_payload) {
            return Err(bad("text/signature does not belong to this source part"));
        }
        let bytes = std::mem::size_of::<w::Part>()
            + wire.text.as_ref().map_or(0, String::len)
            + wire.thought_signature.as_ref().map_or(0, String::len);
        if bytes > self.limit {
            return Err(bad("Gemini text part exceeds limit"));
        }
        if !self.blocked(index) {
            out.push(wire);
        } else {
            self.reserve(bytes)?;
            self.parts[index].bytes += bytes;
            self.parts[index].buffered_bytes += bytes;
            self.parts[index].buffered.push(wire);
        }
        self.parts[index].has_payload = true;
        self.parts[index].signed = signed;
        Ok(())
    }
    fn drain(&mut self, out: &mut Vec<w::Part>) -> Result<(), CodecError> {
        while !self.parts.is_empty() && !self.blocked(0) {
            let part = self.parts.front_mut().unwrap();
            if part.kind == StreamPartKind::Text {
                out.append(&mut part.buffered);
                self.bytes -= part.buffered_bytes;
                part.bytes -= part.buffered_bytes;
                part.buffered_bytes = 0;
            }
            if !part.complete {
                break;
            }
            let part = self.parts.pop_front().unwrap();
            self.bytes -= part.bytes;
            if part.kind == StreamPartKind::ToolCall {
                let call = part.call;
                out.push(thinking::wire_call(
                    &call.id,
                    &FunctionCall {
                        name: call.name,
                        arguments: call.args,
                    },
                    call.gemini.as_ref(),
                )?);
            }
        }
        Ok(())
    }
    fn blocked(&self, index: usize) -> bool {
        index != 0
            || self.containers.first_key_value().is_some_and(|(item, _)| {
                StreamItem::Ordered(*item) < self.parts[index].position.item
            })
    }
}
