use nyro_llm::codec::{anthropic, gemini, openai};
use serde_json::{Value, json};

fn text(s: &str) -> Value {
    json!({"type":"text","text":s,"prompt_cache_breakpoint":{"mode":"explicit"}})
}

#[test]
fn breakpoints_preserve_text_image_and_tool_result_positions_between_openai_apis() {
    let body = json!({"model":"public","max_tokens":32,
        "prompt_cache_options":{"mode":"explicit","ttl":"30m"},
        "messages":[
            {"role":"system","content":[text("system")]},
            {"role":"developer","content":[text("developer")]},
            {"role":"user","content":[text("before"),
                {"type":"image_url","image_url":{"url":"https://example.com/image.png","detail":"high"},"prompt_cache_breakpoint":{"mode":"explicit"}},
                {"type":"text","text":"after"}]},
            {"role":"assistant","content":[text("history"),{"type":"text","text":"history tail"}],"tool_calls":[{"type":"function","id":"call_1","function":{"name":"lookup","arguments":"{}"}}]},
            {"role":"tool","tool_call_id":"call_1","content":[text("result"),{"type":"text","text":"tail"}]}]});
    let r = openai::decode_chat(body.clone()).unwrap();
    assert_eq!(openai::encode_chat(&r).unwrap(), body);
    let responses = openai::responses::encode_chat(&r).unwrap();
    assert_eq!(
        responses["input"][2]["content"][1],
        json!({"type":"input_image","image_url":"https://example.com/image.png","detail":"high","prompt_cache_breakpoint":{"mode":"explicit"}})
    );
    assert_eq!(responses["input"][3]["content"][0]["type"], "input_text");
    assert_eq!(responses["input"][3]["content"][1]["type"], "input_text");
    assert_eq!(responses["input"][5]["call_id"], "call_1");
    assert_eq!(
        responses["input"][5]["output"][0]["prompt_cache_breakpoint"],
        json!({"mode":"explicit"})
    );
    let roundtrip = openai::responses::decode_chat(responses.clone()).unwrap();
    assert_eq!(
        openai::responses::encode_chat(&roundtrip).unwrap(),
        responses
    );
    let mut back = openai::encode_chat(&roundtrip).unwrap();
    // Existing Responses normalization expresses the token limit as completion tokens.
    back.as_object_mut().unwrap().remove("store");
    back.as_object_mut()
        .unwrap()
        .remove("max_completion_tokens");
    back["max_tokens"] = json!(32);
    assert_eq!(back, body);
}

#[test]
fn marker_alone_filters_other_vendors_in_every_supported_role() {
    for role in ["system", "developer", "user", "assistant", "tool"] {
        let mut message = json!({"role":role,"content":[text("prefix")]});
        if role == "tool" {
            message["tool_call_id"] = json!("call_1");
        }
        let r = openai::decode_chat(json!({"model":"public","max_tokens":32,"messages":[message]}))
            .unwrap();
        assert!(anthropic::encode_chat(&r).is_err(), "{role}");
        assert!(gemini::encode_chat(&r).is_err(), "{role}");
        for encoded in [openai::encode_chat(&r), openai::responses::encode_chat(&r)] {
            assert!(encoded.unwrap().get("prompt_cache_options").is_none());
        }
    }
    let r = openai::decode_chat(json!({"model":"public","max_tokens":32,"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,YQ=="},"prompt_cache_breakpoint":{"mode":"explicit"}}]}]})).unwrap();
    assert!(anthropic::encode_chat(&r).is_err());
    assert!(gemini::encode_chat(&r).is_err());
}

