use nyro_llm::codec::{anthropic, gemini, openai};
use serde_json::{Value, json};

fn request(responses: bool) -> Value {
    if responses {
        json!({"model":"public","input":"Hello","max_output_tokens":32})
    } else {
        json!({"model":"public","messages":[{"role":"user","content":"Hello"}],"max_tokens":32})
    }
}

#[test]
fn openai_cache_retention_and_key_survive_both_api_formats() {
    for responses in [false, true] {
        for retention in ["in_memory", "24h"] {
            let mut body = request(responses);
            body["prompt_cache_retention"] = json!(retention);
            body["prompt_cache_key"] = json!("tenant:conversation");
            let r = if responses {
                openai::responses::decode_chat(body)
            } else {
                openai::decode_chat(body)
            }
            .unwrap();
            for encoded in [openai::encode_chat(&r), openai::responses::encode_chat(&r)] {
                let encoded = encoded.unwrap();
                assert_eq!(encoded["prompt_cache_retention"], retention);
                assert_eq!(encoded["prompt_cache_key"], "tenant:conversation");
            }
            assert!(anthropic::encode_chat(&r).is_err());
            assert!(gemini::encode_chat(&r).is_err());
            // Retention alone must also make incompatible targets ineligible.
            let mut r = r;
            r.openai.prompt_cache_key = None;
            assert!(anthropic::encode_chat(&r).is_err());
            assert!(gemini::encode_chat(&r).is_err());
        }
    }
}

#[test]
fn cache_retention_absence_null_and_invalid_values_are_distinct() {
    for responses in [false, true] {
        for value in [
            None,
            Some(Value::Null),
            Some(json!("1h")),
            Some(json!("")),
            Some(json!(24)),
            Some(json!({"ttl":"24h"})),
        ] {
            let mut body = request(responses);
            if let Some(value) = &value {
                body["prompt_cache_retention"] = value.clone();
            }
            let decoded = if responses {
                openai::responses::decode_chat(body)
            } else {
                openai::decode_chat(body)
            };
            if value.as_ref().is_none_or(Value::is_null) {
                let r = decoded.unwrap();
                assert!(
                    openai::encode_chat(&r)
                        .unwrap()
                        .get("prompt_cache_retention")
                        .is_none()
                );
                assert!(
                    openai::responses::encode_chat(&r)
                        .unwrap()
                        .get("prompt_cache_retention")
                        .is_none()
                );
                assert!(anthropic::encode_chat(&r).is_ok());
                assert!(gemini::encode_chat(&r).is_ok());
            } else {
                assert!(decoded.is_err(), "{responses}: {value:?}");
            }
        }
    }
}

#[test]
fn vendor_cache_controls_are_not_silently_converted() {
    let mut anthropic =
        json!({"model":"public","messages":[{"role":"user","content":"Hello"}],"max_tokens":32});
    anthropic["cache_control"] = json!({"type":"ephemeral","ttl":"1h"});
    assert!(anthropic::decode_chat(anthropic).is_err());
    assert!(gemini::decode_chat(json!({"contents":[{"role":"user","parts":[{"text":"Hello"}]}],"cachedContent":"cachedContents/existing"}), "public", false).is_err());
    // Per-block breakpoints are not part of the strict content subset yet.
    for responses in [false, true] {
        let mut body = request(responses);
        if responses {
            body["input"] = json!([{"role":"user","content":[{"type":"input_text","text":"Hello","prompt_cache_breakpoint":{"mode":"explicit"}}]}]);
        } else {
            body["messages"][0]["content"] = json!([{"type":"text","text":"Hello","prompt_cache_breakpoint":{"mode":"explicit"}}]);
        }
        assert!(
            if responses {
                openai::responses::decode_chat(body)
            } else {
                openai::decode_chat(body)
            }
            .is_err()
        );
    }
}

#[test]
fn current_openai_cache_options_preserve_modes_ttl_and_independent_retention() {
    for responses in [false, true] {
        for options in [
            json!({}),
            json!({"mode":"implicit"}),
            json!({"ttl":"30m"}),
            json!({"mode":"explicit","ttl":"30m"}),
        ] {
            let mut body = request(responses);
            body["prompt_cache_options"] = options.clone();
            body["prompt_cache_retention"] = json!("24h");
            let mut r = if responses {
                openai::responses::decode_chat(body)
            } else {
                openai::decode_chat(body)
            }
            .unwrap();
            for encoded in [openai::encode_chat(&r), openai::responses::encode_chat(&r)] {
                let encoded = encoded.unwrap();
                assert_eq!(encoded["prompt_cache_options"], options);
                assert_eq!(encoded["prompt_cache_retention"], "24h");
                assert!(encoded.get("prompt_cache_key").is_none());
            }
            r.openai.prompt_cache_retention = None;
            assert!(anthropic::encode_chat(&r).is_err());
            assert!(gemini::encode_chat(&r).is_err());
        }
    }
}

#[test]
fn current_cache_options_reject_invalid_or_unimplemented_fields() {
    for responses in [false, true] {
        for options in [
            json!("implicit"),
            json!({"ttl":"24h"}),
            json!({"ttl":"5m"}),
            json!({"mode":"auto"}),
            json!({"mode":null}),
            json!({"ttl":null}),
            json!({"comparison_response_id":"resp_previous"}),
        ] {
            let mut body = request(responses);
            body["prompt_cache_options"] = options.clone();
            assert!(
                if responses {
                    openai::responses::decode_chat(body)
                } else {
                    openai::decode_chat(body)
                }
                .is_err(),
                "{responses}: {options}"
            );
        }
        let mut body = request(responses);
        body["prompt_cache_options"] = Value::Null;
        let r = if responses {
            openai::responses::decode_chat(body)
        } else {
            openai::decode_chat(body)
        }
        .unwrap();
        assert!(
            openai::encode_chat(&r)
                .unwrap()
                .get("prompt_cache_options")
                .is_none()
        );
        assert!(
            openai::responses::encode_chat(&r)
                .unwrap()
                .get("prompt_cache_options")
                .is_none()
        );
    }
}

#[test]
fn responses_accepts_valid_cache_option_echoes_but_rejects_unimplemented_details() {
    let response = json!({"id":"r","object":"response","created_at":1,"model":"m","status":"completed","output":[{"type":"message","id":"msg_r","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Hello"}]}]});
    for options in [
        Value::Null,
        json!({}),
        json!({"mode":"implicit","ttl":"30m"}),
    ] {
        let mut body = response.clone();
        body["prompt_cache_options"] = options;
        openai::responses::decode_chat_response(body).unwrap();
    }
    for options in [
        json!({"ttl":"1h"}),
        json!({"comparison_response_id":"prior"}),
        json!({"mode":"unknown"}),
    ] {
        let mut body = response.clone();
        body["prompt_cache_options"] = options;
        assert!(openai::responses::decode_chat_response(body).is_err());
    }
}
