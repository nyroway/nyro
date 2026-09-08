use nyro_llm::{
    Workload,
    config::{Config, Model, Provider, ProviderKind},
};
use std::collections::{BTreeMap, BTreeSet};

fn valid_config() -> Config {
    Config {
        providers: BTreeMap::from([(
            "openai".into(),
            Provider {
                kind: ProviderKind::Openai,
                api: None,
                base_url: "https://api.example.test/v1".into(),
                api_key: Some("provider-secret".into()),
            },
        )]),
        models: BTreeMap::from([(
            "chat".into(),
            Model {
                provider: "openai".into(),
                upstream_model: "gpt-example".into(),
                workloads: vec![Workload::Chat],
                allow_anonymous: false,
                subjects: BTreeSet::from(["deploy".into()]),
            },
        )]),
    }
}

#[test]
fn serde_requires_known_provider_kind_and_rejects_unknown_fields() {
    let missing_kind = r#"{"providers":{"p":{"base_url":"https://example.test"}},"models":{}}"#;
    let unknown_kind =
        r#"{"providers":{"p":{"kind":"other","base_url":"https://example.test"}},"models":{}}"#;
    let unknown_field = r#"{"providers":{"p":{"kind":"openai","base_url":"https://example.test","extra":true}},"models":{}}"#;

    assert!(serde_json::from_str::<Config>(missing_kind).is_err());
    assert!(serde_json::from_str::<Config>(unknown_kind).is_err());
    assert!(serde_json::from_str::<Config>(unknown_field).is_err());
}

#[test]
fn serde_applies_only_the_declared_policy_defaults() {
    let parsed: Config = serde_json::from_str(
        r#"{
            "providers":{"p":{"kind":"openai","base_url":"https://example.test"}},
            "models":{"m":{"provider":"p","upstream_model":"upstream","workloads":["chat"]}}
        }"#,
    )
    .unwrap();

    assert_eq!(parsed.providers["p"].api_key, None);
    assert!(!parsed.models["m"].allow_anonymous);
    assert!(parsed.models["m"].subjects.is_empty());
}

#[test]
fn validation_rejects_empty_names_workloads_duplicates_and_missing_references() {
    assert!(valid_config().validate().is_ok());

    let mut config = valid_config();
    config.providers.clear();
    assert!(config.validate().is_err());

    let mut config = valid_config();
    config.models.clear();
    assert!(config.validate().is_err());

    let mut config = valid_config();
    let provider = config.providers.remove("openai").unwrap();
    config.providers.insert("".into(), provider);
    assert!(config.validate().is_err());

    let mut config = valid_config();
    let model = config.models.remove("chat").unwrap();
    config.models.insert("".into(), model);
    assert!(config.validate().is_err());

    let mut config = valid_config();
    config.models.get_mut("chat").unwrap().provider = "missing".into();
    assert!(config.validate().is_err());

    let mut config = valid_config();
    config
        .models
        .get_mut("chat")
        .unwrap()
        .upstream_model
        .clear();
    assert!(config.validate().is_err());

    let mut config = valid_config();
    config.models.get_mut("chat").unwrap().workloads.clear();
    assert!(config.validate().is_err());

    let mut config = valid_config();
    config.models.get_mut("chat").unwrap().workloads = vec![Workload::Chat, Workload::Chat];
    assert!(config.validate().is_err());
}

#[test]
fn validation_accepts_only_safe_http_endpoints_and_header_values() {
    for endpoint in [
        "",
        "ftp://example.test",
        "https://",
        "https://@example.test/v1",
        "https://user:url-secret@example.test/v1",
        "https://example.test/v1?token=url-secret",
        "https://example.test/v1#url-secret",
    ] {
        let mut config = valid_config();
        config.providers.get_mut("openai").unwrap().base_url = endpoint.into();
        let error = config.validate().unwrap_err().to_string();
        assert!(!error.contains("url-secret"));
    }

    let mut config = valid_config();
    config.providers.get_mut("openai").unwrap().api_key = Some("bad\nsecret".into());
    let error = config.validate().unwrap_err().to_string();
    assert!(!error.contains("bad\nsecret"));
}

#[test]
fn debug_never_exposes_provider_credentials() {
    let mut config = valid_config();
    config.providers.get_mut("openai").unwrap().base_url =
        "https://user:url-secret@example.test/v1".into();

    let rendered = format!("{config:?}");
    assert!(!rendered.contains("provider-secret"));
    assert!(!rendered.contains("url-secret"));
}

#[test]
fn api_selector_is_openai_only_and_responses_is_chat_only() {
    for api in ["chat_completions", "responses"] {
        let mut value = serde_json::to_value(valid_config()).unwrap();
        value["providers"]["openai"]["api"] = api.into();
        let config: Config = serde_json::from_value(value.clone()).unwrap();
        assert!(config.validate().is_ok());
        for kind in ["anthropic", "gemini"] {
            value["providers"]["openai"]["kind"] = kind.into();
            let config: Config = serde_json::from_value(value.clone()).unwrap();
            assert!(config.validate().is_err());
        }
    }
    let mut value = serde_json::to_value(valid_config()).unwrap();
    value["providers"]["openai"]["api"] = "responses".into();
    value["models"]["chat"]["workloads"] = serde_json::json!(["embedding"]);
    let config: Config = serde_json::from_value(value.clone()).unwrap();
    assert!(config.validate().is_err());
    value["providers"]["openai"]["api"] = "chat_completions".into();
    let config: Config = serde_json::from_value(value.clone()).unwrap();
    assert!(config.validate().is_ok());
    value["providers"]["openai"]["api"] = "unknown".into();
    assert!(serde_json::from_value::<Config>(value).is_err());
}
