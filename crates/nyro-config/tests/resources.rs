use nyro_config::resources::Resources;

const CONFIG: &str = r#"
version: 1
upstreams:
  - id: chat-pool
    kind: llm
    targets:
      - id: primary
        protocol: openai/chat-completions
        base_url: https://example.test/v1
        model: actual
        weight: ${WEIGHT}
        auth:
          type: key-auth
          in: header
          name: Authorization
          prefix: Bearer
          secret: ${SECRET}
models:
  - id: chat-main
    upstream: chat-pool
    capability: chat
    access: {mode: restricted}
    execution: {request_timeout: '${TIMEOUT}'}
    limits: {request: [{limit: 10, window: 1m}]}
consumers:
  - id: app
    credentials: [{id: first, type: key-auth, secret: '${KEY}'}]
    grants: {models: [chat-main]}
"#;

#[test]
fn typed_environment_replacement_is_nonrecursive_and_cannot_inject_yaml() {
    let config = Resources::from_yaml_with(CONFIG, |name| match name {
        "WEIGHT" => Some("3".into()),
        "TIMEOUT" => Some("1.5".into()),
        "SECRET" => Some("private: value\nmodels: [] ${KEY}".into()),
        "KEY" => Some("caller-secret".into()),
        _ => None,
    });
    // Header secrets with line breaks are rejected without echoing the value.
    assert!(!config.unwrap_err().to_string().contains("private"));
    let config = Resources::from_yaml_with(CONFIG, |name| {
        Some(
            match name {
                "WEIGHT" => "3",
                "TIMEOUT" => "1.5",
                "SECRET" => "${KEY}",
                _ => "caller-secret",
            }
            .into(),
        )
    })
    .unwrap();
    assert_eq!(config.models[0].execution.request_timeout, Some(1.5));
    let json = serde_json::to_value(config).unwrap();
    assert_eq!(
        json["upstreams"][0]["targets"][0]["auth"]["secret"],
        "${KEY}"
    );
    assert_eq!(json["upstreams"][0]["balance"], "weighted-roundrobin");
}

#[test]
fn schema_rejects_old_shapes_unknown_fields_and_invalid_references() {
    for yaml in [
        "llm: {}",
        "version: 2",
        "version: 1\nenabled: true",
        "version: 1\nmodels: [{id: chat, upstream: missing, capability: chat}]",
        "version: 1\nconsumers: [{id: app, credentials: [], grants: {models: [missing]}}]",
    ] {
        assert!(Resources::from_yaml(yaml).is_err(), "{yaml}");
    }
    assert!(Resources::from_yaml("version: 1").is_ok());
    assert!(Resources::from_yaml("version: 1\nmodels: ${UNSET}").is_err());
}

#[test]
fn mcp_paths_are_derived_and_protocol_capability_must_match() {
    let yaml = r#"
version: 1
upstreams:
 - id: pool
   kind: mcp
   targets: [{id: primary, transport: streamable-http, url: 'https://example.test/mcp'}]
mcps:
 - id: tools
   upstream: pool
   allowed_tools: [search]
   access: {mode: anonymous}
"#;
    assert!(Resources::from_yaml(yaml).is_ok());
    assert!(Resources::from_yaml(&yaml.replace("id: tools", "id: ../tools")).is_err());
    assert!(Resources::from_yaml(&yaml.replace("kind: mcp", "kind: llm")).is_err());
    assert!(
        Resources::from_yaml(&yaml.replace("allowed_tools: [search]", "allowed_tools: ['*']"))
            .is_err()
    );
}

#[test]
fn compiler_shares_consumer_identity_pool_history_and_validates_unused_targets() {
    let yaml = CONFIG
        .replace("${WEIGHT}", "3")
        .replace("${TIMEOUT}", "1.5")
        .replace("${SECRET}", "upstream-secret")
        .replace("${KEY}", "caller-secret");
    let mut resources = Resources::from_yaml(&yaml).unwrap();
    resources.consumers[0]
        .credentials
        .push(nyro_config::resources::Credential {
            id: "next".into(),
            kind: nyro_config::resources::CredentialKind::KeyAuth,
            secret: "rotated-secret".into(),
        });
    let snapshot = nyro_config::compile::Snapshot::file(resources);
    let compiled = snapshot
        .compile(&Default::default(), Default::default())
        .unwrap();
    assert_eq!(
        compiled.keys.authenticate("caller-secret").unwrap(),
        compiled.keys.authenticate("rotated-secret").unwrap()
    );
    assert_eq!(
        compiled.llm_policies.routing_scopes["chat-main"],
        "upstreams:chat-pool"
    );
    assert_eq!(
        compiled.llm_policies.execution["chat-main"].request_timeout,
        std::time::Duration::from_millis(1500)
    );
}

#[test]
fn unused_targets_validate_protocol_paths_and_egress_and_expand_boolean_values() {
    let yaml = r#"
version: 1
upstreams:
 - id: unused
   kind: llm
   targets:
    - id: primary
      protocol: gemini/generate-content
      base_url: https://example.test/v1beta
      model: gemini-model
      egress: {http1_only: '${HTTP1}'}
"#;
    let parsed = Resources::from_yaml_with(yaml, |_| Some("true".into())).unwrap();
    assert!(parsed.upstreams[0].targets[0].egress.http1_only);
    for invalid in [
        yaml.replace("gemini-model", "../invalid"),
        yaml.replace(
            "https://example.test/v1beta",
            "https://example.test/v1beta?key=secret",
        ),
        yaml.replace("http1_only: '${HTTP1}'", "proxy_url: 'ftp://example.test/'"),
    ] {
        assert!(Resources::from_yaml_with(&invalid, |_| Some("true".into())).is_err());
    }
    assert!(Resources::from_yaml_with(yaml, |_| Some("true".repeat(400_000))).is_err());
    let escaped =
        Resources::from_yaml_with("version: 1\nconsumers: [{id: '$${NAME}'}]", |_| None).unwrap();
    assert_eq!(escaped.consumers[0].id, "${NAME}");
}
