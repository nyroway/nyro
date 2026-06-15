use std::collections::VecDeque;

use crate::protocol::ir::request::{
    AiRequest, ContentBlock, Message, MessageContent, Role, ToolCall,
};

pub fn normalize_request_tool_results(req: &mut AiRequest) {
    let mut pending_calls: VecDeque<(String, String)> = VecDeque::new();
    let mut generated_id_seq: usize = 0;
    let mut normalized_messages: Vec<Message> = Vec::with_capacity(req.messages.len());

    for mut msg in req.messages.drain(..) {
        if msg.role == Role::Assistant {
            if let Some(tool_calls) = &mut msg.tool_calls {
                for tc in tool_calls.iter_mut() {
                    if tc.id.trim().is_empty() {
                        generated_id_seq += 1;
                        tc.id = format!("call_nyro_{generated_id_seq}");
                    }
                    pending_calls.push_back((tc.id.clone(), tc.name.clone()));
                }
            }
            normalized_messages.push(msg);
            continue;
        }

        if msg.role != Role::Tool {
            normalized_messages.push(msg);
            continue;
        }

        let existing_id = msg
            .tool_call_id
            .as_ref()
            .filter(|v| !v.trim().is_empty())
            .cloned();

        let mut resolved_id: Option<String> = None;
        let mut has_linked_pending_call = false;

        if let Some(id) = existing_id.as_ref()
            && let Some(pos) = pending_calls
                .iter()
                .position(|(pending_id, _)| pending_id == id)
        {
            let _ = pending_calls.remove(pos);
            resolved_id = Some(id.clone());
            has_linked_pending_call = true;
        }

        let hinted_value = extract_tool_result_hint(&msg.content);

        if resolved_id.is_none()
            && let Some(hint) = hinted_value.clone()
            && let Some(pos) = pending_calls
                .iter()
                .position(|(pending_id, _)| pending_id == &hint)
            && let Some((call_id, _)) = pending_calls.remove(pos)
        {
            resolved_id = Some(call_id);
            has_linked_pending_call = true;
        }

        if resolved_id.is_none()
            && let Some(hint) = hinted_value.clone()
            && let Some(pos) = pending_calls
                .iter()
                .position(|(_, pending_name)| pending_name.eq_ignore_ascii_case(&hint))
            && let Some((call_id, _)) = pending_calls.remove(pos)
        {
            resolved_id = Some(call_id);
            has_linked_pending_call = true;
        }

        if resolved_id.is_none()
            && let Some((call_id, _name)) = pending_calls.pop_front()
        {
            resolved_id = Some(call_id);
            has_linked_pending_call = true;
        }

        if resolved_id.is_none() {
            resolved_id = existing_id;
        }

        if resolved_id.is_none() {
            generated_id_seq += 1;
            resolved_id = Some(format!("call_nyro_synth_{generated_id_seq}"));
        }

        let final_id = resolved_id.expect("final tool_call_id should always exist");
        if !has_linked_pending_call {
            let synth_name = hinted_value.unwrap_or_else(|| "unknown_tool".to_string());
            normalized_messages.push(Message {
                role: Role::Assistant,
                content: MessageContent::Text(String::new()),
                tool_calls: Some(vec![ToolCall {
                    id: final_id.clone(),
                    name: synth_name,
                    arguments: "{}".to_string(),
                }]),
                tool_call_id: None,
                meta: None,
            });
        }

        msg.tool_call_id = Some(final_id);
        normalized_messages.push(msg);
    }

    req.messages = normalized_messages;
}

fn extract_tool_result_hint(content: &MessageContent) -> Option<String> {
    let MessageContent::Blocks(blocks) = content else {
        return None;
    };
    for block in blocks {
        if let ContentBlock::ToolResult { tool_use_id, .. } = block
            && !tool_use_id.trim().is_empty()
        {
            return Some(tool_use_id.clone());
        }
    }
    None
}

#[cfg(test)]
mod tests {
    use super::*;

    fn make_req(messages: Vec<Message>) -> AiRequest {
        AiRequest::new("test", messages)
    }

