use nyro_llm::{
    codec::{anthropic, gemini, openai},
    ir::{ChatRequest, Content, ContentPart},
};
use serde_json::{Value, json};

fn request(result: Value) -> Value {
    json!({"contents":[
        {"role":"model","parts":[{"functionCall":{"id":"call","name":"image","args":{}}}]},
        {"role":"user","parts":[{"functionResponse":result}]}
    ]})
}
fn result() -> Value {
    json!({"id":"call","name":"image","response":{"label":"photos","images":[{"$ref":"first.png"},{"$ref":"second.jpg"}]},"parts":[
        {"inlineData":{"mimeType":"image/png","data":"AQID","displayName":"first.png"}},
        {"inlineData":{"mimeType":"image/jpeg","data":"BAUG","displayName":"second.jpg"}}
    ]})
}
fn decode(result: Value) -> ChatRequest {
    gemini::decode_chat(request(result), "m", false).unwrap()
}

#[test]
fn native_function_result_keeps_response_images_and_reference_names() {
    let source = result();
    let decoded = decode(source.clone());
    let encoded = gemini::encode_chat(&decoded).unwrap();
    assert_eq!(
        encoded["contents"][1]["parts"][0]["functionResponse"],
        source
    );
    let ir = serde_json::to_value(&decoded).unwrap();
    let parts = &ir["messages"][1]["items"][0]["value"];
    assert_eq!(parts.as_array().unwrap().len(), 1);
    assert_eq!(parts[0]["type"], "gemini_function_response");
    assert_eq!(parts[0]["response"], source["response"]);
    assert_eq!(parts[0]["parts"], source["parts"]);
    assert!(parts[0].get("text").is_none());
}

#[test]
fn native_empty_and_unreferenced_image_parts_are_preserved() {
    for parts in [
        json!([]),
        json!([{ "inlineData":{"mimeType":"image/webp","data":"AQID"}}]),
    ] {
        let source = json!({"id":"call","name":"image","response":{},"parts":parts});
        assert_eq!(
            gemini::encode_chat(&decode(source.clone())).unwrap()["contents"][1]["parts"][0]["functionResponse"],
            source
        );
    }
    let plain = decode(json!({"id":"call","name":"image","response":{"value":1}}));
    assert!(matches!(
        plain.messages[1].content(),
        Some(Content::Text(_))
    ));
    let mut named = result();
    named["response"] =
        json!({"ordinary":{"$ref":"not a media reference","description":"literal JSON object"}});
    assert_eq!(
        gemini::encode_chat(&decode(named.clone())).unwrap()["contents"][1]["parts"][0]["functionResponse"],
        named
    );
}

#[test]
fn media_results_keep_parallel_call_identity_and_batch_validation() {
    let mut a = result();
    let mut b = result();
    a["id"] = json!("a");
    b["id"] = json!("b");
    let source = json!({"contents":[
        {"role":"model","parts":[{"functionCall":{"id":"a","name":"image","args":{}}},{"functionCall":{"id":"b","name":"image","args":{}}}]},
        {"role":"user","parts":[{"functionResponse":b},{"functionResponse":a}]}
    ]});
    let decoded = gemini::decode_chat(source.clone(), "m", false).unwrap();
    assert_eq!(
        gemini::encode_chat(&decoded).unwrap()["contents"],
        source["contents"]
    );
    let mut missing = decoded.clone();
    missing.messages.pop();
    assert!(gemini::encode_chat(&missing).is_err());
    let mut duplicate = decoded;
    duplicate.messages[2].tool_call_id = duplicate.messages[1].tool_call_id.clone();
    assert!(gemini::encode_chat(&duplicate).is_err());
    for id in [Value::Null, json!("unknown")] {
        let mut invalid = source.clone();
        invalid["contents"][1]["parts"][0]["functionResponse"]["id"] = id;
        assert!(gemini::decode_chat(invalid, "m", false).is_err());
    }
}

#[test]
fn native_image_results_fail_closed_for_other_protocols_and_ambiguous_bodies() {
    let mut decoded = decode(result());
    assert!(openai::encode_chat(&decoded).is_err());
    assert!(openai::responses::encode_chat(&decoded).is_err());
    decoded.generation.max_tokens = Some(100);
    assert!(anthropic::encode_chat(&decoded).is_err());
    let original = decoded.clone();
    for role in [nyro_llm::ir::Role::User, nyro_llm::ir::Role::Assistant] {
        let mut decoded = original.clone();
        decoded.messages[1].role = role;
        decoded.messages[1].tool_call_id = None;
        assert!(gemini::encode_chat(&decoded).is_err());
    }
    let Some(Content::Parts(parts)) = decoded.messages[1].content_mut() else {
        panic!("typed result body required")
    };
    parts.push(ContentPart::Text {
        text: "extra".into(),
        anthropic_cache_control: None,
        prompt_cache_breakpoint: None,
    });
    assert!(gemini::encode_chat(&decoded).is_err());
    let mut duplicated = original;
    let extra = duplicated.messages[1].items[0].clone();
    duplicated.messages[1].items.push(extra);
    assert!(gemini::encode_chat(&duplicated).is_err());
}

#[test]
fn invalid_media_and_references_are_rejected_on_wire_and_direct_ir() {
    let mut cases = Vec::new();
    for (key, value) in [
        ("mimeType", json!("image/gif")),
        ("mimeType", json!("application/pdf")),
        ("data", json!("%%%")),
        ("data", json!("")),
        ("displayName", json!("second.jpg")),
    ] {
        let mut source = result();
        source["parts"][0]["inlineData"][key] = value;
        cases.push(source);
    }
    for response in [
        json!({"image":{"$ref":"missing"}}),
        json!({"a":{"$ref":"first.png"},"b":{"$ref":"first.png"}}),
        json!({"image":{"$ref":2}}),
        json!(null),
        json!([]),
    ] {
        let mut source = result();
        source["response"] = response;
        cases.push(source);
    }
    for source in cases {
        assert!(
            gemini::decode_chat(request(source.clone()), "m", false).is_err(),
            "accepted {source}"
        );
        let mut direct = serde_json::to_value(decode(result())).unwrap();
        direct["messages"][1]["items"][0]["value"][0]["response"] = source["response"].clone();
        direct["messages"][1]["items"][0]["value"][0]["parts"] = source["parts"].clone();
        if let Ok(direct) = serde_json::from_value::<ChatRequest>(direct) {
            assert!(gemini::encode_chat(&direct).is_err());
        }
    }
    for invalid in [
        json!({"text":"not media"}),
        json!({"fileData":{"fileUri":"https://example.com/i.png"}}),
        json!({"inlineData":null}),
        json!({"inlineData":{"mimeType":"image/png","data":"AQID","unknown":1}}),
    ] {
        let mut source = result();
        source["parts"] = json!([invalid]);
        assert!(gemini::decode_chat(request(source), "m", false).is_err());
    }
}

#[test]
fn generic_tool_image_has_no_implicit_gemini_schema_mapping() {
    let mut decoded = decode(json!({"id":"call","name":"image","response":{}}));
    *decoded.messages[1].content_mut().unwrap() = Content::Parts(vec![ContentPart::ImageUrl {
        image_url: nyro_llm::ir::ImageUrl {
            url: "data:image/png;base64,AQID".into(),
            detail: None,
        },
        anthropic_cache_control: None,
        prompt_cache_breakpoint: None,
    }]);
    assert!(gemini::encode_chat(&decoded).is_err());
}
