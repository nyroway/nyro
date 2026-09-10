use nyro_llm::codec::{anthropic, gemini, openai};
use nyro_protocol::framing::Event;
use serde_json::{Value, json};
fn response(usage: Value) -> Value {
    json!({"id":"r","type":"message","role":"assistant","model":"m","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","stop_sequence":null,"usage":usage})
}
fn usage() -> Value {
    json!({"input_tokens":3,"cache_creation_input_tokens":4,"cache_read_input_tokens":5,"output_tokens":2,"cache_creation":{"ephemeral_5m_input_tokens":1,"ephemeral_1h_input_tokens":3}})
}
fn event(v: Value) -> Event {
    Event {
        event: Some(v["type"].as_str().unwrap().into()),
        data: v.to_string(),
    }
}
fn start(u: Value) -> Event {
    let mut r = response(u);
    r["content"] = json!([]);
    r["stop_reason"] = Value::Null;
    event(json!({"type":"message_start","message":r}))
}
fn end(u: Value) -> Event {
    event(
        json!({"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":u}),
    )
}
#[test]
fn cache_categories_round_trip_without_counting_ttl_twice() {
    let r = anthropic::decode_chat_response(response(usage())).unwrap();
    let u = r.usage.as_ref().unwrap();
    assert_eq!(
        (u.prompt_tokens, u.completion_tokens, u.total_tokens),
        (12, 2, 14)
    );
    assert_eq!(
        u.prompt_tokens_details.as_ref().unwrap().cached_tokens,
        Some(5)
    );
    assert_eq!(
        serde_json::to_value(u).unwrap()["cache_creation"],
        json!({"input_tokens":4,"ephemeral_5m_input_tokens":1,"ephemeral_1h_input_tokens":3})
    );
    assert_eq!(
        anthropic::encode_chat_response(&r).unwrap()["usage"],
        usage()
    );
    assert!(openai::encode_chat_response(&r).is_err());
    assert!(openai::responses::encode_chat_response(&r).is_err());
    assert!(gemini::encode_chat_response(&r).is_err());
}
#[test]
fn cache_reads_are_portable_and_zero_creation_is_normalized() {
    let r=anthropic::decode_chat_response(response(json!({"input_tokens":3,"output_tokens":2,"cache_read_input_tokens":5,"cache_creation_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0}}))).unwrap();
    assert_eq!(r.usage.as_ref().unwrap().total_tokens, 10);
    assert_eq!(
        openai::encode_chat_response(&r).unwrap()["usage"]["prompt_tokens_details"]["cached_tokens"],
        5
    );
    assert_eq!(
        openai::responses::encode_chat_response(&r).unwrap()["usage"]["input_tokens"],
        8
    );
    assert_eq!(
        gemini::encode_chat_response(&r).unwrap()["usageMetadata"]["cachedContentTokenCount"],
        5
    );
    assert_eq!(
        anthropic::encode_chat_response(&r).unwrap()["usage"],
        json!({"input_tokens":3,"output_tokens":2,"cache_read_input_tokens":5})
    );
}
#[test]
fn invalid_cache_counts_and_breakdowns_are_rejected() {
    for (key, value) in [
        ("input_tokens", json!(u64::MAX)),
        ("cache_read_input_tokens", json!(-1)),
        ("cache_creation_input_tokens", json!(1.5)),
        ("cache_read_input_tokens", Value::Null),
        ("cache_creation", Value::Null),
        (
            "cache_creation",
            json!({"ephemeral_5m_input_tokens":2,"ephemeral_1h_input_tokens":3}),
        ),
        ("cache_creation", json!({"ephemeral_5m_input_tokens":4})),
        (
            "cache_creation",
            json!({"ephemeral_5m_input_tokens":u64::MAX,"ephemeral_1h_input_tokens":1}),
        ),
    ] {
        let mut u = usage();
        u[key] = value;
        assert!(
            anthropic::decode_chat_response(response(u)).is_err(),
            "{key}"
        );
    }
}
#[test]
fn streamed_cache_usage_retains_omitted_counters_and_round_trips() {
    let mut d = anthropic::StreamDecoder::new();
    let mut u = usage();
    u["output_tokens"] = json!(0);
    let mut events = d.push(&start(u)).unwrap();
    events.extend(d.push(&end(json!({"output_tokens":2}))).unwrap());
    events.extend(d.push(&event(json!({"type":"message_stop"}))).unwrap());
    let mut e = anthropic::StreamEncoder::new("m".into());
    let mut output = String::new();
    for event in &events {
        output.push_str(&e.push(event).unwrap());
    }
    let frames: Vec<Value> = output
        .lines()
        .filter_map(|l| l.strip_prefix("data: "))
        .map(|l| serde_json::from_str(l).unwrap())
        .collect();
    let final_usage = &frames
        .iter()
        .find(|v| v["type"] == "message_delta")
        .unwrap()["usage"];
    assert_eq!(final_usage, &usage());
    let initial = &frames[0]["message"]["usage"];
    assert_eq!(initial["input_tokens"], 3);
    assert_eq!(initial["cache_creation_input_tokens"], 4);
    assert_eq!(frames.last().unwrap()["type"], "message_stop");
}
#[test]
fn streamed_cache_components_cannot_regress_even_if_total_grows() {
    for delta in [
        json!({"output_tokens":20,"input_tokens":2}),
        json!({"output_tokens":20,"cache_read_input_tokens":4}),
        json!({"output_tokens":20,"cache_creation_input_tokens":3}),
        json!({"output_tokens":20,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":4}}),
    ] {
        let mut d = anthropic::StreamDecoder::new();
        let mut u = usage();
        u["output_tokens"] = json!(0);
        d.push(&start(u)).unwrap();
        assert!(d.push(&end(delta.clone())).is_err(), "{delta}");
        assert!(d.push(&event(json!({"type":"message_stop"}))).is_err());
    }
}