    fn assistant_with_tool(tool_id: &str, tool_name: &str) -> Message {
        Message {
            role: Role::Assistant,
            content: MessageContent::Text(String::new()),
            tool_calls: Some(vec![ToolCall {
                id: tool_id.to_string(),
                name: tool_name.to_string(),
                arguments: "{}".to_string(),
            }]),
            tool_call_id: None,
            meta: None,
        }
    }

    fn tool_result_with_id(tool_call_id: &str) -> Message {
        Message {
            role: Role::Tool,
            content: MessageContent::Text("result".to_string()),
            tool_calls: None,
            tool_call_id: Some(tool_call_id.to_string()),
            meta: None,
        }
    }

    fn tool_result_no_id() -> Message {
        Message {
            role: Role::Tool,
            content: MessageContent::Text("result".to_string()),
            tool_calls: None,
            tool_call_id: None,
            meta: None,
        }
    }

    #[test]
    fn test_correlation_by_matching_id() {
        let mut req = make_req(vec![
            assistant_with_tool("call_1", "get_weather"),
            tool_result_with_id("call_1"),
        ]);
        normalize_request_tool_results(&mut req);

        let tool_msg = req.messages.iter().find(|m| m.role == Role::Tool).unwrap();
        assert_eq!(tool_msg.tool_call_id.as_deref(), Some("call_1"));
    }

    #[test]
    fn test_correlation_fifo_when_no_id() {
        let mut req = make_req(vec![
            assistant_with_tool("call_abc", "search"),
            tool_result_no_id(),
        ]);
        normalize_request_tool_results(&mut req);

        let tool_msg = req.messages.iter().find(|m| m.role == Role::Tool).unwrap();
        assert_eq!(
            tool_msg.tool_call_id.as_deref(),
            Some("call_abc"),
            "FIFO fallback should correlate to the single pending call"
        );
    }

    #[test]
    fn test_generated_id_for_empty_tool_call_id() {
        let mut req = make_req(vec![
            Message {
                role: Role::Assistant,
                content: MessageContent::Text(String::new()),
                tool_calls: Some(vec![ToolCall {
                    id: "".to_string(),
                    name: "my_tool".to_string(),
                    arguments: "{}".to_string(),
                }]),
                tool_call_id: None,
                meta: None,
            },
            tool_result_no_id(),
        ]);
        normalize_request_tool_results(&mut req);

        let asst = req
            .messages
            .iter()
            .find(|m| m.role == Role::Assistant)
            .unwrap();
        let tc_id = &asst.tool_calls.as_ref().unwrap()[0].id;
        assert!(
            !tc_id.is_empty(),
            "blank tool_call_id must be replaced with generated id"
        );

        let tool_msg = req.messages.iter().find(|m| m.role == Role::Tool).unwrap();
        assert_eq!(
            tool_msg.tool_call_id.as_deref(),
            Some(tc_id.as_str()),
            "tool result id must match the generated assistant tool_call id"
        );
    }

    #[test]
    fn test_multiple_tool_calls_fifo_order() {
        let mut req = make_req(vec![
            Message {
                role: Role::Assistant,
                content: MessageContent::Text(String::new()),
                tool_calls: Some(vec![
                    ToolCall {
                        id: "call_1".to_string(),
                        name: "tool_a".to_string(),
                        arguments: "{}".to_string(),
                    },
                    ToolCall {
                        id: "call_2".to_string(),
                        name: "tool_b".to_string(),
                        arguments: "{}".to_string(),
                    },
                ]),
                tool_call_id: None,
                meta: None,
            },
            tool_result_no_id(),
            tool_result_no_id(),
        ]);
        normalize_request_tool_results(&mut req);

        let tool_msgs: Vec<_> = req
            .messages
            .iter()
            .filter(|m| m.role == Role::Tool)
            .collect();
        assert_eq!(
            tool_msgs[0].tool_call_id.as_deref(),
            Some("call_1"),
            "first tool result should map to call_1"
        );
        assert_eq!(
            tool_msgs[1].tool_call_id.as_deref(),
            Some("call_2"),
            "second tool result should map to call_2"
        );
    }
}
