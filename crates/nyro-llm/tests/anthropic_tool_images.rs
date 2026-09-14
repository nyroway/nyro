use nyro_llm::{
    codec::{anthropic, openai},
    ir::{Content, ContentPart, Role},
};
use serde_json::{Value, json};

fn request(content: Value) -> Value {
    json!({"model":"m","max_tokens":32,"messages":[
        {"role":"assistant","content":[{"type":"tool_use","id":"t","name":"capture","input":{}}]},
        {"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":content}]}
    ]})
}

fn image(source: Value) -> Value {
    json!({"type":"image","source":source})
}

#[test]
fn tool_result_images_round_trip_inside_the_result_in_original_order() {
    for source in [
        json!({"type":"base64","media_type":"image/png","data":"AQID"}),
        json!({"type":"base64","media_type":"image/jpeg","data":"AQID"}),
        json!({"type":"base64","media_type":"image/webp","data":"AQID"}),
        json!({"type":"base64","media_type":"image/gif","data":"AQID"}),
        json!({"type":"url","url":"https://example.invalid/screenshot.png"}),
    ] {
        let body = request(
            json!([{"type":"text","text":"before"}, image(source), {"type":"text","text":"after"}]),
        );
        let ir = anthropic::decode_chat(body.clone()).unwrap();
        assert_eq!(ir.messages.len(), 2);
        assert_eq!(ir.messages[1].role, Role::Tool);
        assert_eq!(ir.messages[1].tool_call_id.as_deref(), Some("t"));
        let Some(Content::Parts(parts)) = ir.messages[1].content() else {
            panic!("expected ordered result parts")
        };
        assert!(matches!(parts[1], ContentPart::ImageUrl { .. }));
        assert_eq!(anthropic::encode_chat(&ir).unwrap(), body);
        assert!(openai::encode_chat(&ir).is_err());
    }
}

#[test]
fn parallel_image_results_keep_ids_error_and_inner_outer_cache_controls() {
    let body = json!({"model":"m","max_tokens":32,"messages":[
        {"role":"assistant","content":[{"type":"tool_use","id":"a","name":"f","input":{}},{"type":"tool_use","id":"b","name":"g","input":{}}]},
        {"role":"user","content":[
            {"type":"tool_result","tool_use_id":"b","is_error":true,"cache_control":{"type":"ephemeral","ttl":"5m"},"content":[
                {"type":"text","text":"failed screenshot","cache_control":{"type":"ephemeral","ttl":"1h"}},
                {"type":"image","source":{"type":"base64","media_type":"image/png","data":"AQID"},"cache_control":{"type":"ephemeral"}}
            ]},
            {"type":"tool_result","tool_use_id":"a","content":[{"type":"image","source":{"type":"url","url":"https://example.invalid/a.png"}}]},
            {"type":"text","text":"continue"}
        ]}
    ]});
    let ir = anthropic::decode_chat(body.clone()).unwrap();
    assert_eq!(ir.messages[1].tool_call_id.as_deref(), Some("b"));
    assert!(ir.messages[1].tool_error);
    assert_eq!(ir.messages[2].tool_call_id.as_deref(), Some("a"));
    assert_eq!(anthropic::encode_chat(&ir).unwrap(), body);
    let mut missing = ir.clone();
    missing.messages.remove(2);
    assert!(anthropic::encode_chat(&missing).is_err());
    let mut duplicate = ir;
    duplicate.messages[2].tool_call_id = Some("b".into());
    assert!(anthropic::encode_chat(&duplicate).is_err());
}

#[test]
fn malformed_images_and_non_image_nested_content_remain_rejected() {
    for source in [
        json!({"type":"base64","media_type":"image/png","data":"not base64"}),
        json!({"type":"base64","media_type":"image/png","data":""}),
        json!({"type":"base64","media_type":"image/heic","data":"AQID"}),
        json!({"type":"url","url":"data:image/png;base64,AQID"}),
        json!({"type":"url","url":"file:///tmp/image.png"}),
        json!({"type":"url","url":"https://"}),
        json!({"type":"file","file_id":"file-1"}),
    ] {
        assert!(anthropic::decode_chat(request(json!([image(source)]))).is_err());
    }
    for nested in [
        json!({"type":"tool_result","tool_use_id":"nested","content":"no"}),
        json!({"type":"tool_use","id":"nested","name":"f","input":{}}),
        json!({"type":"thinking","thinking":"hidden","signature":"s"}),
        json!({"type":"document","source":{"type":"text","media_type":"text/plain","data":"no"}}),
    ] {
        assert!(anthropic::decode_chat(request(json!([nested]))).is_err());
    }
    let valid = image(json!({"type":"base64","media_type":"image/png","data":"AQID"}));
    for role in ["assistant", "system", "tool"] {
        let mut wrong_role = request(json!([valid]));
        wrong_role["messages"][1]["role"] = json!(role);
        assert!(anthropic::decode_chat(wrong_role).is_err());
    }
    let generated = json!({"id":"r","type":"message","role":"assistant","model":"m","content":[valid],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}});
    assert!(anthropic::decode_chat_response(generated).is_err());
}

#[test]
fn tool_image_projection_keeps_existing_role_and_detail_restrictions() {
    let source = json!({"type":"url","url":"https://example.invalid/image.png"});
    let ir = anthropic::decode_chat(request(json!([image(source)]))).unwrap();
    for role in [Role::Assistant, Role::System, Role::Developer] {
        let mut wrong_role = ir.clone();
        wrong_role.messages[1].role = role;
        wrong_role.messages[1].tool_call_id = None;
        assert!(anthropic::encode_chat(&wrong_role).is_err());
    }
    let mut detailed = ir;
    let Some(Content::Parts(parts)) = detailed.messages[1].content_mut() else {
        unreachable!()
    };
    let ContentPart::ImageUrl { image_url, .. } = &mut parts[0] else {
        unreachable!()
    };
    image_url.detail = Some("high".into());
    assert!(anthropic::encode_chat(&detailed).is_err());
}
