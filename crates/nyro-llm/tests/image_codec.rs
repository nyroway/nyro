use nyro_llm::{
    codec::{anthropic, gemini, openai},
    ir::*,
};
use nyro_protocol::framing::Event;
use serde_json::{Value, json};

// Valid 1x1 PNG; codecs preserve bytes and leave image decoding to the vendor.
const PNG: &str =
    "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAIAAACQd1PeAAAADElEQVR4nGP4z8AAAAMBAQDJ/pLvAAAAAElFTkSuQmCC";
fn image_url() -> String {
    format!("data:image/png;base64,{PNG}")
}
fn chat(url: &str, detail: Option<&str>) -> Value {
    let mut image = json!({"url":url});
    if let Some(detail) = detail {
        image["detail"] = json!(detail);
    }
    json!({"model":"m","max_tokens":64,"messages":[{"role":"user","content":[{"type":"text","text":"before"},{"type":"image_url","image_url":image},{"type":"text","text":"after"}]}]})
}
fn anthro(source: Value) -> Value {
    json!({"model":"m","max_tokens":64,"messages":[{"role":"user","content":[{"type":"text","text":"before"},{"type":"image","source":source},{"type":"text","text":"after"}]}]})
}
fn responses(url: &str, detail: Option<&str>) -> Value {
    let mut image = json!({"type":"input_image","image_url":url});
    if let Some(detail) = detail {
        image["detail"] = json!(detail);
    }
    json!({"model":"m","max_output_tokens":64,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"before"},image,{"type":"input_text","text":"after"}]}]})
}
fn gem() -> Value {
    json!({"contents":[{"role":"user","parts":[{"text":"before"},{"inlineData":{"mimeType":"image/png","data":PNG}},{"text":"after"}]}],"generationConfig":{"maxOutputTokens":64}})
}

