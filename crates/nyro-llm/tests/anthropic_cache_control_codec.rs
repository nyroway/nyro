use nyro_llm::codec::{anthropic, gemini, openai};
use serde_json::{Value, json};

fn request() -> Value {
    json!({"model":"public","max_tokens":32,
        "system":[{"type":"text","text":"first"},{"type":"text","text":"second"}],
        "tools":[{"name":"lookup","input_schema":{"type":"object"}}],
        "messages":[
            {"role":"user","content":[{"type":"text","text":"before"},{"type":"image","source":{"type":"url","url":"https://example.com/image.png"}},{"type":"text","text":"after"}]},
            {"role":"assistant","content":[{"type":"text","text":"history"},{"type":"tool_use","id":"call_1","name":"lookup","input":{"query":"x"}}]},
            {"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":[{"type":"text","text":"result one"},{"type":"text","text":"result two"}]}]}]})
}
const LOCATIONS: [&str; 9] = [
    "",
    "/system/0",
    "/tools/0",
    "/messages/0/content/0",
    "/messages/0/content/1",
    "/messages/1/content/0",
    "/messages/1/content/1",
    "/messages/2/content/0",
    "/messages/2/content/0/content/0",
];

#[test]
fn every_supported_cache_location_keeps_position_and_ttl() {
    for location in LOCATIONS {
        for control in [
            json!({"type":"ephemeral"}),
            json!({"type":"ephemeral","ttl":"5m"}),
            json!({"type":"ephemeral","ttl":"1h"}),
        ] {
            let mut body = request();
            body.pointer_mut(location).unwrap()["cache_control"] = control;
            let r = anthropic::decode_chat(body.clone()).unwrap();
            assert_eq!(anthropic::encode_chat(&r).unwrap(), body, "{location}");
            assert!(openai::encode_chat(&r).is_err(), "{location}");
            assert!(openai::responses::encode_chat(&r).is_err(), "{location}");
            assert!(gemini::encode_chat(&r).is_err(), "{location}");
        }
    }
}

#[test]
fn null_controls_normalize_away_but_malformed_controls_fail() {
    for location in LOCATIONS {
        for control in [
            Value::Null,
            json!({}),
            json!({"type":"persistent"}),
            json!({"type":"ephemeral","ttl":"30m"}),
            json!({"type":"ephemeral","ttl":null}),
            json!({"type":"ephemeral","unexpected":true}),
        ] {
            let mut body = request();
            body.pointer_mut(location).unwrap()["cache_control"] = control.clone();
            let decoded = anthropic::decode_chat(body);
            if control.is_null() {
                assert_eq!(
                    anthropic::encode_chat(&decoded.unwrap()).unwrap(),
                    request()
                );
            } else {
                assert!(decoded.is_err(), "{location}: {control}");
            }
        }
    }
}

#[test]
fn mixed_ttl_order_and_automatic_control_are_preserved_without_insertion() {
    let mut body = request();
    body["cache_control"] = json!({"type":"ephemeral"});
    body["tools"][0]["cache_control"] = json!({"type":"ephemeral","ttl":"1h"});
    body["system"][0]["cache_control"] = json!({"type":"ephemeral","ttl":"1h"});
    body["messages"][0]["content"][1]["cache_control"] = json!({"type":"ephemeral","ttl":"5m"});
    let r = anthropic::decode_chat(body.clone()).unwrap();
    assert_eq!(anthropic::encode_chat(&r).unwrap(), body);
}

#[test]
fn generated_json_and_stream_blocks_reject_input_cache_controls() {
    use nyro_protocol::framing::Event;
    for control in [Value::Null, json!({"type":"ephemeral"})] {
        for mut content in [
            json!({"type":"text","text":""}),
            json!({"type":"tool_use","id":"call_1","name":"lookup","input":{}}),
        ] {
            content["cache_control"] = control.clone();
            let response = json!({"id":"r","type":"message","role":"assistant","model":"m","content":[content],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}});
            assert!(anthropic::decode_chat_response(response).is_err());
            let mut decoder = anthropic::StreamDecoder::new();
            let start = json!({"type":"message_start","message":{"id":"r","type":"message","role":"assistant","model":"m","content":[],"stop_reason":null,"usage":{"input_tokens":1,"output_tokens":0}}});
            decoder
                .push(&Event {
                    event: Some("message_start".into()),
                    data: start.to_string(),
                })
                .unwrap();
            let block = json!({"type":"content_block_start","index":0,"content_block":content});
            assert!(
                decoder
                    .push(&Event {
                        event: Some("content_block_start".into()),
                        data: block.to_string()
                    })
                    .is_err()
            );
        }
    }
}

#[test]
fn direct_ir_output_cannot_emit_anthropic_input_controls() {
    for location in ["/messages/1/content/0", "/messages/1/content/1"] {
        let mut body = request();
        body.pointer_mut(location).unwrap()["cache_control"] = json!({"type":"ephemeral"});
        let input = anthropic::decode_chat(body).unwrap();
        let mut response = openai::decode_chat_response(json!({"id":"r","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"answer"},"finish_reason":"stop"}]})).unwrap();
        response.choices[0].message.content = input.messages[2].content.clone();
        response.choices[0].message.tool_calls = input.messages[2].tool_calls.clone();
        for encoded in [
            openai::encode_chat_response(&response),
            openai::responses::encode_chat_response(&response),
            anthropic::encode_chat_response(&response),
            gemini::encode_chat_response(&response),
        ] {
            assert!(encoded.is_err(), "{location}");
        }
    }
}

#[test]
fn parallel_tool_cache_controls_keep_outer_and_inner_positions() {
    let mut body = request();
    body["messages"] = json!([
        {"role":"user","content":[{"type":"text","text":"lookup"}]},
        {"role":"assistant","content":[
            {"type":"tool_use","id":"a","name":"lookup","input":{"id":1},"cache_control":{"type":"ephemeral","ttl":"1h"}},
            {"type":"tool_use","id":"b","name":"lookup","input":{"id":2},"cache_control":{"type":"ephemeral","ttl":"1h"}}]},
        {"role":"user","content":[
            {"type":"tool_result","tool_use_id":"b","content":[{"type":"text","text":"B"}],"cache_control":{"type":"ephemeral","ttl":"5m"}},
            {"type":"tool_result","tool_use_id":"a","content":[{"type":"text","text":"A","cache_control":{"type":"ephemeral"}}]},
            {"type":"text","text":"continue"}]}
    ]);
    let r = anthropic::decode_chat(body.clone()).unwrap();
    assert_eq!(anthropic::encode_chat(&r).unwrap(), body);
}
