use nyro_llm::{
    codec::{
        anthropic, gemini,
        openai::{self, responses},
    },
    ir::*,
};
use serde_json::{Value, json};

fn chat() -> Value {
    json!({"model":"m","messages":[{"role":"user","content":"Hi"}],"max_tokens":2048})
}
fn request() -> Value {
    json!({"model":"m","input":"Hi","max_output_tokens":2048})
}

#[test]
fn effort_maps_between_openai_apis_without_defaulting_or_vendor_coercion() {
    for effort in ["none", "minimal", "low", "medium", "high", "xhigh", "max"] {
        let mut c = chat();
        c["reasoning_effort"] = json!(effort);
        let mut r = request();
        r["reasoning"] = json!({"effort":effort});
        for ir in [
            openai::decode_chat(c).unwrap(),
            responses::decode_chat(r).unwrap(),
        ] {
            assert_eq!(
                openai::encode_chat(&ir).unwrap()["reasoning_effort"],
                effort
            );
            assert_eq!(
                responses::encode_chat(&ir).unwrap()["reasoning"],
                json!({"effort":effort})
            );
            assert!(anthropic::encode_chat(&ir).is_err());
            assert!(gemini::encode_chat(&ir).is_err());
        }
    }
    for ir in [
        openai::decode_chat(chat()).unwrap(),
        responses::decode_chat(request()).unwrap(),
    ] {
        assert!(
            openai::encode_chat(&ir)
                .unwrap()
                .get("reasoning_effort")
                .is_none()
        );
        assert!(
            responses::encode_chat(&ir)
                .unwrap()
                .get("reasoning")
                .is_none()
        );
    }
    for effort in [
        json!("ultra"),
        json!("HIGH"),
        json!(4),
        json!(true),
        json!(""),
    ] {
        let mut c = chat();
        c["reasoning_effort"] = effort;
        assert!(openai::decode_chat(c).is_err());
    }
}
#[test]
fn responses_controls_do_not_disappear_through_shared_chat_validation() {
    for extra in [
        json!({}),
        json!({"summary":"auto"}),
        json!({"generate_summary":"concise"}),
        json!({"context":"auto"}),
        json!({"mode":"standard"}),
    ] {
        let mut r = request();
        r["reasoning"] = extra.clone();
        let ir = responses::decode_chat(r.clone()).unwrap();
        for ir in [ir, {
            r["reasoning"]["effort"] = json!("medium");
            responses::decode_chat(r).unwrap()
        }] {
            if extra != json!({}) || ir.responses_reasoning.as_ref().unwrap().effort.is_none() {
                assert!(openai::encode_chat(&ir).is_err());
            }
            assert!(anthropic::encode_chat(&ir).is_err());
            assert!(gemini::encode_chat(&ir).is_err());
            assert!(responses::encode_chat(&ir).is_ok());
        }
    }
    let mut r = request();
    r["reasoning"] = json!({"effort":"low"});
    r["include"] = json!(["reasoning.encrypted_content"]);
    let ir = responses::decode_chat(r).unwrap();
    assert!(openai::encode_chat(&ir).is_err());
    assert!(gemini::encode_chat(&ir).is_err());
}
#[test]
fn direct_ir_conflicting_effort_is_rejected_and_equal_values_merge() {
    let mut r = request();
    r["reasoning"] = json!({"effort":"high"});
    let mut value = serde_json::to_value(responses::decode_chat(r).unwrap()).unwrap();
    for (effort, accepted) in [("high", true), ("low", false)] {
        value["openai"]["reasoning_effort"] = json!(effort);
        let ir: ChatRequest = serde_json::from_value(value.clone()).unwrap();
        assert_eq!(openai::encode_chat(&ir).is_ok(), accepted);
        assert_eq!(responses::encode_chat(&ir).is_ok(), accepted);
    }
}
fn answer(text: &str) -> Value {
    json!({"id":"r","object":"chat.completion","created":1,"model":"m","usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5},"choices":[{"index":0,"message":{"role":"assistant","content":text},"finish_reason":"stop"}]})
}
#[test]
fn legacy_reasoning_fields_are_not_promoted_to_generic_reasoning() {
    for field in ["reasoning_content", "reasoning", "reasoning_signature"] {
        let mut c = chat();
        c["messages"][0]["role"] = json!("assistant");
        c["messages"][0][field] = json!("opaque");
        assert!(openai::decode_chat(c).is_err());
        let mut r = answer("answer");
        r["choices"][0]["message"][field] = json!("opaque");
        assert!(openai::decode_chat_response(r).is_err());
        let frame = json!({"id":"r","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{field:"opaque"}}]});
        assert!(openai::decode_chat_event(&frame.to_string()).is_err());
    }
}
#[test]
fn think_tags_remain_literal_text_across_json_protocol_conversion() {
    for text in [
        "<think>reason</think>answer",
        "  <think> a </think> x <think>b</think> y  ",
        "<think>unfinished",
        "Example: `<think>…</think>`",
    ] {
        let ir = openai::decode_chat_response(answer(text)).unwrap();
        for back in [
            openai::decode_chat_response(openai::encode_chat_response(&ir).unwrap()).unwrap(),
            responses::decode_chat_response(responses::encode_chat_response(&ir).unwrap()).unwrap(),
            anthropic::decode_chat_response(anthropic::encode_chat_response(&ir).unwrap()).unwrap(),
            gemini::decode_chat_response(gemini::encode_chat_response(&ir).unwrap()).unwrap(),
        ] {
            let content = back.choices[0].message.content().unwrap();
            let actual = match content {
                Content::Text(s) => s.clone(),
                Content::Parts(p) => p
                    .iter()
                    .map(|p| match p {
                        ContentPart::Text { text, .. } => text.as_str(),
                        _ => panic!("fabricated reasoning"),
                    })
                    .collect::<String>(),
            };
            assert_eq!(actual, text);
        }
    }
}

#[test]
fn split_think_tags_stay_literal_in_streams_for_every_target() {
    let fragments = ["  <th", "ink>summary</thi", "nk>answer  "];
    let mut events = Vec::new();
    for text in fragments {
        events.push(openai::decode_chat_event(&json!({"id":"r","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":text}}]}).to_string()).unwrap());
    }
    events.push(openai::decode_chat_event(&json!({"id":"r","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}).to_string()).unwrap());
    events.push(ChatEvent::Done);
    let mut a = anthropic::StreamEncoder::new("m".into());
    let mut g = gemini::StreamEncoder::new("m".into());
    let mut r = responses::StreamEncoder::new("m".into());
    let mut outputs = [String::new(), String::new(), String::new(), String::new()];
    for e in events {
        for (output, encoded) in outputs.iter_mut().zip([
            openai::encode_chat_event(&e, "m"),
            a.push(&e),
            g.push(&e),
            r.push(&e),
        ]) {
            output.push_str(&encoded.unwrap());
        }
    }
    for (format, output) in outputs.into_iter().enumerate() {
        let mut framing = nyro_protocol::framing::Decoder::new(65536);
        let frames = framing.push(output.as_bytes()).unwrap();
        let mut text = String::new();
        for frame in frames {
            if frame.data == "[DONE]" {
                continue;
            }
            let v: Value = serde_json::from_str(&frame.data).unwrap();
            let delta = match format {
                0 => v
                    .pointer("/choices/0/delta/content")
                    .and_then(Value::as_str),
                1 if v["type"] == "content_block_delta" => {
                    v.pointer("/delta/text").and_then(Value::as_str)
                }
                2 => v
                    .pointer("/candidates/0/content/parts/0/text")
                    .and_then(Value::as_str),
                3 if v["type"] == "response.output_text.delta" => v["delta"].as_str(),
                _ => None,
            };
            if let Some(s) = delta {
                text.push_str(s);
            }
            assert!(!frame.data.contains("reasoning_content"));
            assert!(!frame.data.contains("signature_delta"));
            assert!(!frame.data.contains("reasoning_summary"));
        }
        assert_eq!(text, fragments.concat(), "format={format}");
    }
}
