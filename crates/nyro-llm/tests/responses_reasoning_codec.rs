use nyro_llm::codec::{
    anthropic, gemini,
    openai::{self, responses as r},
};
use nyro_llm::ir::ChatEvent;
use nyro_protocol::framing::{Decoder, Event};
use serde_json::{Value, json};

fn reasoning() -> Value {
    json!({"type":"reasoning","id":"rs_original","summary":[{"type":"summary_text","text":"A summary"},{"type":"summary_text","text":""}],"encrypted_content":"opaque-final"})
}
fn response(output: Value) -> Value {
    json!({"id":"resp_test","object":"response","created_at":1,"model":"m","status":"completed","output":output,"usage":{"input_tokens":3,"output_tokens":7,"total_tokens":10,"output_tokens_details":{"reasoning_tokens":5}}})
}
fn message() -> Value {
    json!({"type":"message","id":"msg_test","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Answer","annotations":[],"logprobs":[]}]})
}
fn event(n: usize, kind: &str, mut fields: Value) -> Event {
    fields["type"] = json!(kind);
    fields["sequence_number"] = json!(n);
    Event {
        event: Some(kind.into()),
        data: fields.to_string(),
    }
}
#[test]
fn reasoning_config_and_history_round_trip() {
    let config = json!({"effort":"max","summary":"auto","context":"all_turns","mode":"pro"});
    let body = json!({"model":"m","reasoning":config,"include":["reasoning.encrypted_content"],"input":[{"role":"user","content":"Hi"},reasoning(),{"type":"function_call","call_id":"call_1","name":"f","arguments":"{}"},{"type":"function_call_output","call_id":"call_1","output":"ok"},reasoning()]});
    let request = r::decode_chat(body).unwrap();
    let encoded = r::encode_chat(&request).unwrap();
    assert_eq!(encoded["reasoning"], config);
    assert_eq!(encoded["include"], json!(["reasoning.encrypted_content"]));
    assert_eq!(encoded["input"][1], reasoning());
    assert_eq!(encoded["input"][4], reasoning());
    assert_eq!(
        r::encode_chat(&r::decode_chat(encoded.clone()).unwrap()).unwrap(),
        encoded
    );
    assert!(openai::encode_chat(&request).is_err());
    assert!(anthropic::encode_chat(&request).is_err());
    assert!(gemini::encode_chat(&request).is_err());
}
#[test]
fn reasoning_json_preserves_items_and_usage() {
    let decoded = r::decode_chat_response(response(json!([reasoning(), message()]))).unwrap();
    let encoded = r::encode_chat_response(&decoded).unwrap();
    assert_eq!(encoded["output"][0], reasoning());
    assert_eq!(encoded["output"][1]["content"][0]["text"], "Answer");
    assert_eq!(
        encoded["usage"]["output_tokens_details"]["reasoning_tokens"],
        5
    );
    assert_eq!(encoded["usage"]["total_tokens"], 10);
    assert!(openai::encode_chat_response(&decoded).is_err());
    assert!(anthropic::encode_chat_response(&decoded).is_err());
    assert!(gemini::encode_chat_response(&decoded).is_err());
}
fn frames() -> Vec<Event> {
    let mut initial = response(json!([]));
    initial["status"] = json!("in_progress");
    initial["usage"] = Value::Null;
    let start = json!({"type":"reasoning","id":"rs_original","summary":[],"encrypted_content":"opaque-partial"});
    let mut frames = vec![
        ("response.created", json!({"response":initial})),
        (
            "response.output_item.added",
            json!({"output_index":0,"item":start}),
        ),
    ];
    for (index, text) in ["A summary", ""].into_iter().enumerate() {
        frames.extend([
            ("response.reasoning_summary_part.added",json!({"output_index":0,"item_id":"rs_original","summary_index":index,"part":{"type":"summary_text","text":""}})),
            ("response.reasoning_summary_text.delta",json!({"output_index":0,"item_id":"rs_original","summary_index":index,"delta":text})),
            ("response.reasoning_summary_text.done",json!({"output_index":0,"item_id":"rs_original","summary_index":index,"text":text})),
            ("response.reasoning_summary_part.done",json!({"output_index":0,"item_id":"rs_original","summary_index":index,"part":{"type":"summary_text","text":text}})),
        ]);
    }
    frames.extend([
        (
            "response.output_item.done",
            json!({"output_index":0,"item":reasoning()}),
        ),
        (
            "response.completed",
            json!({"response":response(json!([reasoning()]))}),
        ),
    ]);
    frames
        .into_iter()
        .enumerate()
        .map(|(i, (k, v))| event(i, k, v))
        .collect()
}
#[test]
fn reasoning_stream_preserves_summary_boundaries_and_final_ciphertext() {
    let mut decoder = r::StreamDecoder::new();
    let mut encoder = r::StreamEncoder::new("public".into());
    let mut wire = String::new();
    for frame in frames() {
        for event in decoder.push(&frame).unwrap() {
            wire.push_str(&encoder.push(&event).unwrap());
        }
    }
    decoder.finish().unwrap();
    let mut framing = Decoder::new(1024 * 1024);
    let events = framing.push(wire.as_bytes()).unwrap();
    let terminal: Value = serde_json::from_str(&events.last().unwrap().data).unwrap();
    assert_eq!(terminal["response"]["output"][0], reasoning());
    assert_eq!(terminal["response"]["model"], "public");
    assert_eq!(terminal["response"]["usage"]["total_tokens"], 10);
    let mut second = r::StreamDecoder::new();
    for event in events {
        second.push(&event).unwrap();
    }
    second.finish().unwrap();
}
#[test]
fn terminal_only_reasoning_has_known_usage_before_conversion_can_fail() {
    let mut decoder = r::StreamDecoder::new();
    let events = decoder
        .push(&event(
            0,
            "response.completed",
            json!({"response":response(json!([reasoning()]))}),
        ))
        .unwrap();
    let first = events
        .iter()
        .find_map(|e| {
            if let ChatEvent::Chunk(c) = e {
                Some(c)
            } else {
                None
            }
        })
        .unwrap();
    assert_eq!(first.usage.as_ref().unwrap().total_tokens, 10);
}

