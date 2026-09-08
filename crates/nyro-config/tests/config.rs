use nyro_config::{Config, LimitConfig, SecurityConfig, ServerConfig};
use nyro_llm::{
    Workload,
    config::{Config as LlmConfig, Model, Provider, ProviderKind},
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
                    provider: "openai".into(),
                    upstream_model: "gpt-example".into(),
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
