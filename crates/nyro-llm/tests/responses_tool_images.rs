use nyro_llm::{
    codec::{
        anthropic,
        openai::{self, responses},
    },
    ir::{Content, ContentPart, ImageUrl, MessageItem},
};
use serde_json::{Value, json};

fn request(output: Value) -> Value {
    json!({"model":"m","max_output_tokens":64,"input":[
        {"role":"user","content":[{"type":"input_text","text":"inspect"}]},
        {"type":"function_call","call_id":"c","name":"inspect","arguments":"{}"},
        {"type":"function_call_output","call_id":"c","output":output}
    ]})
}

#[test]
fn tool_images_preserve_order_url_data_detail_and_breakpoints() {
    for detail in [
        None,
        Some("auto"),
        Some("low"),
        Some("high"),
        Some("original"),
    ] {
        for url in [
            "https://example.com/image.png",
            "data:image/png;base64,YQ==",
        ] {
            let mut image = json!({"type":"input_image","image_url":url,"prompt_cache_breakpoint":{"mode":"explicit"}});
            if let Some(detail) = detail {
                image["detail"] = json!(detail);
            }
            let output = json!([{"type":"input_text","text":"before"},image,{"type":"input_text","text":"after"}]);
            let decoded = responses::decode_chat(request(output.clone())).unwrap();
            let encoded = responses::encode_chat(&decoded).unwrap();
            assert_eq!(encoded["input"][2]["output"], output);
            assert_eq!(encoded["input"][2]["call_id"], "c");
            assert_eq!(responses::decode_chat(encoded).unwrap(), decoded);
            assert!(openai::encode_chat(&decoded).is_err());
            assert!(anthropic::encode_chat(&decoded).is_err());
        }
    }
}

#[test]
fn encoder_accepts_canonical_tool_images() {
    let mut decoded = responses::decode_chat(request(json!("result"))).unwrap();
    decoded.messages[2].items = vec![MessageItem::Content(Content::Parts(vec![
        ContentPart::ImageUrl {
            anthropic_cache_control: None,
            prompt_cache_breakpoint: None,
            image_url: ImageUrl {
                url: "data:image/jpeg;base64,YQ==".into(),
                detail: Some("high".into()),
            },
        },
    ]))];
    assert_eq!(
        responses::encode_chat(&decoded).unwrap()["input"][2]["output"],
        json!([{"type":"input_image","image_url":"data:image/jpeg;base64,YQ==","detail":"high"}])
    );
    assert!(anthropic::encode_chat(&decoded).is_err());
    decoded.messages[2].tool_error = true;
    assert!(responses::encode_chat(&decoded).is_err());
}

#[test]
fn tool_images_reject_invalid_sources_files_and_unknown_fields() {
    for image in [
        json!({"type":"input_image"}),
        json!({"type":"input_image","image_url":null}),
        json!({"type":"input_image","image_url":"file:///image.png"}),
        json!({"type":"input_image","image_url":"data:image/png;base64,invalid!"}),
        json!({"type":"input_image","image_url":"data:text/plain;base64,YQ=="}),
        json!({"type":"input_image","file_id":"file1"}),
        json!({"type":"input_image","image_url":"https://example.com/i.png","file_id":"file1"}),
        json!({"type":"input_image","image_url":"https://example.com/i.png","detail":"invalid"}),
        json!({"type":"input_image","image_url":"https://example.com/i.png","prompt_cache_breakpoint":null}),
        json!({"type":"input_image","image_url":"https://example.com/i.png","prompt_cache_breakpoint":{"mode":"implicit"}}),
        json!({"type":"input_image","image_url":"https://example.com/i.png","extra":true}),
        json!({"type":"input_file","file_id":"file1"}),
    ] {
        assert!(
            responses::decode_chat(request(json!([image.clone()]))).is_err(),
            "{image}"
        );
    }
}

#[test]
fn parallel_results_keep_call_ids_and_anthropic_image_order() {
    let output = json!([{"type":"input_text","text":"before"},{"type":"input_image","image_url":"data:image/png;base64,YQ=="},{"type":"input_image","image_url":"https://example.com/a.png"},{"type":"input_text","text":"after"}]);
    let mut body = request(output.clone());
    body["input"].as_array_mut().unwrap().insert(
        2,
        json!({"type":"function_call","call_id":"b","name":"inspect","arguments":"{}"}),
    );
    body["input"].as_array_mut().unwrap().insert(3, json!({"type":"function_call_output","call_id":"b","output":[{"type":"input_image","image_url":"https://example.com/b.png"}]}));
    let decoded = responses::decode_chat(body).unwrap();
    let anthropic = anthropic::encode_chat(&decoded).unwrap();
    let results = &anthropic["messages"][2]["content"];
    assert_eq!(results[0]["tool_use_id"], "b");
    assert_eq!(results[1]["tool_use_id"], "c");
    assert_eq!(
        results[1]["content"][1]["source"],
        json!({"type":"base64","media_type":"image/png","data":"YQ=="})
    );
    assert_eq!(
        results[1]["content"][2]["source"],
        json!({"type":"url","url":"https://example.com/a.png"})
    );
    let back = responses::encode_chat(&anthropic::decode_chat(anthropic).unwrap()).unwrap();
    assert_eq!(back["input"][3]["call_id"], "b");
    assert_eq!(back["input"][4]["call_id"], "c");
    assert_eq!(back["input"][4]["output"], output);
}
