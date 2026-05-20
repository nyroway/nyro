use std::collections::HashMap;

use uuid::Uuid;

use crate::protocol::ir::AiStreamDelta;
use crate::protocol::ir::usage::Usage;
use crate::protocol::{SseEvent, StreamResponseEncoder};

struct PendingFunctionCall {
    output_index: usize,
    item_id: String,
    call_id: String,
    name: String,
    arguments: String,
}

pub struct ResponsesStreamFormatter {
    resp_id: String,
    msg_id: String,
    model: String,
    accumulated_text: String,
    accumulated_reasoning: String,
    usage: Usage,
    started: bool,
    completed: bool,
    next_output_index: usize,
    reasoning_item_id: Option<String>,
    reasoning_output_index: Option<usize>,
    tool_index_map: HashMap<usize, usize>,
    tool_calls: Vec<PendingFunctionCall>,
}

impl Default for ResponsesStreamFormatter {
    fn default() -> Self {
        Self::new()
    }
}

impl ResponsesStreamFormatter {
    pub fn new() -> Self {
        Self {
            resp_id: format!("resp_{}", Uuid::new_v4().simple()),
            msg_id: format!("msg_{}", Uuid::new_v4().simple()),
            model: String::new(),
            accumulated_text: String::new(),
            accumulated_reasoning: String::new(),
            usage: Usage::default(),
            started: false,
            completed: false,
            next_output_index: 1,
            reasoning_item_id: None,
            reasoning_output_index: None,
            tool_index_map: HashMap::new(),
            tool_calls: Vec::new(),
        }
    }

    fn ensure_started(&mut self, events: &mut Vec<SseEvent>) {
        if self.started {
            return;
        }
        self.started = true;
        events.extend(self.emit_preamble());
    }

    fn emit_preamble(&mut self) -> Vec<SseEvent> {
        let mut events = Vec::with_capacity(4);
        let model = if self.model.is_empty() {
            "unknown".to_string()
        } else {
            self.model.clone()
        };

        let created = serde_json::json!({
            "type": "response.created",
            "response": {
                "id": self.resp_id,
                "object": "response",
                "status": "in_progress",
                "model": model,
                "output": [],
                "output_text": ""
            }
        });
        events.push(SseEvent::new(Some("response.created"), created.to_string()));

        let in_progress = serde_json::json!({
            "type": "response.in_progress",
            "response": {
                "id": self.resp_id,
                "object": "response",
                "status": "in_progress"
            }
        });
        events.push(SseEvent::new(
            Some("response.in_progress"),
            in_progress.to_string(),
        ));

        let item_added = serde_json::json!({
            "type": "response.output_item.added",
            "output_index": 0,
            "item": {
                "type": "message",
                "id": self.msg_id,
                "status": "in_progress",
                "role": "assistant",
                "content": []
            }
        });
        events.push(SseEvent::new(
            Some("response.output_item.added"),
            item_added.to_string(),
        ));

        let part_added = serde_json::json!({
            "type": "response.content_part.added",
            "item_id": self.msg_id,
            "output_index": 0,
            "content_index": 0,
            "part": {
                "type": "output_text",
                "text": "",
                "annotations": []
            }
        });
        events.push(SseEvent::new(
            Some("response.content_part.added"),
            part_added.to_string(),
        ));

        events
    }

