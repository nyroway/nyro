use nyro_core::Gateway;
use nyro_core::admin::CopyProviderOptions;
use nyro_core::config::GatewayConfig;
use nyro_core::db::models::*;
use nyro_core::storage::Storage as _;
use std::path::PathBuf;

use uuid::Uuid;

const FAR_FUTURE_RFC3339: &str = "2099-01-01T00:00:00Z";
const CODEX_RUNTIME_URL: &str = "https://chatgpt.com/backend-api/codex";

#[tokio::test]
async fn copy_provider_creates_disabled_provider_with_copy_suffix() -> anyhow::Result<()> {
    let gw = build_gateway().await?;
    let original = gw
        .admin()
        .create_provider(api_key_provider_input("source-provider"))
        .await?;

    let copied = gw.admin().copy_provider(&original.id).await?;

    assert_ne!(copied.id, original.id);
    assert_eq!(copied.name, "source-provider_Copy");
    assert_eq!(copied.vendor, original.vendor);
    assert_eq!(copied.protocol, original.protocol);
    assert_eq!(copied.base_url, original.base_url);
    assert_eq!(copied.preset_key, original.preset_key);
    assert_eq!(copied.channel, original.channel);
    assert_eq!(copied.models_source, original.models_source);
    assert_eq!(copied.static_models, original.static_models);
    assert_eq!(copied.api_key, original.api_key);
    assert_eq!(copied.auth_mode, original.auth_mode);
    assert_eq!(copied.use_proxy, original.use_proxy);
    assert!(original.is_enabled);
    assert!(!copied.is_enabled);

    Ok(())
}

#[tokio::test]
async fn copy_provider_uses_numbered_suffix_when_copy_name_exists() -> anyhow::Result<()> {
    let gw = build_gateway().await?;
    let original = gw
        .admin()
        .create_provider(api_key_provider_input("source-provider"))
        .await?;
    gw.admin().copy_provider(&original.id).await?;

    let second_copy = gw.admin().copy_provider(&original.id).await?;

    assert_eq!(second_copy.name, "source-provider_Copy2");

    Ok(())
}

#[tokio::test]
async fn copy_provider_can_copy_matching_route_targets_to_copied_provider() -> anyhow::Result<()> {
    let gw = build_gateway().await?;
    let original = gw
        .admin()
        .create_provider(api_key_provider_input("route-source-provider"))
        .await?;
    let fallback = gw
        .admin()
        .create_provider(api_key_provider_input("route-fallback-provider"))
        .await?;

    let source_route = gw
        .admin()
        .create_model(CreateModel {
            name: "source-model".to_string(),
            balance: Some("priority".to_string()),
            target_provider: String::new(),
            target_model: String::new(),
            targets: vec![
                CreateModelBackend {
                    provider_id: original.id.clone(),
                    model: "source-upstream-model".to_string(),
                    weight: Some(80),
                    priority: Some(1),
                },
                CreateModelBackend {
                    provider_id: fallback.id.clone(),
                    model: "fallback-upstream-model".to_string(),
                    weight: Some(20),
                    priority: Some(2),
                },
            ],
            enable_auth: Some(true),
            enable_payload: None,
        })
        .await?;

    let copied = gw
        .admin()
        .copy_provider_with_options(
            &original.id,
            CopyProviderOptions {
                append_targets: true,
            },
        )
        .await?;

    assert!(!copied.is_enabled);
    let models = gw.admin().list_models().await?;
    assert_eq!(
        models.len(),
        1,
        "copying route targets must not create new routes"
    );
    assert!(models.iter().all(|model| model.name != "source-model_Copy"));

    let updated_model = models
        .iter()
        .find(|model| model.id == source_route.id)
        .expect("source route should remain");
    assert_eq!(updated_model.name, "source-model");
    assert_eq!(updated_model.balance, "priority");
    assert!(updated_model.enable_auth);
    assert_eq!(updated_model.target_provider, original.id);
    assert_eq!(updated_model.target_model, "source-upstream-model");
    assert_eq!(updated_model.targets.len(), 3);
    assert!(updated_model.targets.iter().any(|target| {
        target.provider_id == original.id
            && target.model == "source-upstream-model"
            && target.weight == 80
            && target.priority == 1
    }));
    assert!(updated_model.targets.iter().any(|target| {
        target.provider_id == copied.id
            && target.model == "source-upstream-model"
            && target.weight == 80
            && target.priority == 1
    }));
    assert!(updated_model.targets.iter().any(|target| {
        target.provider_id == fallback.id
            && target.model == "fallback-upstream-model"
            && target.weight == 20
            && target.priority == 2
    }));

    Ok(())
}

