use nyro_llm::codec::openai::*;
use serde_json::json;
#[test]
fn typed_chat_round_trip_preserves_content_tools_and_options() {
    let value = json!({"model":"alias","messages":[{"role":"user","content":[{"type":"text","text":"hello"},{"type":"image_url","image_url":{"url":"https://example.test/image","detail":"low"}},{"type":"input_audio","input_audio":{"data":"YQ==","format":"wav"}}]},{"role":"assistant","tool_calls":[{"id":"call1","type":"function","function":{"name":"weather","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call1","content":"sunny"}],"stream":true,"stream_options":{"include_usage":true},"temperature":0.4,"max_tokens":17,"max_completion_tokens":20,"tools":[{"type":"function","function":{"name":"weather","parameters":{"type":"object"},"strict":true}}],"tool_choice":"auto","response_format":{"type":"json_object"},"seed":42,"user":"u","parallel_tool_calls":false});
    let request = decode_chat(value.clone()).unwrap();
    assert_eq!(request.model, "alias");
    assert_eq!(request.messages.len(), 3);
    assert_eq!(encode_chat(&request).unwrap(), value);
    assert!(decode_chat(json!({"model":"m","messages":[],"unknown":1})).is_err());
    assert!(
        decode_chat(json!({"model":"m","messages":[{"role":"wizard","content":"x"}]})).is_err()
    );
    assert!(decode_chat(json!({"model":"m","messages":[{"role":"user","content":[{"type":"video","data":"x"}]}]})).is_err());
}
#[test]
fn embeddings_have_four_inputs_and_two_outputs() {
    for input in [
        json!("hello"),
        json!(["hello", "world"]),
        json!([1, 2]),
        json!([[1, 2], [3]]),
    ] {
        let value = json!({"model":"alias","input":input,"dimensions":2,"encoding_format":"float","user":"u"});
        assert_eq!(
            encode_embedding(&decode_embedding(value.clone()).unwrap()).unwrap(),
            value
        );
    }
    for embedding in [json!([0.1, 0.2]), json!("zczMPc3MTD4=")] {
        let value = json!({"object":"list","model":"upstream","data":[{"object":"embedding","index":0,"embedding":embedding}],"usage":{"prompt_tokens":2,"total_tokens":2}});
        let mut response = decode_embedding_response(value.clone()).unwrap();
        assert_eq!(encode_embedding_response(&response).unwrap(), value);
        response.model = "alias".into();
        assert_eq!(
            encode_embedding_response(&response).unwrap()["model"],
            "alias"
        );
    }
    for input in [json!([]), json!([[]]), json!([-1]), json!(["a", 1])] {
        assert!(decode_embedding(json!({"model":"m","input":input})).is_err());
    }
    assert!(decode_embedding(json!({"model":"m","input":"x","stream":true})).is_err());
}
#[test]
fn response_and_tool_stream_round_trip_with_usage_and_done() {
    let response = json!({"id":"c1","object":"chat.completion","created":1,"model":"upstream","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}});
    assert_eq!(
        encode_chat_response(&decode_chat_response(response.clone()).unwrap()).unwrap(),
        response
    );
    let chunk = json!({"id":"c1","object":"chat.completion.chunk","created":1,"model":"upstream","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call1","type":"function","function":{"name":"weather","arguments":"{"}}]},"finish_reason":null}]});
    let event = decode_chat_event(&chunk.to_string()).unwrap();
    assert!(!event.is_done());
    let encoded = encode_chat_event(&event, "alias").unwrap();
    let mut expected = chunk;
    expected["model"] = json!("alias");
    assert!(encoded.ends_with("\n\n"));
    assert_eq!(
        serde_json::from_str::<serde_json::Value>(encoded.strip_prefix("data: ").unwrap().trim())
            .unwrap(),
        expected
    );
    assert!(decode_chat_event("[DONE]").unwrap().is_done());
    assert_eq!(
        encode_chat_event(&decode_chat_event("[DONE]").unwrap(), "m").unwrap(),
        "data: [DONE]\n\n"
    );
    assert!(decode_chat_event(r#"{"error":{"message":"bad"}}"#).is_err());
}
#[test]
fn request_helpers_and_usage_only_chunk_keep_workloads_separate() {
    use nyro_llm::{Request, Workload};
    let mut request = Request::Chat(
        decode_chat(
            json!({"model":"alias","messages":[{"role":"user","content":"hello"}],"stream":true}),
        )
        .unwrap(),
    );
    assert_eq!(request.workload(), Workload::Chat);
    assert!(request.is_streaming());
    request.set_model("upstream".into());
    assert_eq!(request.model(), "upstream");
    let request =
        Request::Embedding(decode_embedding(json!({"model":"embed","input":[1,2]})).unwrap());
    assert_eq!(request.workload(), Workload::Embedding);
    assert!(!request.is_streaming());
    assert_eq!(
        serde_json::to_value(Workload::Embedding).unwrap(),
        json!("embedding")
    );
    let chunk = json!({"id":"c","object":"chat.completion.chunk","created":1,"model":"u","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"prompt_tokens_details":{"cached_tokens":2,"audio_tokens":1},"completion_tokens_details":{"reasoning_tokens":2,"audio_tokens":1,"accepted_prediction_tokens":1,"rejected_prediction_tokens":0}}});
    let event = decode_chat_event(&chunk.to_string()).unwrap();
    let encoded = encode_chat_event(&event, "alias").unwrap();
    let value: serde_json::Value =
        serde_json::from_str(encoded.strip_prefix("data: ").unwrap().trim()).unwrap();
    assert_eq!(value["usage"], chunk["usage"]);
    assert_eq!(value["choices"], json!([]));
}
#[test]
fn invalid_generation_and_nested_unknown_fields_fail_explicitly() {
    for extra in [
        json!({"temperature":3}),
        json!({"top_p":-0.1}),
        json!({"max_tokens":0}),
        json!({"stream_options":{"include_usage":true}}),
        json!({"tools":[{"type":"function","function":{"name":"f","unexpected":true}}]}),
    ] {
        let mut value = json!({"model":"m","messages":[{"role":"user","content":"hello"}]});
        value
            .as_object_mut()
            .unwrap()
            .extend(extra.as_object().unwrap().clone());
        assert!(decode_chat(value).is_err());
    }
    assert!(
        decode_chat(json!({"model":"m","messages":[{"role":"tool","content":"value"}]})).is_err()
    );
    assert!(decode_chat_response(json!({"error":{"message":"upstream failed"}})).is_err());
    assert!(decode_embedding_response(json!({"error":{"message":"upstream failed"}})).is_err());
}
#[test]
fn stream_obfuscation_is_preserved_when_requested() {
    let chunk = json!({"id":"c","object":"chat.completion.chunk","created":1,"model":"u","choices":[],"obfuscation":"abc"});
    let event = decode_chat_event(&chunk.to_string()).unwrap();
    let encoded = encode_chat_event(&event, "alias").unwrap();
    let value: serde_json::Value =
        serde_json::from_str(encoded.strip_prefix("data: ").unwrap().trim()).unwrap();
    assert_eq!(value["obfuscation"], "abc");
}