#[test]
fn config_omission_and_official_values_are_preserved() {
    for config in [
        json!({}),
        json!({"effort":"none"}),
        json!({"effort":"minimal"}),
        json!({"effort":"low"}),
        json!({"effort":"medium"}),
        json!({"effort":"high"}),
        json!({"effort":"xhigh"}),
        json!({"summary":"concise","generate_summary":"detailed","context":"current_turn","mode":"standard"}),
        json!({"mode":"future-upstream-mode","context":"auto"}),
    ] {
        let ir = r::decode_chat(json!({"model":"m","input":"Hi","reasoning":config})).unwrap();
        assert_eq!(r::encode_chat(&ir).unwrap()["reasoning"], config);
    }
    let encoded =
        r::encode_chat(&r::decode_chat(json!({"model":"m","input":"Hi"})).unwrap()).unwrap();
    assert!(encoded.get("reasoning").is_none());
    assert!(encoded.get("include").is_none());
    for config in [
        json!({"effort":"ultra"}),
        json!({"summary":true}),
        json!({"context":"last"}),
        json!({"mode":4}),
        json!({"budget":1024}),
    ] {
        assert!(r::decode_chat(json!({"model":"m","input":"Hi","reasoning":config})).is_err());
    }
    assert!(
        r::decode_chat(
            json!({"model":"m","input":"Hi","include":["message.output_text.logprobs"]})
        )
        .is_err()
    );
}
#[test]
fn invalid_reasoning_items_and_unsupported_order_are_rejected() {
    for (key, value) in [
        ("id", json!("")),
        ("summary", json!([{"type":"text","text":"bad"}])),
        ("summary", Value::Null),
        ("encrypted_content", json!({})),
        ("content", json!([{"type":"reasoning_text","text":"raw"}])),
        ("status", json!("in_progress")),
        ("vendor", json!(true)),
    ] {
        let mut item = reasoning();
        item[key] = value;
        assert!(
            r::decode_chat(json!({"model":"m","input":[item.clone()]})).is_err(),
            "{item}"
        );
        assert!(
            r::decode_chat_response(response(json!([item.clone()]))).is_err(),
            "{item}"
        );
    }
    assert!(r::decode_chat_response(response(json!([message(), reasoning()]))).is_err());
    assert!(r::decode_chat_response(response(json!([reasoning(), reasoning()]))).is_err());
    let mut ir = r::decode_chat(json!({"model":"m","input":[reasoning()]})).unwrap();
    ir.messages[0].role = nyro_llm::ir::Role::User;
    assert!(r::encode_chat(&ir).is_err());
}
#[test]
fn malformed_stream_identity_order_snapshots_and_truncation_fail_closed() {
    // Every event has an independent required lifecycle transition.
    let valid = frames();
    for skip in (0..valid.len()).filter(|i| *i != 7) {
        // An empty delta is optional.
        // Terminal-only is explicitly supported, but this always leaves earlier items.
        let mut d = r::StreamDecoder::new();
        let mut rejected = false;
        for (i, frame) in valid
            .iter()
            .enumerate()
            .filter(|(i, _)| *i != skip)
            .enumerate()
        {
            let mut v: Value = serde_json::from_str(&frame.1.data).unwrap();
            v["sequence_number"] = json!(i);
            if d.push(&Event {
                event: frame.1.event.clone(),
                data: v.to_string(),
            })
            .is_err()
            {
                rejected = true;
                break;
            }
        }
        assert!(rejected || d.finish().is_err(), "skip {skip}");
    }
    for (index, key, value) in [
        (2, "summary_index", json!(1)),
        (3, "item_id", json!("wrong")),
        (4, "text", json!("different")),
        (5, "status", json!("completed")),
        (
            10,
            "item",
            json!({"type":"reasoning","id":"rs_original","summary":[]}),
        ),
    ] {
        let mut bad = valid.clone();
        let mut v: Value = serde_json::from_str(&bad[index].data).unwrap();
        v[key] = value;
        bad[index].data = v.to_string();
        let mut d = r::StreamDecoder::new();
        assert!(
            bad.iter().any(|e| d.push(e).is_err()),
            "index {index} {key}"
        );
        assert!(d.push(&valid[0]).is_err());
    }
}
#[test]
fn cross_protocol_stream_encoders_reject_reasoning_before_emitting_it() {
    let mut d = r::StreamDecoder::new();
    let mut rejected = false;
    for frame in frames() {
        for e in d.push(&frame).unwrap() {
            if matches!(&e,ChatEvent::Chunk(c) if c.choices.iter().any(|c|c.delta.responses_reasoning.is_some()))
            {
                assert!(openai::encode_chat_event(&e, "m").is_err());
                assert!(anthropic::StreamEncoder::new("m".into()).push(&e).is_err());
                assert!(gemini::StreamEncoder::new("m".into()).push(&e).is_err());
                rejected = true;
            }
        }
    }
    assert!(rejected);
}
#[test]
fn reasoning_then_message_then_function_stream_retains_item_order() {
    let call = json!({"type":"function_call","id":"fc_test","call_id":"call1","name":"f","arguments":"{}","status":"completed"});
    let mut d = r::StreamDecoder::new();
    let mut e = r::StreamEncoder::new("public".into());
    let events = d
        .push(&event(
            0,
            "response.completed",
            json!({"response":response(json!([reasoning(),message(),call]))}),
        ))
        .unwrap();
    let mut output = String::new();
    for event in events {
        output.push_str(&e.push(&event).unwrap());
    }
    let mut framing = Decoder::new(1024 * 1024);
    let frames = framing.push(output.as_bytes()).unwrap();
    let terminal: Value = serde_json::from_str(&frames.last().unwrap().data).unwrap();
    assert_eq!(terminal["response"]["output"][0], reasoning());
    assert_eq!(terminal["response"]["output"][1]["type"], "message");
    assert_eq!(terminal["response"]["output"][2]["call_id"], "call1");
    let mut d = r::StreamDecoder::new();
    for frame in frames {
        d.push(&frame).unwrap();
    }
    d.finish().unwrap();
}
#[test]
fn incomplete_reasoning_summary_and_ciphertext_survive_streaming() {
    let mut frames = frames();
    let mut part: Value = serde_json::from_str(&frames[9].data).unwrap();
    part["status"] = json!("incomplete");
    frames[9].data = part.to_string();
    let mut done = reasoning();
    done["status"] = json!("incomplete");
    frames[10] = event(
        10,
        "response.output_item.done",
        json!({"output_index":0,"item":done}),
    );
    let mut terminal = response(json!([done]));
    terminal["status"] = json!("incomplete");
    terminal["incomplete_details"] = json!({"reason":"max_output_tokens"});
    frames[11] = event(11, "response.incomplete", json!({"response":terminal}));
    let mut decoder = r::StreamDecoder::new();
    let mut encoder = r::StreamEncoder::new("m".into());
    let mut text = String::new();
    for frame in frames {
        for e in decoder.push(&frame).unwrap() {
            text.push_str(&encoder.push(&e).unwrap());
        }
    }
    decoder.finish().unwrap();
    let mut framing = Decoder::new(65536);
    let frames = framing.push(text.as_bytes()).unwrap();
    assert!(frames.iter().any(|f| f.event.as_deref()
        == Some("response.reasoning_summary_part.done")
        && serde_json::from_str::<Value>(&f.data).unwrap()["status"] == "incomplete"));
    let terminal: Value = serde_json::from_str(&frames.last().unwrap().data).unwrap();
    assert_eq!(terminal["response"]["output"][0], done);
}
#[test]
fn reasoning_state_and_terminal_expansion_are_bounded() {
    let mut big = reasoning();
    big["summary"] = json!(
        (0..100)
            .map(|_| json!({"type":"summary_text","text":""}))
            .collect::<Vec<_>>()
    );
    let frame = event(
        0,
        "response.completed",
        json!({"response":response(json!([big]))}),
    );
    assert!(frame.data.len() < 8192);
    assert!(r::StreamDecoder::with_limit(8192).push(&frame).is_err());
    let mut valid: Vec<_> = frames().into_iter().take(3).collect();
    for i in 3..8 {
        valid.push(event(i,"response.reasoning_summary_text.delta",json!({"output_index":0,"item_id":"rs_original","summary_index":0,"delta":"x".repeat(300)})));
    }
    assert!(valid.iter().all(|e| e.data.len() < 1024));
    let mut decoder = r::StreamDecoder::with_limit(1024);
    assert!(valid.iter().any(|e| decoder.push(e).is_err()));
    let mut decoder = r::StreamDecoder::new();
    let mut encoder = r::StreamEncoder::with_limit("m".into(), 1024);
    let mut rejected = false;
    for frame in valid {
        for e in decoder.push(&frame).unwrap() {
            if encoder.push(&e).is_err() {
                rejected = true;
            }
        }
    }
    assert!(rejected);
}
#[test]
fn completed_json_cannot_contain_incomplete_reasoning_items() {
    let mut item = reasoning();
    item["status"] = json!("incomplete");
    let mut body = response(json!([item]));
    body["status"] = json!("incomplete");
    body["incomplete_details"] = json!({"reason":"max_output_tokens"});
    let mut ir = r::decode_chat_response(body).unwrap();
    assert!(r::encode_chat_response(&ir).is_ok());
    ir.choices[0].finish_reason = Some("stop".into());
    assert!(r::encode_chat_response(&ir).is_err());
}