#[tokio::test]
async fn copy_provider_does_not_append_targets_by_default() -> anyhow::Result<()> {
    let gw = build_gateway().await?;
    let original = gw
        .admin()
        .create_provider(api_key_provider_input("no-route-copy-provider"))
        .await?;

    gw.admin()
        .create_model(CreateModel {
            name: "no-route-copy-model".to_string(),
            balance: None,
            target_provider: original.id.clone(),
            target_model: "source-upstream-model".to_string(),
            targets: vec![],
            enable_auth: None,
            enable_payload: None,
        })
        .await?;

    gw.admin().copy_provider(&original.id).await?;

    let models = gw.admin().list_models().await?;
    assert_eq!(models.len(), 1);
    assert_eq!(models[0].targets.len(), 1);
    assert_eq!(models[0].targets[0].provider_id, original.id);

    Ok(())
}

#[tokio::test]
async fn delete_provider_removes_route_associations_before_provider() -> anyhow::Result<()> {
    let gw = build_gateway().await?;
    let removed_provider = gw
        .admin()
        .create_provider(api_key_provider_input("route-delete-provider"))
        .await?;
    let kept_provider = gw
        .admin()
        .create_provider(api_key_provider_input("route-keep-provider"))
        .await?;

    let removed_route = gw
        .admin()
        .create_model(CreateModel {
            name: "route-owned-model".to_string(),
            balance: None,
            target_provider: removed_provider.id.clone(),
            target_model: "gpt-delete".to_string(),
            targets: vec![],
            enable_auth: None,
            enable_payload: None,
        })
        .await?;
    let kept_route = gw
        .admin()
        .create_model(CreateModel {
            name: "route-kept-model".to_string(),
            balance: None,
            target_provider: kept_provider.id.clone(),
            target_model: "gpt-keep".to_string(),
            targets: vec![
                CreateModelBackend {
                    provider_id: kept_provider.id.clone(),
                    model: "gpt-keep".to_string(),
                    weight: Some(100),
                    priority: Some(1),
                },
                CreateModelBackend {
                    provider_id: removed_provider.id.clone(),
                    model: "gpt-delete-secondary".to_string(),
                    weight: Some(50),
                    priority: Some(2),
                },
            ],
            enable_auth: None,
            enable_payload: None,
        })
        .await?;

    gw.admin().delete_provider(&removed_provider.id).await?;

    assert!(
        gw.admin().get_provider(&removed_provider.id).await.is_err(),
        "provider should be deleted after dependent route rows are removed"
    );

    let models = gw.admin().list_models().await?;
    assert!(
        !models.iter().any(|model| model.id == removed_route.id),
        "routes whose primary provider was deleted should be removed"
    );
    let kept_route = models
        .iter()
        .find(|model| model.id == kept_route.id)
        .expect("route with a different primary provider should remain");
    assert_eq!(kept_route.target_provider, kept_provider.id);
    assert!(
        kept_route
            .targets
            .iter()
            .all(|target| target.provider_id != removed_provider.id),
        "secondary route target associations for the deleted provider should be removed"
    );

    let model_cache = gw.model_cache.read().await;
    assert!(model_cache.match_model("route-owned-model").is_none());
    assert!(model_cache.match_model("route-kept-model").is_some());

    Ok(())
}

