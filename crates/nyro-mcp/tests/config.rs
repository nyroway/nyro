use nyro_mcp::config::Config;
use serde_json::{Value, json};

fn input() -> Value {
    json!({"servers": {"knowledge": {
        "transport": "http", "url": "https://tools.example.test/mcp",
        "bearer_token": "upstream-secret", "subjects": ["client-a"],
        "allowed_tools": ["search_documents"]
    }}})
}
fn invalid(value: Value) {
    if let Ok(config) = serde_json::from_value::<Config>(value.clone()) {
        assert!(config.validate().is_err(), "accepted {value}");
    }
}
#[test]
fn rejects_invalid_config_and_hides_credentials() {
    let parsed: Config = serde_json::from_value(input()).unwrap();
    parsed.validate().unwrap();
    assert!(!format!("{parsed:?}").contains("upstream-secret"));
    for patch in [
        json!({"transport":"stdio"}),
        json!({"transport":null}),
        json!({"url":"https://user:secret@example.test/mcp"}),
        json!({"url":"https://example.test/mcp?key=secret"}),
        json!({"url":"https://example.test/mcp#fragment"}),
        json!({"url":" https://example.test/mcp"}),
        json!({"url":"file:///tmp/a"}),
        json!({"url":"https://@example.test/mcp"}),
        json!({"subjects":[]}),
        json!({"subjects":["a","a"]}),
        json!({"allowed_tools":[]}),
        json!({"allowed_tools":["a","a"]}),
        json!({"allowed_tools":["*"]}),
        json!({"allowed_tools":[""]}),
        json!({"bearer_token":"bad\r\nheader"}),
        json!({"bearer_token":""}),
        json!({"bearer_token":null}),
        json!({"unexpected":true}),
    ] {
        let mut value = input();
        for (key, replacement) in patch.as_object().unwrap() {
            value["servers"]["knowledge"][key] = replacement.clone();
        }
        invalid(value);
    }
}
#[test]
fn rejects_invalid_limits_and_server_ids() {
    for (field, value) in [
        ("servers", Value::Null),
        ("request_timeout_ms", json!(0)),
        ("max_body_bytes", json!(0)),
        ("max_response_bytes", json!(0)),
        ("max_frame_bytes", json!(0)),
        ("unexpected", json!(true)),
    ] {
        let mut raw = input();
        raw[field] = value;
        invalid(raw);
    }
    for id in ["", "a/b", "%61", ".", "中文", &"a".repeat(65)] {
        let mut raw = input();
        let server = raw["servers"]["knowledge"].take();
        raw["servers"] = json!({id:server});
        invalid(raw);
    }
}
#[test]
fn canonicalizes_sets_without_losing_fields() {
    let mut raw = input();
    raw["servers"]["knowledge"]["subjects"] = json!(["z", "a"]);
    raw["servers"]["knowledge"]["allowed_tools"] = json!(["z", "a"]);
    let mut config: Config = serde_json::from_value(raw).unwrap();
    config.canonicalize();
    assert_eq!(config.servers["knowledge"].subjects, ["a", "z"]);
    assert_eq!(config.servers["knowledge"].allowed_tools, ["a", "z"]);
    assert_eq!(config.request_timeout_ms, 30000);
}
#[test]
fn sdk_has_the_selected_wire_revision() {
    assert_eq!(
        rmcp::model::ProtocolVersion::V_2026_07_28.as_str(),
        "2026-07-28"
    );
}

#[test]
fn empty_servers_are_valid_but_limits_still_apply() {
    for value in [json!({}), json!({"servers": {}})] {
        let config: Config = serde_json::from_value(value).unwrap();
        config.validate().unwrap();
    }
    invalid(json!({"servers": {}, "request_timeout_ms": 0}));
}