    fn emit_completed(&mut self) -> Vec<SseEvent> {
        let mut events = Vec::new();

        if let (Some(item_id), Some(output_index)) =
            (&self.reasoning_item_id, self.reasoning_output_index)
        {
            let reasoning_done = serde_json::json!({
                "type": "response.output_item.done",
                "output_index": output_index,
                "item": {
                    "type": "reasoning",
                    "id": item_id,
                    "summary": [{
                        "type": "summary_text",
                        "text": self.accumulated_reasoning
                    }]
                }
            });
            events.push(SseEvent::new(
                Some("response.output_item.done"),
                reasoning_done.to_string(),
            ));
        }

        for call in &self.tool_calls {
            let tool_done = serde_json::json!({
                "type": "response.output_item.done",
                "output_index": call.output_index,
                "item": {
                    "type": "function_call",
                    "id": call.item_id,
                    "call_id": call.call_id,
                    "name": call.name,
                    "arguments": call.arguments,
                    "status": "completed"
                }
            });
            events.push(SseEvent::new(
                Some("response.output_item.done"),
                tool_done.to_string(),
            ));
        }

        let text_done = serde_json::json!({
            "type": "response.output_text.done",
            "item_id": self.msg_id,
            "output_index": 0,
            "content_index": 0,
            "text": self.accumulated_text
        });
        events.push(SseEvent::new(
            Some("response.output_text.done"),
            text_done.to_string(),
        ));

        let part_done = serde_json::json!({
            "type": "response.content_part.done",
            "item_id": self.msg_id,
            "output_index": 0,
            "content_index": 0,
            "part": {
                "type": "output_text",
                "text": self.accumulated_text,
                "annotations": []
            }
        });
        events.push(SseEvent::new(
            Some("response.content_part.done"),
            part_done.to_string(),
        ));

        let item_done = serde_json::json!({
            "type": "response.output_item.done",
            "output_index": 0,
            "item": {
                "type": "message",
                "id": self.msg_id,
                "status": "completed",
                "role": "assistant",
                "content": [{
                    "type": "output_text",
                    "text": self.accumulated_text,
                    "annotations": []
                }]
            }
        });
        events.push(SseEvent::new(
            Some("response.output_item.done"),
            item_done.to_string(),
        ));

        let mut output: Vec<serde_json::Value> = Vec::new();
        if let Some(item_id) = &self.reasoning_item_id {
            output.push(serde_json::json!({
                "type": "reasoning",
                "id": item_id,
                "summary": [{
                    "type": "summary_text",
                    "text": self.accumulated_reasoning
                }]
            }));
        }
        for call in &self.tool_calls {
            output.push(serde_json::json!({
                "type": "function_call",
                "id": call.item_id,
                "call_id": call.call_id,
                "name": call.name,
                "arguments": call.arguments,
                "status": "completed"
            }));
        }
        output.push(serde_json::json!({
            "type": "message",
            "id": self.msg_id,
            "status": "completed",
            "role": "assistant",
            "content": [{
                "type": "output_text",
                "text": self.accumulated_text,
                "annotations": []
            }]
        }));

        let completed = serde_json::json!({
            "type": "response.completed",
            "response": {
                "id": self.resp_id,
                "object": "response",
                "status": "completed",
                "model": self.model,
                "output": output,
                "output_text": self.accumulated_text,
                "usage": {
                    "input_tokens": self.usage.prompt_tokens,
                    "output_tokens": self.usage.completion_tokens,
                    "total_tokens": self.usage.prompt_tokens + self.usage.completion_tokens
                }
            }
        });
        events.push(SseEvent::new(
            Some("response.completed"),
            completed.to_string(),
        ));

        events
    }
}

