use nyro_core::Gateway;
use nyro_core::admin::CopyProviderOptions;
use nyro_core::config::GatewayConfig;
use nyro_core::db::models::*;

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
            name: "source-route".to_string(),
            virtual_model: "source-model".to_string(),
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
            access_control: Some(true),
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
    assert!(models.iter().all(|model| model.name != "source-route_Copy"));
    assert!(
        models
            .iter()
            .all(|model| model.virtual_model != "source-model_Copy")
    );

    let updated_model = models
        .iter()
        .find(|model| model.id == source_route.id)
        .expect("source route should remain");
    assert_eq!(updated_model.name, "source-route");
    assert_eq!(updated_model.virtual_model, "source-model");
    assert_eq!(updated_model.balance, "priority");
    assert!(updated_model.access_control);
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
            name: "no-route-copy-source".to_string(),
            virtual_model: "no-route-copy-model".to_string(),
            balance: None,
            target_provider: original.id.clone(),
            target_model: "source-upstream-model".to_string(),
            targets: vec![],
            access_control: None,
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
            name: "route-owned-by-deleted-provider".to_string(),
            virtual_model: "route-owned-model".to_string(),
            balance: None,
            target_provider: removed_provider.id.clone(),
            target_model: "gpt-delete".to_string(),
            targets: vec![],
            access_control: None,
        })
        .await?;
    let kept_route = gw
        .admin()
        .create_model(CreateModel {
            name: "route-with-secondary-deleted-provider".to_string(),
            virtual_model: "route-kept-model".to_string(),
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
            access_control: None,
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