#[test]
fn inline_images_keep_mime_bytes_and_text_order_across_four_codecs() {
    let a = anthro(json!({"type":"base64","media_type":"image/png","data":PNG}));
    let expected_chat = chat(&image_url(), None);
    let expected_responses = responses(&image_url(), None);
    for r in [
        openai::decode_chat(expected_chat.clone()).unwrap(),
        anthropic::decode_chat(a.clone()).unwrap(),
        openai::responses::decode_chat(expected_responses.clone()).unwrap(),
        gemini::decode_chat(gem(), "m", false).unwrap(),
    ] {
        assert_eq!(
            openai::encode_chat(&r).unwrap()["messages"],
            expected_chat["messages"]
        );
        assert_eq!(
            anthropic::encode_chat(&r).unwrap()["messages"],
            a["messages"]
        );
        assert_eq!(
            openai::responses::encode_chat(&r).unwrap()["input"],
            expected_responses["input"]
        );
        assert_eq!(
            gemini::encode_chat(&r).unwrap()["contents"],
            gem()["contents"]
        );
    }
}
#[test]
fn remote_urls_and_detail_have_destination_specific_boundaries() {
    let url = "https://images.example.test/photo.png?token=opaque";
    let a = anthro(json!({"type":"url","url":url}));
    let r = anthropic::decode_chat(a.clone()).unwrap();
    assert_eq!(
        openai::encode_chat(&r).unwrap()["messages"],
        chat(url, None)["messages"]
    );
    assert_eq!(
        openai::responses::encode_chat(&r).unwrap()["input"],
        responses(url, None)["input"]
    );
    assert_eq!(
        anthropic::encode_chat(&r).unwrap()["messages"],
        a["messages"]
    );
    assert!(gemini::encode_chat(&r).is_err());
    for detail in ["auto", "low", "high", "original"] {
        let r = openai::responses::decode_chat(responses(&image_url(), Some(detail))).unwrap();
        assert_eq!(
            openai::encode_chat(&r).unwrap()["messages"],
            chat(&image_url(), Some(detail))["messages"]
        );
        assert_eq!(
            openai::responses::encode_chat(&r).unwrap()["input"],
            responses(&image_url(), Some(detail))["input"]
        );
        assert_eq!(anthropic::encode_chat(&r).is_ok(), detail == "auto");
        assert_eq!(gemini::encode_chat(&r).is_ok(), detail == "auto");
    }
}
#[test]
fn malformed_images_and_unsupported_sources_are_rejected() {
    for url in [
        "",
        "file:///tmp/image.png",
        "https://",
        "data:image/png;base64,",
        "data:image/png;base64,not_base64",
        "data:text/plain;base64,YQ==",
        "data:image/png,raw",
    ] {
        assert!(openai::decode_chat(chat(url, None)).is_err(), "{url}");
        assert!(
            openai::responses::decode_chat(responses(url, None)).is_err(),
            "{url}"
        );
    }
    assert!(openai::decode_chat(chat(&image_url(), Some("unknown"))).is_err());
    assert!(anthropic::decode_chat(anthro(json!({"type":"file","file_id":"file-1"}))).is_err());
    assert!(
        anthropic::decode_chat(anthro(
            json!({"type":"base64","media_type":"image/png","data":"invalid"})
        ))
        .is_err()
    );
    let mut v = gem();
    v["contents"][0]["parts"][1] =
        json!({"fileData":{"mimeType":"image/png","fileUri":"https://files.example.test/file"}});
    assert!(gemini::decode_chat(v, "m", false).is_err());
    let mut v = gem();
    v["contents"][0]["parts"][1]["text"] = json!("also text");
    assert!(gemini::decode_chat(v, "m", false).is_err());
    let mut v = gem();
    v["contents"][0]["parts"][1]["inlineData"]["mimeType"] = json!("audio/wav");
    assert!(gemini::decode_chat(v, "m", false).is_err());
}
#[test]
fn images_are_user_input_only_including_direct_ir_encoders() {
    for role in [Role::System, Role::Developer, Role::Assistant, Role::Tool] {
        let mut r = openai::decode_chat(chat(&image_url(), None)).unwrap();
        r.messages[0].role = role.clone();
        if role == Role::Tool {
            r.messages[0].tool_call_id = Some("call".into());
        }
        assert!(openai::encode_chat(&r).is_err());
        assert!(openai::responses::encode_chat(&r).is_err());
        assert!(anthropic::encode_chat(&r).is_err());
        assert!(gemini::encode_chat(&r).is_err());
    }
    let mut v = anthro(json!({"type":"base64","media_type":"image/png","data":PNG}));
    v["messages"][0]["role"] = json!("assistant");
    assert!(anthropic::decode_chat(v).is_err());
    let mut v = responses(&image_url(), None);
    v["input"][0]["role"] = json!("system");
    assert!(openai::responses::decode_chat(v).is_err());
    let mut v = gem();
    v["contents"][0]["role"] = json!("model");
    assert!(gemini::decode_chat(v, "m", false).is_err());
}
#[test]
fn accepting_input_images_does_not_silently_accept_generated_images() {
    let block =
        json!({"type":"image","source":{"type":"base64","media_type":"image/png","data":PNG}});
    let v = json!({"id":"r","type":"message","role":"assistant","model":"m","content":[block],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}});
    assert!(anthropic::decode_chat_response(v).is_err());
    let mut decoder = anthropic::StreamDecoder::new();
    decoder.push(&Event { event:Some("message_start".into()), data:json!({"type":"message_start","message":{"id":"r","type":"message","role":"assistant","model":"m","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":0}}}).to_string() }).unwrap();
    assert!(
        decoder
            .push(&Event {
                event: Some("content_block_start".into()),
                data: json!({"type":"content_block_start","index":0,"content_block":block})
                    .to_string()
            })
            .is_err()
    );

    let v = json!({"candidates":[{"content":{"role":"model","parts":[{"inlineData":{"mimeType":"image/png","data":PNG}}]},"finishReason":"STOP"}]});
    assert!(gemini::decode_chat_response(v.clone()).is_err());
    let mut decoder = gemini::StreamDecoder::new();
    assert!(
        decoder
            .push(&Event {
                event: None,
                data: v.to_string()
            })
            .is_err()
    );
}

#[test]
fn image_mime_types_are_preserved_with_gemini_gif_boundary() {
    for mime in ["image/png", "image/jpeg", "image/webp", "image/gif"] {
        // These are envelope tests: intentionally do not claim pixel/MIME sniffing.
        let url = format!("data:{mime};base64,{PNG}");
        let r = openai::decode_chat(chat(&url, None)).unwrap();
        let a = anthropic::encode_chat(&r).unwrap();
        assert_eq!(
            a["messages"][0]["content"][1]["source"],
            json!({"type":"base64","media_type":mime,"data":PNG})
        );
        assert_eq!(
            openai::responses::encode_chat(&r).unwrap()["input"][0]["content"][1]["image_url"],
            url
        );
        let g = gemini::encode_chat(&r);
        if mime == "image/gif" {
            assert!(g.is_err());
        } else {
            assert_eq!(
                g.unwrap()["contents"][0]["parts"][1]["inlineData"],
                json!({"mimeType":mime,"data":PNG})
            );
        }
    }
}
