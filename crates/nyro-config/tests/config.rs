use nyro_config::{Config, LimitConfig, SecurityConfig, ServerConfig};
use nyro_llm::{
    Workload,
    config::{Backend, Config as LlmConfig, Model, Provider, ProviderKind},
};
use nyro_security::ApiKey;
use std::{
    collections::{BTreeMap, BTreeSet},
    fs,
    net::SocketAddr,
};

fn valid_config() -> Config {
    Config {
        server: ServerConfig::default(),
        llm: LlmConfig {
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
                    rate: None,
                    quota: None,
                    backends: vec![Backend {
                        id: "default".into(),
                        provider: "openai".into(),
                        upstream_model: "gpt-example".into(),
                        weight: 100,
                        priority: 0,
                    }],
                    workloads: vec![Workload::Chat, Workload::Embedding],
                    allow_anonymous: false,
                    subjects: BTreeSet::from(["deploy".into()]),
                },
            )]),
        },
        security: SecurityConfig {
            api_keys: vec![ApiKey {
                id: "deploy".into(),
                secret: "client-secret".into(),
            }],
        },
        limit: LimitConfig::default(),
    }
}

#[test]
fn yaml_loads_valid_config_with_effective_defaults() {
    let parsed = Config::from_yaml(
        r#"
llm:
  providers:
    openai:
      kind: openai
      base_url: https://api.example.test/v1
  models:
    chat:
      provider: openai
      upstream_model: gpt-example
      workloads: [chat]
      allow_anonymous: true
"#,
    )
    .unwrap();

    assert_eq!(parsed.server.listen, "127.0.0.1:19530".parse().unwrap());
    assert_eq!(parsed.server.request_timeout_ms, 120_000);
    assert_eq!(parsed.server.max_body_bytes, 1_048_576);
    assert_eq!(parsed.server.max_response_bytes, 16_777_216);
    assert_eq!(parsed.server.max_frame_bytes, 1_048_576);
    assert_eq!(parsed.limit.concurrency, 64);
    assert!(parsed.security.api_keys.is_empty());
}

