use serde_json::Value;
use uuid::Uuid;

use crate::protocol::ResponseEncoder;
use crate::protocol::ir::AiResponse;
use crate::protocol::ir::response::ResponseItem;

pub struct ResponsesResponseFormatter;

impl ResponseEncoder for ResponsesResponseFormatter {
    fn format_response(&self, resp: &AiResponse) -> Value {
        let resp_id = if resp.id.is_empty() {
            format!("resp_{}", Uuid::new_v4().simple())
        } else {
            resp.id.clone()
        };
        let msg_id = format!("msg_{}", Uuid::new_v4().simple());

        let mut output: Vec<Value> = Vec::new();
        let mut output_text = String::new();

        if let Some(items) = &resp.items {
            for item in items {
                match item {
                    ResponseItem::Thinking { text } => {
                        output.push(serde_json::json!({
                            "type": "reasoning",
                            "id": format!("rs_{}", Uuid::new_v4().simple()),
                            "summary": [{
                                "type": "summary_text",
                                "text": text
                            }]
                        }));
                    }
                    ResponseItem::FunctionCall {
                        call_id,
                        name,
                        arguments,
                    } => {
                        output.push(serde_json::json!({
                            "type": "function_call",
                            "id": format!("fc_{}", Uuid::new_v4().simple()),
                            "call_id": call_id,
                            "name": name,
                            "arguments": arguments,
                            "status": "completed"
                        }));
                    }
                    ResponseItem::OutputText { text } => {
                        output_text.push_str(text);
                    }
                    _ => {}
                }
            }
        } else {
            if let Some(reasoning) = &resp.reasoning_content {
                output.push(serde_json::json!({
                    "type": "reasoning",
                    "id": format!("rs_{}", Uuid::new_v4().simple()),
                    "summary": [{
                        "type": "summary_text",
                        "text": reasoning
                    }]
                }));
            }
            for tc in &resp.tool_calls {
                output.push(serde_json::json!({
                    "type": "function_call",
                    "id": format!("fc_{}", Uuid::new_v4().simple()),
                    "call_id": tc.id,
                    "name": tc.name,
                    "arguments": tc.arguments,
                    "status": "completed"
                }));
            }
            output_text.push_str(&resp.content);
        }

        if !output_text.is_empty() {
            output.push(serde_json::json!({
                "type": "message",
                "id": msg_id,
                "status": "completed",
                "role": "assistant",
                "content": [{
                    "type": "output_text",
                    "text": output_text,
                    "annotations": []
                }]
            }));
        }

        serde_json::json!({
            "id": resp_id,
            "object": "response",
            "status": "completed",
            "model": resp.model,
            "output": output,
            "output_text": output_text,
            "usage": {
                "input_tokens": resp.usage.prompt_tokens,
                "output_tokens": resp.usage.completion_tokens,
                "total_tokens": resp.usage.prompt_tokens + resp.usage.completion_tokens
            }
        })
    }
}