impl StreamResponseEncoder for ResponsesStreamFormatter {
    fn format_deltas(&mut self, deltas: &[AiStreamDelta]) -> Vec<SseEvent> {
        let mut events = Vec::new();

        for delta in deltas {
            match delta {
                AiStreamDelta::MessageStart { id, model } => {
                    if !id.is_empty() {
                        self.resp_id = id.clone();
                    }
                    self.model = model.clone();
                    self.ensure_started(&mut events);
                }
                AiStreamDelta::ThinkingDelta(text) => {
                    self.ensure_started(&mut events);
                    if self.reasoning_item_id.is_none() {
                        let item_id = format!("rs_{}", Uuid::new_v4().simple());
                        let output_index = self.next_output_index;
                        self.next_output_index += 1;
                        self.reasoning_item_id = Some(item_id.clone());
                        self.reasoning_output_index = Some(output_index);
                        let added = serde_json::json!({
                            "type": "response.output_item.added",
                            "output_index": output_index,
                            "item": {
                                "type": "reasoning",
                                "id": item_id,
                                "summary": []
                            }
                        });
                        events.push(SseEvent::new(
                            Some("response.output_item.added"),
                            added.to_string(),
                        ));
                    }
                    self.accumulated_reasoning.push_str(text);
                    let ev = serde_json::json!({
                        "type": "response.reasoning_summary_text.delta",
                        "item_id": self.reasoning_item_id,
                        "output_index": self.reasoning_output_index,
                        "summary_index": 0,
                        "delta": text
                    });
                    events.push(SseEvent::new(
                        Some("response.reasoning_summary_text.delta"),
                        ev.to_string(),
                    ));
                }
                AiStreamDelta::ThinkingSignature(_) => {}
                AiStreamDelta::TextDelta(text) => {
                    self.ensure_started(&mut events);
                    self.accumulated_text.push_str(text);
                    let ev = serde_json::json!({
                        "type": "response.output_text.delta",
                        "item_id": self.msg_id,
                        "output_index": 0,
                        "content_index": 0,
                        "delta": text
                    });
                    events.push(SseEvent::new(
                        Some("response.output_text.delta"),
                        ev.to_string(),
                    ));
                }
                AiStreamDelta::ToolCallStart { index, id, name } => {
                    self.ensure_started(&mut events);
                    let output_index = self.next_output_index;
                    self.next_output_index += 1;
                    let item_id = format!("fc_{}", Uuid::new_v4().simple());
                    let call_id = if id.is_empty() {
                        format!("call_{}", Uuid::new_v4().simple())
                    } else {
                        id.clone()
                    };

                    self.tool_index_map.insert(*index, self.tool_calls.len());
                    self.tool_calls.push(PendingFunctionCall {
                        output_index,
                        item_id: item_id.clone(),
                        call_id: call_id.clone(),
                        name: name.clone(),
                        arguments: String::new(),
                    });

                    let added = serde_json::json!({
                        "type": "response.output_item.added",
                        "output_index": output_index,
                        "item": {
                            "type": "function_call",
                            "id": item_id,
                            "call_id": call_id,
                            "name": name,
                            "arguments": "",
                            "status": "in_progress"
                        }
                    });
                    events.push(SseEvent::new(
                        Some("response.output_item.added"),
                        added.to_string(),
                    ));
                }
                AiStreamDelta::ToolCallDelta { index, arguments } => {
                    if let Some(pos) = self.tool_index_map.get(index).copied()
                        && let Some(call) = self.tool_calls.get_mut(pos)
                    {
                        call.arguments.push_str(arguments);
                        let ev = serde_json::json!({
                            "type": "response.function_call_arguments.delta",
                            "item_id": call.item_id,
                            "output_index": call.output_index,
                            "delta": arguments
                        });
                        events.push(SseEvent::new(
                            Some("response.function_call_arguments.delta"),
                            ev.to_string(),
                        ));
                    }
                }
                AiStreamDelta::Usage(u) => {
                    if u.prompt_tokens > 0 {
                        self.usage.prompt_tokens = u.prompt_tokens;
                    }
                    if u.completion_tokens > 0 {
                        self.usage.completion_tokens = u.completion_tokens;
                    }
                    if u.cache_read_tokens.is_some() {
                        self.usage.cache_read_tokens = u.cache_read_tokens;
                    }
                    if u.cache_creation_tokens.is_some() {
                        self.usage.cache_creation_tokens = u.cache_creation_tokens;
                    }
                    if u.server_tool_use.is_some() {
                        self.usage.server_tool_use = u.server_tool_use.clone();
                    }
                }
                AiStreamDelta::Done { .. } => {
                    if !self.completed {
                        self.completed = true;
                        events.extend(self.emit_completed());
                    }
                }
                _ => {}
            }
        }

        events
    }

    fn format_done(&mut self) -> Vec<SseEvent> {
        let mut events = Vec::new();
        if !self.completed {
            self.completed = true;
            events.extend(self.emit_completed());
        }
        events.push(SseEvent::new(None, "[DONE]"));
        events
    }

    fn usage(&self) -> Usage {
        self.usage.clone()
    }
}