#[test]
fn malformed_breakpoints_and_unsupported_locations_are_rejected() {
    for marker in [
        Value::Null,
        json!({}),
        json!({"mode":"implicit"}),
        json!({"mode":null}),
        json!({"mode":"explicit","ttl":"30m"}),
        json!(true),
    ] {
        for responses in [false, true] {
            let mut part = text("prefix");
            part["prompt_cache_breakpoint"] = marker.clone();
            let decoded = if responses {
                part["type"] = json!("input_text");
                openai::responses::decode_chat(
                    json!({"model":"m","input":[{"role":"user","content":[part]}]}),
                )
            } else {
                openai::decode_chat(
                    json!({"model":"m","messages":[{"role":"user","content":[part]}]}),
                )
            };
            assert!(decoded.is_err(), "{responses}: {marker}");
        }
    }
    for part in [
        json!({"type":"refusal","refusal":"no"}),
        json!({"type":"input_audio","input_audio":{"data":"YQ==","format":"wav"}}),
    ] {
        let mut part = part;
        part["prompt_cache_breakpoint"] = json!({"mode":"explicit"});
        assert!(
            openai::decode_chat(
                json!({"model":"m","messages":[{"role":"assistant","content":[part]}]})
            )
            .is_err()
        );
    }
    assert!(openai::responses::decode_chat(json!({"model":"m","input":[{"role":"assistant","content":[{"type":"output_text","text":"history","prompt_cache_breakpoint":{"mode":"explicit"}}]}]})).is_err());
}

#[test]
fn input_markers_cannot_leak_into_generated_output() {
    let plain = json!({"id":"c","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"answer"},"finish_reason":"stop"}]});
    let mut marked = plain.clone();
    marked["choices"][0]["message"]["content"] = json!([text("answer")]);
    assert!(openai::decode_chat_response(marked).is_err());
    let mut response = openai::decode_chat_response(plain).unwrap();
    let input = openai::decode_chat(
        json!({"model":"m","messages":[{"role":"assistant","content":[text("history")]}]}),
    )
    .unwrap();
    response.choices[0].message.content = input.messages[0].content.clone();
    for encoded in [
        openai::encode_chat_response(&response),
        openai::responses::encode_chat_response(&response),
        anthropic::encode_chat_response(&response),
        gemini::encode_chat_response(&response),
    ] {
        assert!(encoded.is_err());
    }
}

#[test]
fn chat_prediction_text_keeps_its_documented_marker() {
    let body = json!({"model":"m","messages":[{"role":"user","content":"update"}],"prediction":{"type":"content","content":[text("prefix")]}});
    let r = openai::decode_chat(body.clone()).unwrap();
    assert_eq!(openai::encode_chat(&r).unwrap(), body);
    assert!(openai::responses::encode_chat(&r).is_err());
}

#[test]
fn prediction_does_not_gain_image_breakpoints_from_shared_content_types() {
    let body = json!({"model":"m","messages":[{"role":"user","content":"update"}],"prediction":{"type":"content","content":[{"type":"image_url","image_url":{"url":"https://example.com/image.png"},"prompt_cache_breakpoint":{"mode":"explicit"}}]}});
    assert!(openai::decode_chat(body).is_err());
}

#[test]
fn image_markers_reject_null_and_malformed_values() {
    for marker in [
        Value::Null,
        json!({}),
        json!({"mode":"implicit"}),
        json!({"mode":"explicit","ttl":"30m"}),
    ] {
        assert!(openai::decode_chat(json!({"model":"m","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.com/image.png"},"prompt_cache_breakpoint":marker}]}]})).is_err());
        let decoded = openai::responses::decode_chat(
            json!({"model":"m","input":[{"role":"user","content":[{"type":"input_image","image_url":"https://example.com/image.png","prompt_cache_breakpoint":marker}]}]}),
        );
        assert!(decoded.is_err());
    }
}

#[test]
fn marked_assistant_input_rejects_refusal_mixing_in_responses() {
    for as_part in [false, true] {
        let mut message = json!({"role":"assistant","content":[text("history")]});
        if as_part {
            message["content"]
                .as_array_mut()
                .unwrap()
                .push(json!({"type":"refusal","refusal":"no"}));
        } else {
            message["refusal"] = json!("no");
        }
        let r = openai::decode_chat(json!({"model":"m","messages":[message]})).unwrap();
        assert!(openai::responses::encode_chat(&r).is_err());
    }
}
