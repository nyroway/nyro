use nyro_llm::{
    codec::openai,
    ir::{MessageItem, Role},
};
use serde_json::json;

#[test]
fn tool_image_ir_keeps_responses_eligibility_without_opening_chat_tool_images() {
    let mut request = openai::decode_chat(json!({"model":"m","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.test/result.png"}}]}]})).unwrap();
    request.messages[0].role = Role::Tool;
    request.messages[0].tool_call_id = Some("call".into());
    assert!(openai::encode_chat(&request).is_err());
    let encoded = openai::responses::encode_chat(&request).unwrap();
    assert_eq!(encoded["input"][0]["call_id"], "call");
    assert_eq!(
        encoded["input"][0]["output"][0]["image_url"],
        "https://example.test/result.png"
    );
    assert!(matches!(
        request.messages[0].items[0],
        MessageItem::Content(_)
    ));
}

#[test]
fn chat_wire_rejects_tool_images_without_reclassifying_them_as_user_input() {
    let wire = json!({"model":"m","messages":[{"role":"tool","tool_call_id":"call","content":[{"type":"image_url","image_url":{"url":"https://example.test/result.png"}}]}]});
    assert!(openai::decode_chat(wire).is_err());
}