#[test]
fn yaml_structure_errors_are_safe_and_reject_unknown_fields_and_kinds() {
    for yaml in [
        r#"
llm:
  providers:
    p: { kind: openai, base_url: https://example.test, api_key: top-secret }
  models: {}
unexpected: top-secret
"#,
        r#"
llm:
  providers:
    p: { kind: top-secret-kind, base_url: https://example.test }
  models: {}
"#,
    ] {
        let rendered = Config::from_yaml(yaml).unwrap_err().to_string();
        assert!(rendered.contains("YAML"));
        assert!(!rendered.contains("top-secret"));
    }
}

#[test]
fn yaml_rejects_unknown_credential_policy_fields_without_echoing_values() {
    let error = Config::from_yaml(
        r#"
llm:
  providers:
    openai: { kind: openai, base_url: https://example.test }
  models:
    chat:
      provider: openai
      upstream_model: upstream
      workloads: [chat]
      subjects: [deploy]
security:
  api_keys:
    - id: deploy
      secret: credential-secret
      expires_at: unsupported-secret-policy
"#,
    )
    .unwrap_err()
    .to_string();

    assert!(error.contains("YAML"));
    assert!(!error.contains("credential-secret"));
    assert!(!error.contains("unsupported-secret-policy"));
}

#[test]
fn validation_reuses_credential_rules_and_enforces_model_subjects() {
    let mut config = valid_config();
    config.security.api_keys.push(ApiKey {
        id: "deploy".into(),
        secret: "other-secret".into(),
    });
    assert!(config.validate().is_err());

    let mut config = valid_config();
    config.llm.models.get_mut("chat").unwrap().subjects.clear();
    assert!(config.validate().is_err());

    let mut config = valid_config();
    config
        .llm
        .models
        .get_mut("chat")
        .unwrap()
        .subjects
        .insert("missing".into());
    assert!(config.validate().is_err());

    let mut config = valid_config();
    let model = config.llm.models.get_mut("chat").unwrap();
    model.allow_anonymous = true;
    model.subjects.clear();
    config.security.api_keys.clear();
    assert!(config.validate().is_ok());
}

#[test]
fn validation_rejects_blank_ids_and_non_bearer_secrets_with_a_generic_error() {
    for (id, secret) in [
        ("   ", "valid-secret"),
        ("deploy", "contains whitespace"),
        ("deploy", "contains\ttab"),
        ("deploy", "non-ascii-秘密"),
        ("deploy", "control-\u{7f}"),
    ] {
        let mut config = valid_config();
        config.security.api_keys[0] = ApiKey {
            id: id.into(),
            secret: secret.into(),
        };

        let error = config.validate().unwrap_err().to_string();
        assert_eq!(error, "invalid API key configuration");
        assert!(!error.contains(id));
        assert!(!error.contains(secret));
    }
}

#[test]
fn validation_rejects_zero_resource_bounds_and_invalid_concurrency() {
    let mut configs = Vec::new();
    let mut config = valid_config();
    config.server.request_timeout_ms = 0;
    configs.push(config);
    let mut config = valid_config();
    config.server.max_body_bytes = 0;
    configs.push(config);
    let mut config = valid_config();
    config.server.max_response_bytes = 0;
    configs.push(config);
    let mut config = valid_config();
    config.server.max_frame_bytes = 0;
    configs.push(config);
    let mut config = valid_config();
    config.limit.concurrency = 0;
    configs.push(config);
    let mut config = valid_config();
    config.limit.concurrency = usize::MAX;
    configs.push(config);

    assert!(configs.into_iter().all(|config| config.validate().is_err()));
}

#[test]
fn fingerprint_is_canonical_and_excludes_listen_address() {
    let mut first = valid_config();
    first.security.api_keys.push(ApiKey {
        id: "ops".into(),
        secret: "second-client-secret".into(),
    });
    let mut equivalent = first.clone();
    equivalent.server.listen = "0.0.0.0:8080".parse::<SocketAddr>().unwrap();
    equivalent.security.api_keys.reverse();
    equivalent.llm.providers.get_mut("openai").unwrap().api =
        Some(nyro_llm::config::OpenAiApi::ChatCompletions);
    equivalent
        .llm
        .models
        .get_mut("chat")
        .unwrap()
        .workloads
        .reverse();

    assert_eq!(
        first.fingerprint().unwrap(),
        equivalent.fingerprint().unwrap()
    );

    equivalent.server.max_body_bytes += 1;
    assert_ne!(
        first.fingerprint().unwrap(),
        equivalent.fingerprint().unwrap()
    );
}

#[test]
fn load_reads_plain_yaml_without_expanding_environment_syntax() {
    let directory = tempfile::tempdir().unwrap();
    let path = directory.path().join("nyro.yaml");
    fs::write(
        &path,
        r#"
llm:
  providers:
    openai:
      kind: openai
      base_url: https://api.example.test/v1
      api_key: ${NYRO_PROVIDER_KEY}
  models:
    chat:
      provider: openai
      upstream_model: gpt-example
      workloads: [chat]
      allow_anonymous: true
"#,
    )
    .unwrap();

    let parsed = Config::load(&path).unwrap();
    assert_eq!(
        parsed.llm.providers["openai"].api_key.as_deref(),
        Some("${NYRO_PROVIDER_KEY}")
    );
}

#[test]
fn debug_and_validation_errors_do_not_expose_secrets() {
    let mut config = valid_config();
    config.llm.providers.get_mut("openai").unwrap().base_url =
        "https://user:url-secret@example.test/v1".into();

    let debug = format!("{config:?}");
    let error = config.validate().unwrap_err().to_string();
    for secret in ["provider-secret", "client-secret", "url-secret"] {
        assert!(!debug.contains(secret));
        assert!(!error.contains(secret));
    }
}

fn yaml_with_routing(routing: &str) -> String {
    format!(
        "llm:\n  providers:\n    p: {{kind: openai, base_url: https://example.test}}\n    q: {{kind: openai, base_url: https://second.example.test}}\n  models:\n    chat:\n      workloads: [chat]\n      allow_anonymous: true\n{routing}\n"
    )
}

#[test]
fn yaml_legacy_and_explicit_default_backend_have_the_same_fingerprint() {
    let legacy = Config::from_yaml(&yaml_with_routing(
        "      provider: p\n      upstream_model: u",
    ))
    .unwrap();
    let canonical = Config::from_yaml(&yaml_with_routing(
        "      backends:\n        - {id: default, provider: p, upstream_model: u}",
    ))
    .expect("canonical backend YAML must load");
    assert_eq!(
        legacy.fingerprint().unwrap(),
        canonical.fingerprint().unwrap()
    );
}

#[test]
fn fingerprint_ignores_backend_order_and_retains_every_backend_setting() {
    let first = Config::from_yaml(&yaml_with_routing(
        "      backends:\n        - {id: primary, provider: p, upstream_model: u, weight: 80}\n        - {id: secondary, provider: q, upstream_model: v, weight: 0}"
    )).unwrap();
    let reordered = Config::from_yaml(&yaml_with_routing(
        "      backends:\n        - {id: secondary, provider: q, upstream_model: v, weight: 0}\n        - {id: primary, provider: p, upstream_model: u, weight: 80}"
    )).unwrap();
    assert_eq!(
        first.fingerprint().unwrap(),
        reordered.fingerprint().unwrap()
    );
    for changed_backend in [
        "{id: renamed, provider: q, upstream_model: v, weight: 0}",
        "{id: secondary, provider: p, upstream_model: v, weight: 0}",
        "{id: secondary, provider: q, upstream_model: different, weight: 0}",
        "{id: secondary, provider: q, upstream_model: v, weight: 1}",
        "{id: secondary, provider: q, upstream_model: v, weight: 0, priority: 1}",
    ] {
        let changed = Config::from_yaml(&yaml_with_routing(&format!(
            "      backends:\n        - {{id: primary, provider: p, upstream_model: u, weight: 80}}\n        - {changed_backend}"
        ))).unwrap();
        assert_ne!(first.fingerprint().unwrap(), changed.fingerprint().unwrap());
    }
}

#[test]
fn yaml_rejects_ambiguous_or_invalid_backend_routes() {
    for routing in [
        "      backends: null",
        "      backends: []",
        "      provider: p\n      upstream_model: u\n      backends: null",
        "      provider: null\n      backends: [{id: b, provider: p, upstream_model: u}]",
        "      backends: [{id: b, provider: p, upstream_model: u, weight: -1}]",
        "      backends: [{id: b, provider: p, upstream_model: u, weight: 4294967296}]",
        "      backends: [{id: b, provider: p, upstream_model: u, weight: 0}]",
        "      backends: [{id: b, provider: p, upstream_model: u}, {id: b, provider: p, upstream_model: v}]",
    ] {
        assert!(
            Config::from_yaml(&yaml_with_routing(routing)).is_err(),
            "{routing}"
        );
    }
}

#[test]
fn failover_fingerprint_normalizes_defaults_and_retains_opt_in_policy() {
    let legacy = Config::from_yaml(&yaml_with_routing(
        "      provider: p\n      upstream_model: u",
    ))
    .unwrap();
    let explicit = Config::from_yaml(&yaml_with_routing(
        "      max_attempts: 1\n      health: null\n      backends: [{id: default, provider: p, upstream_model: u, priority: 0}]",
    )).unwrap();
    assert_eq!(
        legacy.fingerprint().unwrap(),
        explicit.fingerprint().unwrap()
    );
    let health_defaults = Config::from_yaml(&yaml_with_routing(
        "      provider: p\n      upstream_model: u\n      health: {}",
    ))
    .unwrap();
    let health_explicit = Config::from_yaml(&yaml_with_routing(
        "      provider: p\n      upstream_model: u\n      health: {failure_threshold: 3, cooldown_ms: 30000}",
    )).unwrap();
    assert_eq!(
        health_defaults.fingerprint().unwrap(),
        health_explicit.fingerprint().unwrap()
    );
    assert_ne!(
        legacy.fingerprint().unwrap(),
        health_defaults.fingerprint().unwrap()
    );
    for policy in [
        "      max_attempts: 2",
        "      health: {failure_threshold: 4}",
        "      health: {cooldown_ms: 30001}",
    ] {
        let changed = Config::from_yaml(&yaml_with_routing(&format!(
            "      provider: p\n      upstream_model: u\n{policy}"
        )))
        .unwrap();
        assert_ne!(
            health_defaults.fingerprint().unwrap(),
            changed.fingerprint().unwrap()
        );
        if policy.contains("max_attempts") {
            assert_ne!(
                legacy.fingerprint().unwrap(),
                changed.fingerprint().unwrap()
            );
        }
    }
    let mut changed_priority = explicit.clone();
    changed_priority
        .llm
        .models
        .get_mut("chat")
        .unwrap()
        .backends[0]
        .priority = 1;
    assert_ne!(
        explicit.fingerprint().unwrap(),
        changed_priority.fingerprint().unwrap()
    );
}

#[test]
fn yaml_rejects_invalid_failover_policy_values() {
    for policy in [
        "max_attempts: 0",
        "max_attempts: -1",
        "max_attempts: 4294967296",
        "max_attempts: null",
        "health: {failure_threshold: 0}",
        "health: {cooldown_ms: 0}",
        "health: {failure_threshold: -1}",
        "health: {cooldown_ms: -1}",
        "health: {failure_threshold: null}",
        "health: {cooldown_ms: null}",
        "health: {unknown: true}",
    ] {
        assert!(
            Config::from_yaml(&yaml_with_routing(&format!(
                "      provider: p\n      upstream_model: u\n      {policy}"
            )))
            .is_err(),
            "{policy}"
        );
    }
}

#[test]
fn rate_fingerprint_normalizes_burst_default_and_retains_every_policy_field() {
    let parse = |rate: &str| {
        Config::from_yaml(&yaml_with_routing(&format!(
            "      provider: p\n      upstream_model: u\n{rate}"
        )))
        .unwrap()
    };
    let disabled = parse("");
    let default = parse("      rate: {requests: 10, period_ms: 60000}");
    let explicit = parse("      rate: {requests: 10, period_ms: 60000, burst: 1}");
    assert_eq!(
        default.fingerprint().unwrap(),
        explicit.fingerprint().unwrap()
    );
    assert_ne!(
        default.fingerprint().unwrap(),
        disabled.fingerprint().unwrap()
    );
    for changed in [
        "      rate: {requests: 11, period_ms: 60000}",
        "      rate: {requests: 10, period_ms: 60001}",
        "      rate: {requests: 10, period_ms: 60000, burst: 2}",
    ] {
        assert_ne!(
            default.fingerprint().unwrap(),
            parse(changed).fingerprint().unwrap()
        );
    }
}

#[test]
fn yaml_rejects_invalid_rate_policy_values() {
    for policy in [
        "null",
        "{}",
        "{requests: 1}",
        "{period_ms: 1000}",
        "{requests: 0, period_ms: 1000}",
        "{requests: -1, period_ms: 1000}",
        "{requests: 4294967296, period_ms: 1000}",
        "{requests: null, period_ms: 1000}",
        "{requests: 1, period_ms: 0}",
        "{requests: 1, period_ms: -1}",
        "{requests: 1, period_ms: 18446744073709551616}",
        "{requests: 1, period_ms: null}",
        "{requests: 1, period_ms: 1000, burst: 0}",
        "{requests: 1, period_ms: 1000, burst: -1}",
        "{requests: 1, period_ms: 1000, burst: 4294967296}",
        "{requests: 1, period_ms: 1000, burst: null}",
        "{requests: 1, period_ms: 1000, unknown: true}",
    ] {
        assert!(
            Config::from_yaml(&yaml_with_routing(&format!(
                "      provider: p\n      upstream_model: u\n      rate: {policy}"
            )))
            .is_err(),
            "{policy}"
        );
    }
}

#[test]
fn quota_fingerprint_retains_both_policy_fields_and_omission_disables_it() {
    let parse = |quota: &str| {
        Config::from_yaml(&yaml_with_routing(&format!(
            "      provider: p\n      upstream_model: u\n{quota}"
        )))
        .unwrap()
    };
    let disabled = parse("");
    let enabled = parse("      quota: {total_tokens: 100, reserve_tokens: 20}");
    assert_ne!(
        disabled.fingerprint().unwrap(),
        enabled.fingerprint().unwrap()
    );
    let reordered = parse("      quota: {reserve_tokens: 20, total_tokens: 100}");
    assert_eq!(
        enabled.fingerprint().unwrap(),
        reordered.fingerprint().unwrap()
    );
    for changed in [
        "      quota: {total_tokens: 101, reserve_tokens: 20}",
        "      quota: {total_tokens: 100, reserve_tokens: 21}",
    ] {
        assert_ne!(
            enabled.fingerprint().unwrap(),
            parse(changed).fingerprint().unwrap()
        );
    }
}

#[test]
fn yaml_rejects_invalid_quota_policy_values() {
    for policy in [
        "null",
        "{}",
        "{total_tokens: 100}",
        "{reserve_tokens: 20}",
        "{total_tokens: 0, reserve_tokens: 20}",
        "{total_tokens: 100, reserve_tokens: 0}",
        "{total_tokens: 10, reserve_tokens: 20}",
        "{total_tokens: -1, reserve_tokens: 20}",
        "{total_tokens: 100, reserve_tokens: -1}",
        "{total_tokens: null, reserve_tokens: 20}",
        "{total_tokens: 100, reserve_tokens: null}",
        "{total_tokens: 18446744073709551616, reserve_tokens: 20}",
        "{total_tokens: 100, reserve_tokens: 18446744073709551616}",
        "{total_tokens: 100, reserve_tokens: 20, unknown: true}",
    ] {
        assert!(
            Config::from_yaml(&yaml_with_routing(&format!(
                "      provider: p\n      upstream_model: u\n      quota: {policy}"
            )))
            .is_err(),
            "{policy}"
        );
    }
}
