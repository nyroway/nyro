use nyro_llm::{
    codec::openai::responses::StreamDecoder,
    ir::{ChatChunk, ChatEvent, PositionedDelta, StreamChoice},
};
use nyro_protocol::framing::Event;
use serde_json::{Value, json};

fn event(sequence: usize, kind: &str, mut value: Value) -> Event {
    value["type"] = json!(kind);
    value["sequence_number"] = json!(sequence);
    Event {
        event: Some(kind.into()),
        data: value.to_string(),
    }
}

#[test]
fn incremental_function_start_accounts_for_expanded_lifecycle_chunks() {
    let created = event(
        0,
        "response.created",
        json!({"response":{
            "id":"r","object":"response","created_at":0,"model":"m","status":"in_progress","output":[],"error":null,"incomplete_details":null,"usage":null
        }}),
    );
    let added = event(
        1,
        "response.output_item.added",
        json!({"output_index":0,"item":{
            "type":"function_call","id":"f","call_id":"c","name":"f","arguments":"","status":"in_progress"
        }}),
    );
    let mut decoder = StreamDecoder::new();
    decoder.push(&created).unwrap();
    let expanded = decoder.push(&added).unwrap();
    let minimum_heap: usize = expanded
        .iter()
        .map(|event| match event {
            ChatEvent::Chunk(chunk) => {
                std::mem::size_of::<ChatChunk>()
                    + chunk.choices.len() * std::mem::size_of::<StreamChoice>()
                    + chunk
                        .choices
                        .iter()
                        .map(|choice| {
                            choice.delta.events.len() * std::mem::size_of::<PositionedDelta>()
                        })
                        .sum::<usize>()
            }
            ChatEvent::Done => 0,
        })
        .sum();
    assert!(
        minimum_heap > 1024,
        "fixture needs >1024 bytes, got {minimum_heap}"
    );
    let mut decoder = StreamDecoder::with_limit(1024);
    decoder.push(&created).unwrap();
    assert!(
        decoder.push(&added).is_err(),
        "function start expanded to at least {minimum_heap} heap bytes past the 1024-byte limit"
    );

    let tight_limit = minimum_heap + 3 * std::mem::size_of::<ChatEvent>() + 9;
    let mut decoder = StreamDecoder::with_limit(tight_limit);
    decoder.push(&created).unwrap();
    if let Ok(events) = decoder.push(&added) {
        let actual_minimum = minimum_heap
            + events.capacity() * std::mem::size_of::<ChatEvent>()
            + events
                .iter()
                .map(|event| match event {
                    ChatEvent::Chunk(chunk) => {
                        chunk.id.len() + chunk.model.len() + chunk.object.len()
                    }
                    ChatEvent::Done => 0,
                })
                .sum::<usize>()
            + 3;
        assert!(
            actual_minimum <= tight_limit,
            "tight {tight_limit}-byte budget returned at least {actual_minimum} heap bytes"
        );
    }
}