#[tokio::test]
async fn copy_oauth_provider_copies_credential_binding() -> anyhow::Result<()> {
    let gw = build_gateway().await?;
    let original = gw.admin().create_provider(oauth_provider_input()).await?;
    seed_oauth_credential(&gw, &original.id, "copy-access-token", "copy-refresh-token").await?;

    let copied = gw.admin().copy_provider(&original.id).await?;
    let copied_credential = gw.storage.oauth_credentials().get(&copied.id).await?;

    assert_eq!(copied.name, format!("{}_Copy", original.name));
    assert_eq!(copied.auth_mode, "oauth");
    assert!(copied.api_key.is_empty());
    assert_eq!(
        copied_credential
            .as_ref()
            .map(|cred| cred.access_token.as_str()),
        Some("copy-access-token"),
    );

    Ok(())
}

#[tokio::test]
async fn logout_provider_oauth_preserves_oauth_mode_and_disconnects_binding() -> anyhow::Result<()>
{
    let gw = build_gateway().await?;
    let provider = gw.admin().create_provider(oauth_provider_input()).await?;
    seed_oauth_credential(&gw, &provider.id, "test-access-token", "test-refresh-token").await?;

    let status = gw.admin().logout_provider_oauth(&provider.id).await?;
    assert_eq!(status.status, "disconnected");

    let updated = gw.admin().get_provider(&provider.id).await?;
    assert_eq!(updated.effective_auth_mode(), "oauth");
    assert!(updated.api_key.is_empty());
    let oauth_cred = gw.storage.oauth_credentials().get(&provider.id).await?;
    assert!(
        oauth_cred.is_none(),
        "oauth credential should be deleted after logout"
    );

    Ok(())
}

async fn build_gateway() -> anyhow::Result<Gateway> {
    let config = GatewayConfig {
        data_dir: test_data_dir(),
        ..Default::default()
    };
    let (gw, _log_rx) = Gateway::new(config).await?;
    Ok(gw)
}

// ── config epoch tests ─────────────────────────────────────────────────────

#[tokio::test]
async fn config_epoch_starts_at_zero_and_increments_on_model_create() -> anyhow::Result<()> {
    let gw = build_gateway().await?;

    let epoch_before: i64 = gw
        .storage
        .settings()
        .get("config_epoch")
        .await?
        .as_deref()
        .and_then(|v| v.parse().ok())
        .unwrap_or(0);

    let provider = gw
        .admin()
        .create_provider(api_key_provider_input("epoch-test-provider"))
        .await?;
    gw.admin()
        .create_model(CreateModel {
            name: "epoch-test-model".to_string(),
            balance: Some("weighted".to_string()),
            target_provider: provider.id.clone(),
            target_model: "gpt-4".to_string(),
            targets: vec![],
            enable_auth: Some(false),
            enable_payload: None,
        })
        .await?;

    let epoch_after: i64 = gw
        .storage
        .settings()
        .get("config_epoch")
        .await?
        .as_deref()
        .and_then(|v| v.parse().ok())
        .unwrap_or(0);

    assert!(
        epoch_after > epoch_before,
        "config_epoch should increment after create_model: before={epoch_before} after={epoch_after}"
    );
    Ok(())
}

#[tokio::test]
async fn config_epoch_increments_on_model_update_and_delete() -> anyhow::Result<()> {
    let gw = build_gateway().await?;
    let provider = gw
        .admin()
        .create_provider(api_key_provider_input("epoch-update-provider"))
        .await?;
    let model = gw
        .admin()
        .create_model(CreateModel {
            name: "epoch-update-model".to_string(),
            balance: Some("weighted".to_string()),
            target_provider: provider.id.clone(),
            target_model: "gpt-4".to_string(),
            targets: vec![],
            enable_auth: Some(false),
            enable_payload: None,
        })
        .await?;

    let epoch_before_update: i64 = gw
        .storage
        .settings()
        .get("config_epoch")
        .await?
        .as_deref()
        .and_then(|v| v.parse().ok())
        .unwrap_or(0);

    gw.admin()
        .update_model(
            &model.id,
            UpdateModel {
                is_enabled: Some(false),
                ..Default::default()
            },
        )
        .await?;

    let epoch_after_update: i64 = gw
        .storage
        .settings()
        .get("config_epoch")
        .await?
        .as_deref()
        .and_then(|v| v.parse().ok())
        .unwrap_or(0);
    assert!(
        epoch_after_update > epoch_before_update,
        "epoch should increment on update"
    );

    gw.admin().delete_model(&model.id).await?;

    let epoch_after_delete: i64 = gw
        .storage
        .settings()
        .get("config_epoch")
        .await?
        .as_deref()
        .and_then(|v| v.parse().ok())
        .unwrap_or(0);
    assert!(
        epoch_after_delete > epoch_after_update,
        "epoch should increment on delete"
    );

    Ok(())
}

