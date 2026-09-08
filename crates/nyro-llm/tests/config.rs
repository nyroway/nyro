use nyro_llm::{
    Workload,
    config::{Backend, Config, Model, Provider, ProviderKind},
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
                max_attempts: 1,
                health: None,
                backends: vec![Backend {
                    id: "default".into(),
                    provider: "openai".into(),
                    upstream_model: "gpt-example".into(),
                    weight: 100,
                    priority: 0,
                }],
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
    config.models.get_mut("chat").unwrap().backends[0].provider = "missing".into();
    assert!(config.validate().is_err());

    let mut config = valid_config();
    config.models.get_mut("chat").unwrap().backends[0]
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

#[test]
fn canonical_backends_and_legacy_models_share_one_serialized_contract() {
    let legacy: Model = serde_json::from_value(serde_json::json!({
        "provider": "p", "upstream_model": "upstream", "workloads": ["chat"]
    }))
    .unwrap();
    let canonical: Model = serde_json::from_value(serde_json::json!({
        "backends": [{"id": "default", "provider": "p", "upstream_model": "upstream"}],
        "workloads": ["chat"]
    }))
    .expect("canonical backends must deserialize");
    let serialized = serde_json::to_value(canonical).unwrap();
    assert_eq!(serialized, serde_json::to_value(legacy).unwrap());
    assert_eq!(serialized["backends"][0]["weight"], 100);
    assert!(serialized.get("provider").is_none());
    assert!(serialized.get("upstream_model").is_none());
}

#[test]
fn routing_deserialization_rejects_missing_mixed_null_and_unknown_fields() {
    let backend = serde_json::json!({"id": "b", "provider": "p", "upstream_model": "u"});
    for routing in [
        serde_json::json!({}),
        serde_json::json!({"provider": "p"}),
        serde_json::json!({"upstream_model": "u"}),
        serde_json::json!({"provider": null, "upstream_model": "u"}),
        serde_json::json!({"provider": "p", "upstream_model": null}),
        serde_json::json!({"backends": null}),
        serde_json::json!({"backends": [backend.clone()], "provider": "p"}),
        serde_json::json!({"backends": [backend.clone()], "upstream_model": "u"}),
        serde_json::json!({"backends": [backend.clone()], "provider": null}),
        serde_json::json!({"backends": [backend.clone()], "upstream_model": null}),
        serde_json::json!({"backends": null, "provider": "p", "upstream_model": "u"}),
        serde_json::json!({"backends": [backend], "priority": 1}),
    ] {
        let mut value = routing;
        value["workloads"] = serde_json::json!(["chat"]);
        assert!(
            serde_json::from_value::<Model>(value.clone()).is_err(),
            "{value}"
        );
    }
    for field in ["weight", "priority"] {
        for value in [
            serde_json::json!(-1),
            serde_json::json!(4294967296u64),
            serde_json::Value::Null,
        ] {
            let mut backend =
                serde_json::json!({"id": "b", "provider": "p", "upstream_model": "u"});
            backend[field] = value;
            assert!(
                serde_json::from_value::<Model>(serde_json::json!({
                    "backends": [backend], "workloads": ["chat"]
                }))
                .is_err()
            );
        }
    }
}

#[test]
fn backend_validation_rejects_empty_duplicate_disabled_and_unknown_targets() {
    let backend = serde_json::json!({"id": "primary", "provider": "openai", "upstream_model": "u", "weight": 100});
    for backends in [
        serde_json::json!([]),
        serde_json::json!([backend.clone(), backend.clone()]),
        serde_json::json!([{ "id": " ", "provider": "openai", "upstream_model": "u" }]),
        serde_json::json!([{ "id": "b", "provider": "openai", "upstream_model": " " }]),
        serde_json::json!([{ "id": "b", "provider": "missing", "upstream_model": "u" }]),
        serde_json::json!([{ "id": "b", "provider": "openai", "upstream_model": "u", "weight": 0 }]),
        serde_json::json!([backend.clone(), { "id": "disabled", "provider": "missing", "upstream_model": "u", "weight": 0 }]),
        serde_json::json!([backend, { "id": "disabled", "provider": "openai", "upstream_model": " ", "weight": 0 }]),
    ] {
        let mut value = serde_json::to_value(valid_config()).unwrap();
        value["models"]["chat"] = serde_json::json!({"backends": backends, "workloads": ["chat"]});
        let config: Config = serde_json::from_value(value).expect("structurally valid model");
        assert!(config.validate().is_err());
    }
}

#[test]
fn disabled_backends_must_support_every_declared_workload_and_valid_model_names() {
    for (kind, api, workloads, upstream_model) in [
        (
            "anthropic",
            None,
            serde_json::json!(["chat", "embedding"]),
            "u",
        ),
        ("gemini", None, serde_json::json!(["embedding"]), "u"),
        (
            "openai",
            Some("responses"),
            serde_json::json!(["embedding"]),
            "u",
        ),
        ("gemini", None, serde_json::json!(["chat"]), "models/../bad"),
    ] {
        let mut value = serde_json::to_value(valid_config()).unwrap();
        value["providers"]["disabled"] =
            serde_json::json!({"kind": kind, "api": api, "base_url": "https://example.test"});
        value["models"]["chat"] = serde_json::json!({
            "backends": [
                {"id": "primary", "provider": "openai", "upstream_model": "u"},
                {"id": "disabled", "provider": "disabled", "upstream_model": upstream_model, "weight": 0}
            ], "workloads": workloads
        });
        let config: Config = serde_json::from_value(value).unwrap();
        assert!(config.validate().is_err(), "{kind} {api:?}");
    }
}

#[test]
fn backend_ids_are_model_scoped_and_large_weight_totals_are_valid() {
    let mut value = serde_json::to_value(valid_config()).unwrap();
    let model = serde_json::json!({
        "backends": [
            {"id": "primary", "provider": "openai", "upstream_model": "u", "weight": 4294967295u32},
            {"id": "secondary", "provider": "openai", "upstream_model": "v", "weight": 4294967295u32},
            {"id": "disabled", "provider": "openai", "upstream_model": "w", "weight": 0}
        ], "workloads": ["chat"]
    });
    value["models"]["chat"] = model.clone();
    value["models"]["another"] = model;
    let config: Config = serde_json::from_value(value).unwrap();
    assert!(config.validate().is_ok());
}

#[test]
fn failover_policy_defaults_preserve_legacy_models() {
    let legacy: Model = serde_json::from_value(serde_json::json!({
        "provider": "openai", "upstream_model": "u", "workloads": ["chat"]
    }))
    .unwrap();
    let canonical = serde_json::to_value(legacy).unwrap();
    assert_eq!(canonical["max_attempts"], 1);
    assert_eq!(canonical["backends"][0]["priority"], 0);
    assert!(
        canonical
            .get("health")
            .is_none_or(serde_json::Value::is_null)
    );
    let mut value = canonical;
    value["health"] = serde_json::json!({});
    let model: Model = serde_json::from_value(value).expect("health defaults must deserialize");
    let health = serde_json::to_value(model).unwrap();
    assert_eq!(health["health"]["failure_threshold"], 3);
    assert_eq!(health["health"]["cooldown_ms"], 30000);
}

#[test]
fn failover_policy_rejects_invalid_bounds_and_unknown_health_fields() {
    for (field, invalid) in [
        ("max_attempts", serde_json::json!(0)),
        ("max_attempts", serde_json::json!(-1)),
        ("max_attempts", serde_json::json!(4294967296u64)),
        ("max_attempts", serde_json::Value::Null),
        ("health", serde_json::json!({"failure_threshold": 0})),
        ("health", serde_json::json!({"cooldown_ms": 0})),
        ("health", serde_json::json!({"failure_threshold": -1})),
        ("health", serde_json::json!({"cooldown_ms": -1})),
        ("health", serde_json::json!({"failure_threshold": null})),
        ("health", serde_json::json!({"cooldown_ms": null})),
        ("health", serde_json::json!({"unexpected": true})),
    ] {
        let mut value = serde_json::to_value(valid_config()).unwrap();
        value["models"]["chat"][field] = invalid;
        if let Ok(config) = serde_json::from_value::<Config>(value.clone()) {
            assert!(config.validate().is_err(), "{value}");
        }
    }
    let mut value = serde_json::to_value(valid_config()).unwrap();
    value["models"]["chat"]["max_attempts"] = serde_json::json!(4294967295u32);
    value["models"]["chat"]["health"] =
        serde_json::json!({"failure_threshold": 4294967295u32, "cooldown_ms": 1});
    value["models"]["chat"]["backends"][0]["priority"] = serde_json::json!(4294967295u32);
    let config: Config = serde_json::from_value(value).expect("valid positive bounds");
    assert!(config.validate().is_ok());
}

#[test]
fn health_cooldown_must_fit_the_platform_timer() {
    use std::time::{Duration, Instant};
    let mut config = valid_config();
    config.models.get_mut("chat").unwrap().health = Some(nyro_llm::config::HealthConfig {
        failure_threshold: 1,
        cooldown_ms: u64::MAX,
    });
    // Instant ranges differ across platforms; reject the largest duration wherever
    // the platform cannot schedule it, while accepting it when it is representable.
    if Instant::now()
        .checked_add(Duration::from_millis(u64::MAX))
        .is_none()
    {
        assert!(config.validate().is_err());
    } else {
        assert!(config.validate().is_ok());
    }
}