// ── readyz (StorageBootstrap::health) ─────────────────────────────────────

#[tokio::test]
async fn storage_health_is_reachable_for_sqlite_gateway() -> anyhow::Result<()> {
    let gw = build_gateway().await?;
    let health = gw.storage.bootstrap().health().await?;
    assert!(
        health.can_connect,
        "SQLite health check should report can_connect"
    );
    assert!(
        health.schema_compatible,
        "SQLite health check should report schema_compatible after migration"
    );
    Ok(())
}

#[tokio::test]
async fn schema_compatible_is_false_when_migrations_skipped() -> anyhow::Result<()> {
    // Create a SQLite pool on a fresh directory without running any migrations.
    // Gateway::new() would fail at ModelCache load, so test directly at storage level.
    let dir = tempfile::tempdir()?;
    let pool = nyro_core::db::init_pool(dir.path()).await?;
    let storage = nyro_core::storage::SqliteStorage::from_pool(pool);

    let health = storage.bootstrap().health().await?;

    assert!(
        health.can_connect,
        "should still connect to SQLite even without schema"
    );
    assert!(
        !health.schema_compatible,
        "schema_compatible must be false when models table has not been created"
    );
    Ok(())
}

fn test_data_dir() -> PathBuf {
    std::env::temp_dir().join(format!("nyro-admin-integration-tests-{}", Uuid::new_v4()))
}

fn oauth_provider_input() -> CreateProvider {
    CreateProvider {
        name: format!("oauth-provider-{}", Uuid::new_v4()),
        vendor: Some("openai".to_string()),
        protocol: "openai".to_string(),
        base_url: CODEX_RUNTIME_URL.to_string(),
        preset_key: Some("openai".to_string()),
        channel: Some("codex".to_string()),
        models_source: None,
        static_models: None,
        api_key: String::new(),
        auth_mode: "oauth".to_string(),
        use_proxy: false,
    }
}

fn api_key_provider_input(name: &str) -> CreateProvider {
    CreateProvider {
        name: name.to_string(),
        vendor: Some("openai".to_string()),
        protocol: "openai-compatible".to_string(),
        base_url: "https://api.openai.com/v1".to_string(),
        preset_key: Some("openai".to_string()),
        channel: Some("default".to_string()),
        models_source: Some("https://api.openai.com/v1/models".to_string()),
        static_models: Some("gpt-test\ntext-test".to_string()),
        api_key: "sk-test".to_string(),
        auth_mode: "apikey".to_string(),
        use_proxy: true,
    }
}

async fn seed_oauth_credential(
    gw: &Gateway,
    provider_id: &str,
    access_token: &str,
    refresh_token: &str,
) -> anyhow::Result<()> {
    gw.storage
        .oauth_credentials()
        .upsert(
            provider_id,
            UpsertOAuthCredential {
                driver_key: "codex".to_string(),
                scheme: "oauth_auth_code_pkce".to_string(),
                access_token: access_token.to_string(),
                refresh_token: Some(refresh_token.to_string()),
                expires_at: Some(FAR_FUTURE_RFC3339.to_string()),
                resource_url: Some(CODEX_RUNTIME_URL.to_string()),
                subject_id: Some("acct_test".to_string()),
                scopes: Some("[\"openid\",\"offline_access\"]".to_string()),
                meta: Some(format!(r#"{{"access_token":"{access_token}"}}"#)),
            },
        )
        .await?;
    Ok(())
}
