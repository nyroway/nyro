use std::sync::Arc;

use anyhow::Context;
use async_trait::async_trait;
use sqlx::{MySql, Pool};
use std::time::Duration;

use crate::db::models::{
    ApiKey, ApiKeyWithBindings, CreateApiKey, CreateModel, CreateModelBackend, CreateProvider,
    LogPage, LogQuery, Model, ModelBackend, ModelStats, OAuthCredential, Provider, ProviderStats,
    RequestLog, StatsHourly, StatsOverview, UpdateApiKey, UpdateModel, UpdateProvider,
    UpsertOAuthCredential, is_valid_provider_auth_mode,
};
use crate::logging::LogEntry;
use crate::storage::sql::config::SqlBackendConfig;
use crate::storage::sql::pool::RelationalPool;
use crate::storage::traits::{
    ApiKeyAccessRecord, ApiKeyStore, AuthAccessStore, LogStore, ModelBackendStore,
    ModelSnapshotStore, ModelStore, OAuthCredentialStore, ProviderStore, ProviderTestResult,
    SettingsStore, Storage, StorageBackend, StorageBootstrap, StorageHealth, UsageWindow,
};

#[derive(Clone)]
pub struct MysqlAdapter {
    pool: Pool<MySql>,
    config: SqlBackendConfig,
}

#[derive(Debug, Clone)]
pub struct MysqlHealth {
    pub can_connect: bool,
    pub schema_compatible: bool,
}

impl MysqlAdapter {
    pub async fn connect(config: SqlBackendConfig) -> anyhow::Result<Self> {
        let pool =
            RelationalPool::connect(crate::storage::sql::config::SqlBackendKind::Mysql, &config)
                .await
                .context("connect mysql adapter")?;
        let pool = pool
            .as_mysql()
            .cloned()
            .ok_or_else(|| anyhow::anyhow!("relational pool kind mismatch: expected mysql"))?;
        Ok(Self { pool, config })
    }

    pub fn config(&self) -> &SqlBackendConfig {
        &self.config
    }

    pub fn pool(&self) -> &Pool<MySql> {
        &self.pool
    }

    pub async fn ping(&self) -> anyhow::Result<()> {
        sqlx::query("SELECT 1").execute(&self.pool).await?;
        Ok(())
    }

    pub async fn health(&self) -> MysqlHealth {
        let can_connect = self.ping().await.is_ok();
        // schema_compatible: verify the final-state `models` table exists,
        // which confirms migrations have completed (routes → models rename done).
        let schema_compatible = if can_connect {
            mysql_table_exists(&self.pool, "models")
                .await
                .unwrap_or(false)
        } else {
            false
        };
        MysqlHealth {
            can_connect,
            schema_compatible,
        }
    }
}

#[derive(Clone)]
pub struct MysqlStorage {
    pool: Pool<MySql>,
    provider_store: Arc<MysqlProviderStore>,
    model_store: Arc<MysqlModelStore>,
    model_backend_store: Arc<MysqlModelBackendStore>,
    settings_store: Arc<MysqlSettingsStore>,
    api_key_store: Arc<MysqlApiKeyStore>,
    auth_store: Arc<MysqlAuthAccessStore>,
    oauth_credential_store: Arc<MysqlOAuthCredentialStore>,
    log_store: Arc<MysqlLogStore>,
    bootstrap: Arc<MysqlBootstrap>,
}

impl MysqlStorage {
    pub async fn connect(config: SqlBackendConfig) -> anyhow::Result<Self> {
        let adapter = MysqlAdapter::connect(config).await?;
        let pool = adapter.pool().clone();
        let provider_store = Arc::new(MysqlProviderStore { pool: pool.clone() });
        let model_store = Arc::new(MysqlModelStore { pool: pool.clone() });
        let model_backend_store = Arc::new(MysqlModelBackendStore { pool: pool.clone() });
        let settings_store = Arc::new(MysqlSettingsStore { pool: pool.clone() });
        let api_key_store = Arc::new(MysqlApiKeyStore { pool: pool.clone() });
        let auth_store = Arc::new(MysqlAuthAccessStore { pool: pool.clone() });
        let oauth_credential_store = Arc::new(MysqlOAuthCredentialStore { pool: pool.clone() });
        let log_store = Arc::new(MysqlLogStore { pool: pool.clone() });
        let bootstrap = Arc::new(MysqlBootstrap { adapter });
        Ok(Self {
            pool,
            provider_store,
            model_store,
            model_backend_store,
            settings_store,
            api_key_store,
            auth_store,
            oauth_credential_store,
            log_store,
            bootstrap,
        })
    }

    pub fn pool(&self) -> &Pool<MySql> {
        &self.pool
    }
}

impl Storage for MysqlStorage {
    fn providers(&self) -> &dyn ProviderStore {
        self.provider_store.as_ref()
    }

    fn models(&self) -> &dyn ModelStore {
        self.model_store.as_ref()
    }

    fn snapshots(&self) -> &dyn ModelSnapshotStore {
        self.model_store.as_ref()
    }

    fn settings(&self) -> &dyn SettingsStore {
        self.settings_store.as_ref()
    }

    fn model_backends(&self) -> Option<&dyn ModelBackendStore> {
        Some(self.model_backend_store.as_ref())
    }

    fn api_keys(&self) -> Option<&dyn ApiKeyStore> {
        Some(self.api_key_store.as_ref())
    }

    fn auth(&self) -> Option<&dyn AuthAccessStore> {
        Some(self.auth_store.as_ref())
    }

    fn logs(&self) -> &dyn LogStore {
        self.log_store.as_ref()
    }

    fn oauth_credentials(&self) -> &dyn OAuthCredentialStore {
        self.oauth_credential_store.as_ref()
    }

    fn bootstrap(&self) -> &dyn StorageBootstrap {
        self.bootstrap.as_ref()
    }
}

// ---------------------------------------------------------------------------
// OAuth Credential Store
// ---------------------------------------------------------------------------

#[derive(Clone)]
struct MysqlOAuthCredentialStore {
    pool: Pool<MySql>,
}

#[async_trait]
impl OAuthCredentialStore for MysqlOAuthCredentialStore {
    async fn get(&self, provider_id: &str) -> anyhow::Result<Option<OAuthCredential>> {
        Ok(sqlx::query_as::<_, OAuthCredential>(
            "SELECT provider_id, driver_key, scheme, access_token, refresh_token, DATE_FORMAT(expires_at, '%Y-%m-%d %H:%i:%S') AS expires_at, resource_url, subject_id, scopes, meta, status, status_version, last_error, DATE_FORMAT(last_refresh_at, '%Y-%m-%d %H:%i:%S') AS last_refresh_at, DATE_FORMAT(created_at, '%Y-%m-%d %H:%i:%S') AS created_at, DATE_FORMAT(updated_at, '%Y-%m-%d %H:%i:%S') AS updated_at FROM provider_oauth_credentials WHERE provider_id = ?",
        )
        .bind(provider_id)
        .fetch_optional(&self.pool)
        .await?)
    }

    async fn upsert(
        &self,
        provider_id: &str,
        input: UpsertOAuthCredential,
    ) -> anyhow::Result<OAuthCredential> {
        sqlx::query(
            "INSERT INTO provider_oauth_credentials (provider_id, driver_key, scheme, access_token, refresh_token, expires_at, resource_url, subject_id, scopes, meta, status, status_version, last_error) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'connected', 0, NULL) ON DUPLICATE KEY UPDATE driver_key=VALUES(driver_key), scheme=VALUES(scheme), access_token=VALUES(access_token), refresh_token=VALUES(refresh_token), expires_at=VALUES(expires_at), resource_url=VALUES(resource_url), subject_id=VALUES(subject_id), scopes=VALUES(scopes), meta=VALUES(meta), status='connected', status_version=status_version+1, last_error=NULL, updated_at=NOW()",
        )
        .bind(provider_id)
        .bind(&input.driver_key)
        .bind(&input.scheme)
        .bind(&input.access_token)
        .bind(&input.refresh_token)
        .bind(&input.expires_at)
        .bind(&input.resource_url)
        .bind(&input.subject_id)
        .bind(input.scopes.as_deref().unwrap_or("[]"))
        .bind(input.meta.as_deref().unwrap_or("{}"))
        .execute(&self.pool)
        .await?;
        self.get(provider_id)
            .await?
            .context("credential not found after upsert")
    }

    async fn delete(&self, provider_id: &str) -> anyhow::Result<()> {
        sqlx::query("DELETE FROM provider_oauth_credentials WHERE provider_id = ?")
            .bind(provider_id)
            .execute(&self.pool)
            .await?;
        Ok(())
    }

    async fn try_begin_refresh(
        &self,
        provider_id: &str,
        expected_version: i32,
    ) -> anyhow::Result<Option<OAuthCredential>> {
        let result = sqlx::query(
            "UPDATE provider_oauth_credentials SET status='refreshing', status_version=status_version+1, updated_at=NOW() WHERE provider_id=? AND status='connected' AND status_version=?",
        )
        .bind(provider_id)
        .bind(expected_version)
        .execute(&self.pool)
        .await?;
        if result.rows_affected() > 0 {
            Ok(self.get(provider_id).await?)
        } else {
            Ok(None)
        }
    }

    async fn complete_refresh(
        &self,
        provider_id: &str,
        input: UpsertOAuthCredential,
    ) -> anyhow::Result<OAuthCredential> {
        sqlx::query(
            "UPDATE provider_oauth_credentials SET driver_key=?, scheme=?, access_token=?, refresh_token=?, expires_at=?, resource_url=?, subject_id=?, scopes=?, meta=?, status='connected', status_version=status_version+1, last_error=NULL, last_refresh_at=NOW(), updated_at=NOW() WHERE provider_id=?",
        )
        .bind(&input.driver_key)
        .bind(&input.scheme)
        .bind(&input.access_token)
        .bind(&input.refresh_token)
        .bind(&input.expires_at)
        .bind(&input.resource_url)
        .bind(&input.subject_id)
        .bind(input.scopes.as_deref().unwrap_or("[]"))
        .bind(input.meta.as_deref().unwrap_or("{}"))
        .bind(provider_id)
        .execute(&self.pool)
        .await?;
        self.get(provider_id)
            .await?
            .context("credential not found after complete_refresh")
    }

    async fn fail_refresh(&self, provider_id: &str, error_message: &str) -> anyhow::Result<()> {
        sqlx::query(
            "UPDATE provider_oauth_credentials SET status='error', last_error=?, status_version=status_version+1, updated_at=NOW() WHERE provider_id=?",
        )
        .bind(error_message)
        .bind(provider_id)
        .execute(&self.pool)
        .await?;
        Ok(())
    }

    async fn list_expiring(&self, before: Duration) -> anyhow::Result<Vec<OAuthCredential>> {
        let seconds = before.as_secs() as i64;
        Ok(sqlx::query_as::<_, OAuthCredential>(
            "SELECT provider_id, driver_key, scheme, access_token, refresh_token, DATE_FORMAT(expires_at, '%Y-%m-%d %H:%i:%S') AS expires_at, resource_url, subject_id, scopes, meta, status, status_version, last_error, DATE_FORMAT(last_refresh_at, '%Y-%m-%d %H:%i:%S') AS last_refresh_at, DATE_FORMAT(created_at, '%Y-%m-%d %H:%i:%S') AS created_at, DATE_FORMAT(updated_at, '%Y-%m-%d %H:%i:%S') AS updated_at FROM provider_oauth_credentials WHERE status='connected' AND expires_at IS NOT NULL AND expires_at <= NOW() + INTERVAL ? SECOND",
        )
        .bind(seconds)
        .fetch_all(&self.pool)
        .await?)
    }

    async fn recover_stale_refreshing(&self, timeout: Duration) -> anyhow::Result<u64> {
        let seconds = timeout.as_secs() as i64;
        let result = sqlx::query(
            "UPDATE provider_oauth_credentials SET status='error', last_error='refresh timeout: process did not complete within timeout', status_version=status_version+1, updated_at=NOW() WHERE status='refreshing' AND updated_at + INTERVAL ? SECOND < NOW()",
        )
        .bind(seconds)
        .execute(&self.pool)
        .await?;
        Ok(result.rows_affected())
    }
}

// ---------------------------------------------------------------------------
// Provider Store
// ---------------------------------------------------------------------------

#[derive(Clone)]
struct MysqlProviderStore {
    pool: Pool<MySql>,
}

#[async_trait]
impl ProviderStore for MysqlProviderStore {
    async fn list(&self) -> anyhow::Result<Vec<Provider>> {
        Ok(sqlx::query_as::<_, Provider>(&provider_select(None))
            .fetch_all(&self.pool)
            .await?)
    }

    async fn get(&self, id: &str) -> anyhow::Result<Option<Provider>> {
        Ok(
            sqlx::query_as::<_, Provider>(&provider_select(Some("WHERE id = ?")))
                .bind(id)
                .fetch_optional(&self.pool)
                .await?,
        )
    }

    async fn create(&self, input: CreateProvider) -> anyhow::Result<Provider> {
        let id = uuid::Uuid::new_v4().to_string();
        let vendor = normalize_provider_vendor(input.vendor.as_deref());
        let models_source = input.effective_models_source().map(ToString::to_string);
        if !is_valid_provider_auth_mode(&input.auth_mode) {
            anyhow::bail!("unsupported provider auth_mode: {}", input.auth_mode);
        }
        sqlx::query(
            "INSERT INTO providers (id, name, vendor, protocol, base_url, preset_key, channel, models_source, static_models, api_key, auth_mode, use_proxy) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
        )
        .bind(&id)
        .bind(input.name.trim())
        .bind(vendor)
        .bind(input.protocol.trim())
        .bind(input.base_url.trim())
        .bind(input.preset_key)
        .bind(input.channel)
        .bind(models_source)
        .bind(input.static_models)
        .bind(input.api_key)
        .bind(input.auth_mode)
        .bind(input.use_proxy)
        .execute(&self.pool)
        .await?;
        self.get(&id)
            .await?
            .context("provider missing after create")
    }

    async fn update(&self, id: &str, input: UpdateProvider) -> anyhow::Result<Provider> {
        let current = self
            .get(id)
            .await?
            .context("provider not found for update")?;
        let models_source_input = input.models_source.map(|value| value.trim().to_string());
        let name = input.name.unwrap_or(current.name);
        let vendor = if input.vendor.is_some() {
            normalize_provider_vendor(input.vendor.as_deref())
        } else {
            normalize_provider_vendor(current.vendor.as_deref())
        };
        let models_source = models_source_input.or_else(|| current.models_source.clone());
        let protocol = input.protocol.unwrap_or(current.protocol.clone());
        let base_url = input.base_url.unwrap_or(current.base_url);
        let preset_key = input.preset_key.or(current.preset_key);
        let channel = input.channel.or(current.channel);
        let static_models = input.static_models.or(current.static_models);
        let api_key = input.api_key.unwrap_or(current.api_key);
        let auth_mode = input.auth_mode.unwrap_or(current.auth_mode);
        if !is_valid_provider_auth_mode(&auth_mode) {
            anyhow::bail!("unsupported provider auth_mode: {}", auth_mode);
        }
        let use_proxy = input.use_proxy.unwrap_or(current.use_proxy);
        let is_enabled = input.is_enabled.unwrap_or(current.is_enabled);

        sqlx::query(
            "UPDATE providers SET name=?, vendor=?, protocol=?, base_url=?, preset_key=?, channel=?, models_source=?, static_models=?, api_key=?, auth_mode=?, use_proxy=?, is_enabled=?, updated_at=NOW() WHERE id=?",
        )
        .bind(name.trim())
        .bind(vendor)
        .bind(protocol.trim())
        .bind(base_url.trim())
        .bind(preset_key)
        .bind(channel)
        .bind(models_source)
        .bind(static_models)
        .bind(api_key)
        .bind(auth_mode)
        .bind(use_proxy)
        .bind(is_enabled)
        .bind(id)
        .execute(&self.pool)
        .await?;
        self.get(id).await?.context("provider missing after update")
    }

    async fn delete(&self, id: &str) -> anyhow::Result<()> {
        let mut tx = self.pool.begin().await?;

        sqlx::query(
            "DELETE FROM model_backends
             WHERE provider_id = ?
                OR model_id IN (SELECT id FROM models WHERE target_provider = ?)",
        )
        .bind(id)
        .bind(id)
        .execute(&mut *tx)
        .await?;

        sqlx::query("DELETE FROM models WHERE target_provider = ?")
            .bind(id)
            .execute(&mut *tx)
            .await?;

        sqlx::query("DELETE FROM providers WHERE id = ?")
            .bind(id)
            .execute(&mut *tx)
            .await?;

        tx.commit().await?;
        Ok(())
    }

    async fn exists_by_name(&self, name: &str, exclude_id: Option<&str>) -> anyhow::Result<bool> {
        let row = if let Some(exclude_id) = exclude_id {
            sqlx::query_scalar::<_, String>(
                "SELECT id FROM providers WHERE LOWER(TRIM(name)) = LOWER(TRIM(?)) AND id != ? LIMIT 1",
            )
            .bind(name)
            .bind(exclude_id)
            .fetch_optional(&self.pool)
            .await?
        } else {
            sqlx::query_scalar::<_, String>(
                "SELECT id FROM providers WHERE LOWER(TRIM(name)) = LOWER(TRIM(?)) LIMIT 1",
            )
            .bind(name)
            .fetch_optional(&self.pool)
            .await?
        };
        Ok(row.is_some())
    }

    async fn record_test_result(
        &self,
        provider_id: &str,
        result: ProviderTestResult,
    ) -> anyhow::Result<()> {
        let _ = result.tested_at;
        sqlx::query(
            "UPDATE providers SET last_test_success = ?, last_test_at = NOW() WHERE id = ?",
        )
        .bind(result.success)
        .bind(provider_id)
        .execute(&self.pool)
        .await?;
        Ok(())
    }
}

// ---------------------------------------------------------------------------
// Model Store
// ---------------------------------------------------------------------------

#[derive(Clone)]
struct MysqlModelStore {
    pool: Pool<MySql>,
}

#[async_trait]
impl ModelStore for MysqlModelStore {
    async fn list(&self) -> anyhow::Result<Vec<Model>> {
        Ok(
            sqlx::query_as::<_, Model>(&model_select(Some("ORDER BY created_at DESC")))
                .fetch_all(&self.pool)
                .await?,
        )
    }

    async fn get(&self, id: &str) -> anyhow::Result<Option<Model>> {
        let sql = format!("{} WHERE id = ?", model_select(None));
        Ok(sqlx::query_as::<_, Model>(&sql)
            .bind(id)
            .fetch_optional(&self.pool)
            .await?)
    }

    async fn create(&self, input: CreateModel) -> anyhow::Result<Model> {
        let id = uuid::Uuid::new_v4().to_string();
        sqlx::query(
            "INSERT INTO models (id, name, balance, target_provider, target_model, enable_auth, enable_payload) VALUES (?, ?, ?, ?, ?, ?, ?)",
        )
        .bind(&id)
        .bind(input.name.trim())
        .bind(input.balance.unwrap_or_else(|| "weighted".to_string()))
        .bind(input.target_provider.trim())
        .bind(input.target_model.trim())
        .bind(input.enable_auth.unwrap_or(false))
        .bind(input.enable_payload)
        .execute(&self.pool)
        .await?;
        self.get(&id).await?.context("model missing after create")
    }

    async fn update(&self, id: &str, input: UpdateModel) -> anyhow::Result<Model> {
        let current = self.get(id).await?.context("model not found for update")?;
        let name = input.name.unwrap_or(current.name);
        let balance = input.balance.unwrap_or(current.balance);
        let target_provider = input.target_provider.unwrap_or(current.target_provider);
        let target_model = input.target_model.unwrap_or(current.target_model);
        let enable_auth = input.enable_auth.unwrap_or(current.enable_auth);
        let enable_payload = input.enable_payload.unwrap_or(current.enable_payload);
        let is_enabled = input.is_enabled.unwrap_or(current.is_enabled);

        sqlx::query(
            "UPDATE models SET name=?, balance=?, target_provider=?, target_model=?, enable_auth=?, enable_payload=?, is_enabled=? WHERE id=?",
        )
        .bind(name.trim())
        .bind(balance.trim().to_lowercase())
        .bind(target_provider.trim())
        .bind(target_model.trim())
        .bind(enable_auth)
        .bind(enable_payload)
        .bind(is_enabled)
        .bind(id)
        .execute(&self.pool)
        .await?;
        self.get(id).await?.context("model missing after update")
    }

    async fn delete(&self, id: &str) -> anyhow::Result<()> {
        sqlx::query("DELETE FROM models WHERE id = ?")
            .bind(id)
            .execute(&self.pool)
            .await?;
        Ok(())
    }

    async fn exists_by_name(&self, name: &str, exclude_id: Option<&str>) -> anyhow::Result<bool> {
        let row = if let Some(exclude_id) = exclude_id {
            sqlx::query_scalar::<_, String>(
                "SELECT id FROM models WHERE LOWER(TRIM(name)) = LOWER(TRIM(?)) AND id != ? LIMIT 1",
            )
            .bind(name)
            .bind(exclude_id)
            .fetch_optional(&self.pool)
            .await?
        } else {
            sqlx::query_scalar::<_, String>(
                "SELECT id FROM models WHERE LOWER(TRIM(name)) = LOWER(TRIM(?)) LIMIT 1",
            )
            .bind(name)
            .fetch_optional(&self.pool)
            .await?
        };
        Ok(row.is_some())
    }
}

#[async_trait]
impl ModelSnapshotStore for MysqlModelStore {
    async fn load_active_snapshot(&self) -> anyhow::Result<Vec<Model>> {
        let sql = format!("{} WHERE COALESCE(is_enabled, 1) = 1", model_select(None));
        Ok(sqlx::query_as::<_, Model>(&sql)
            .fetch_all(&self.pool)
            .await?)
    }
}

// ---------------------------------------------------------------------------
// Model Backend Store
// ---------------------------------------------------------------------------

#[derive(Clone)]
struct MysqlModelBackendStore {
    pool: Pool<MySql>,
}

#[async_trait]
impl ModelBackendStore for MysqlModelBackendStore {
    async fn list_backends_by_model(&self, model_id: &str) -> anyhow::Result<Vec<ModelBackend>> {
        Ok(sqlx::query_as::<_, ModelBackend>(
            "SELECT id, model_id, provider_id, model, weight, priority, DATE_FORMAT(created_at, '%Y-%m-%d %H:%i:%S') AS created_at FROM model_backends WHERE model_id = ? ORDER BY priority ASC, created_at ASC",
        )
        .bind(model_id)
        .fetch_all(&self.pool)
        .await?)
    }

    async fn set_backends(
        &self,
        model_id: &str,
        backends: &[CreateModelBackend],
    ) -> anyhow::Result<Vec<ModelBackend>> {
        let mut tx = self.pool.begin().await?;
        sqlx::query("DELETE FROM model_backends WHERE model_id = ?")
            .bind(model_id)
            .execute(&mut *tx)
            .await?;

        for backend in backends {
            let id = uuid::Uuid::new_v4().to_string();
            sqlx::query(
                "INSERT INTO model_backends (id, model_id, provider_id, model, weight, priority) VALUES (?, ?, ?, ?, ?, ?)",
            )
            .bind(id)
            .bind(model_id)
            .bind(backend.provider_id.trim())
            .bind(backend.model.trim())
            .bind(backend.weight.unwrap_or(100).max(0))
            .bind(backend.priority.unwrap_or(1).max(1))
            .execute(&mut *tx)
            .await?;
        }

        tx.commit().await?;
        self.list_backends_by_model(model_id).await
    }

    async fn delete_backends_by_model(&self, model_id: &str) -> anyhow::Result<()> {
        sqlx::query("DELETE FROM model_backends WHERE model_id = ?")
            .bind(model_id)
            .execute(&self.pool)
            .await?;
        Ok(())
    }
}

// ---------------------------------------------------------------------------
// Settings Store
// ---------------------------------------------------------------------------

#[derive(Clone)]
struct MysqlSettingsStore {
    pool: Pool<MySql>,
}

#[async_trait]
impl SettingsStore for MysqlSettingsStore {
    async fn get(&self, key: &str) -> anyhow::Result<Option<String>> {
        let row: Option<(String,)> = sqlx::query_as("SELECT value FROM settings WHERE name = ?")
            .bind(key)
            .fetch_optional(&self.pool)
            .await?;
        Ok(row.map(|r| r.0))
    }

    async fn set(&self, key: &str, value: &str) -> anyhow::Result<()> {
        sqlx::query(
            "INSERT INTO settings (name, value, updated_at) VALUES (?, ?, NOW()) ON DUPLICATE KEY UPDATE value=VALUES(value), updated_at=NOW()",
        )
        .bind(key)
        .bind(value)
        .execute(&self.pool)
        .await?;
        Ok(())
    }

    async fn list_all(&self) -> anyhow::Result<Vec<(String, String)>> {
        Ok(
            sqlx::query_as::<_, (String, String)>("SELECT name, value FROM settings")
                .fetch_all(&self.pool)
                .await?,
        )
    }
}

// ---------------------------------------------------------------------------
// API Key Store
// ---------------------------------------------------------------------------

#[derive(Clone)]
struct MysqlApiKeyStore {
    pool: Pool<MySql>,
}

#[async_trait]
impl ApiKeyStore for MysqlApiKeyStore {
    async fn list(&self) -> anyhow::Result<Vec<ApiKeyWithBindings>> {
        let rows = sqlx::query_as::<_, ApiKey>(&api_key_select(None))
            .fetch_all(&self.pool)
            .await?;
        let mut items = Vec::with_capacity(rows.len());
        for row in rows {
            let model_ids = list_api_key_model_ids(&self.pool, &row.id).await?;
            items.push(api_key_with_bindings(row, model_ids));
        }
        Ok(items)
    }

    async fn get(&self, id: &str) -> anyhow::Result<Option<ApiKeyWithBindings>> {
        let row = sqlx::query_as::<_, ApiKey>(&api_key_select(Some("WHERE id = ?")))
            .bind(id)
            .fetch_optional(&self.pool)
            .await?;
        let Some(row) = row else {
            return Ok(None);
        };
        let model_ids = list_api_key_model_ids(&self.pool, id).await?;
        Ok(Some(api_key_with_bindings(row, model_ids)))
    }

    async fn create(&self, input: CreateApiKey) -> anyhow::Result<ApiKeyWithBindings> {
        let id = uuid::Uuid::new_v4().to_string();
        let key = format!("sk-{}", uuid::Uuid::new_v4().simple());
        sqlx::query(
            "INSERT INTO api_keys (id, token, name, rpm, rpd, tpm, tpd, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''))",
        )
        .bind(&id)
        .bind(&key)
        .bind(input.name.trim())
        .bind(input.rpm)
        .bind(input.rpd)
        .bind(input.tpm)
        .bind(input.tpd)
        .bind(input.expires_at.as_deref().map(str::trim).unwrap_or(""))
        .execute(&self.pool)
        .await?;
        replace_api_key_models(&self.pool, &id, &input.model_ids).await?;
        self.get(&id).await?.context("api key missing after create")
    }

    async fn update(&self, id: &str, input: UpdateApiKey) -> anyhow::Result<ApiKeyWithBindings> {
        let current = sqlx::query_as::<_, ApiKey>(&api_key_select(Some("WHERE id = ?")))
            .bind(id)
            .fetch_optional(&self.pool)
            .await?
            .context("api key not found for update")?;
        let name = input.name.unwrap_or(current.name);
        let rpm = input.rpm.or(current.rpm);
        let rpd = input.rpd.or(current.rpd);
        let tpm = input.tpm.or(current.tpm);
        let tpd = input.tpd.or(current.tpd);
        let is_enabled = input.is_enabled.unwrap_or(current.is_enabled);
        let expires_at = input.expires_at.or(current.expires_at);

        sqlx::query(
            "UPDATE api_keys SET name=?, rpm=?, rpd=?, tpm=?, tpd=?, is_enabled=?, expires_at=NULLIF(?, ''), updated_at=NOW() WHERE id=?",
        )
        .bind(name.trim())
        .bind(rpm)
        .bind(rpd)
        .bind(tpm)
        .bind(tpd)
        .bind(is_enabled)
        .bind(expires_at.as_deref().map(str::trim).unwrap_or(""))
        .bind(id)
        .execute(&self.pool)
        .await?;

        if let Some(model_ids) = input.model_ids {
            replace_api_key_models(&self.pool, id, &model_ids).await?;
        }
        self.get(id).await?.context("api key missing after update")
    }

    async fn delete(&self, id: &str) -> anyhow::Result<()> {
        sqlx::query("DELETE FROM api_keys WHERE id = ?")
            .bind(id)
            .execute(&self.pool)
            .await?;
        Ok(())
    }

    async fn exists_by_name(&self, name: &str, exclude_id: Option<&str>) -> anyhow::Result<bool> {
        let row = if let Some(exclude_id) = exclude_id {
            sqlx::query_scalar::<_, String>(
                "SELECT id FROM api_keys WHERE LOWER(TRIM(name)) = LOWER(TRIM(?)) AND id != ? LIMIT 1",
            )
            .bind(name)
            .bind(exclude_id)
            .fetch_optional(&self.pool)
            .await?
        } else {
            sqlx::query_scalar::<_, String>(
                "SELECT id FROM api_keys WHERE LOWER(TRIM(name)) = LOWER(TRIM(?)) LIMIT 1",
            )
            .bind(name)
            .fetch_optional(&self.pool)
            .await?
        };
        Ok(row.is_some())
    }
}

// ---------------------------------------------------------------------------
// Auth Access Store
// ---------------------------------------------------------------------------

#[derive(Clone)]
struct MysqlAuthAccessStore {
    pool: Pool<MySql>,
}

#[async_trait]
impl AuthAccessStore for MysqlAuthAccessStore {
    async fn find_api_key(&self, raw_key: &str) -> anyhow::Result<Option<ApiKeyAccessRecord>> {
        let row = sqlx::query_as::<
            _,
            (
                String,
                String,
                bool,
                Option<String>,
                Option<i32>,
                Option<i32>,
                Option<i32>,
                Option<i32>,
            ),
        >(
            "SELECT id, COALESCE(name, '') AS name, COALESCE(is_enabled, 1) AS is_enabled, DATE_FORMAT(expires_at, '%Y-%m-%d %H:%i:%S') AS expires_at, rpm, rpd, tpm, tpd FROM api_keys WHERE token = ?",
        )
        .bind(raw_key)
        .fetch_optional(&self.pool)
        .await?;

        Ok(row.map(
            |(id, name, is_enabled, expires_at, rpm, rpd, tpm, tpd)| ApiKeyAccessRecord {
                id,
                name,
                is_enabled,
                expires_at,
                rpm,
                rpd,
                tpm,
                tpd,
            },
        ))
    }

    async fn model_binding_exists(&self, api_key_id: &str, model_id: &str) -> anyhow::Result<bool> {
        let count = sqlx::query_scalar::<_, i64>(
            "SELECT COUNT(*) FROM api_key_models WHERE api_key_id = ? AND model_id = ?",
        )
        .bind(api_key_id)
        .bind(model_id)
        .fetch_one(&self.pool)
        .await?;
        Ok(count > 0)
    }

    async fn list_bound_model_ids(&self, api_key_id: &str) -> anyhow::Result<Vec<String>> {
        list_api_key_model_ids(&self.pool, api_key_id).await
    }

    async fn request_count_since(
        &self,
        api_key_id: &str,
        window: UsageWindow,
    ) -> anyhow::Result<i64> {
        let seconds: i64 = match window {
            UsageWindow::Minute => 60,
            UsageWindow::Day => 86400,
        };
        Ok(sqlx::query_scalar::<_, i64>(
            "SELECT COUNT(*) FROM request_logs WHERE api_key_id = ? AND created_at >= UNIX_TIMESTAMP(NOW() - INTERVAL ? SECOND) * 1000"
        )
        .bind(api_key_id)
        .bind(seconds)
        .fetch_one(&self.pool)
        .await?)
    }

    async fn token_count_since(
        &self,
        api_key_id: &str,
        window: UsageWindow,
    ) -> anyhow::Result<i64> {
        let seconds: i64 = match window {
            UsageWindow::Minute => 60,
            UsageWindow::Day => 86400,
        };
        Ok(sqlx::query_scalar::<_, i64>(
            "SELECT CAST(COALESCE(SUM(input_tokens + output_tokens), 0) AS SIGNED) FROM request_logs WHERE api_key_id = ? AND created_at >= UNIX_TIMESTAMP(NOW() - INTERVAL ? SECOND) * 1000"
        )
        .bind(api_key_id)
        .bind(seconds)
        .fetch_one(&self.pool)
        .await?)
    }
}

// ---------------------------------------------------------------------------
// Log Store
// ---------------------------------------------------------------------------

#[derive(Clone)]
struct MysqlLogStore {
    pool: Pool<MySql>,
}

#[async_trait]
impl LogStore for MysqlLogStore {
    async fn append_batch(&self, entries: Vec<LogEntry>) -> anyhow::Result<()> {
        for entry in entries {
            let id = uuid::Uuid::new_v4().to_string();
            sqlx::query(
                r#"INSERT INTO request_logs
                    (id, created_at, api_key_id, api_key_name,
                     client_protocol, upstream_protocol, provider_id, provider_name, model_id, model_name, upstream_url,
                     client_model, upstream_model,
                     method, path,
                     client_request_headers, client_request_body,
                     client_response_headers, client_response_body,
                     upstream_request_headers, upstream_request_body,
                     upstream_response_headers, upstream_response_body,
                     upstream_status_code, client_status_code,
                     latency_total_ms, latency_upstream_ms,
                     input_tokens, output_tokens, cache_read_tokens,
                     is_stream, stream_chunks_count, stream_first_chunk_ms)
                VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)"#,
            )
            .bind(&id)
            .bind(entry.created_at)
            .bind(&entry.api_key_id)
            .bind(&entry.api_key_name)
            .bind(&entry.client_protocol)
            .bind(&entry.upstream_protocol)
            .bind(&entry.provider_id)
            .bind(&entry.provider_name)
            .bind(&entry.model_id)
            .bind(&entry.model_name)
            .bind(&entry.upstream_url)
            .bind(&entry.client_model)
            .bind(&entry.upstream_model)
            .bind(&entry.method)
            .bind(&entry.path)
            .bind(&entry.client_request_headers)
            .bind(&entry.client_request_body)
            .bind(&entry.client_response_headers)
            .bind(&entry.client_response_body)
            .bind(&entry.upstream_request_headers)
            .bind(&entry.upstream_request_body)
            .bind(&entry.upstream_response_headers)
            .bind(&entry.upstream_response_body)
            .bind(entry.upstream_status_code)
            .bind(entry.client_status_code)
            .bind(entry.latency_total_ms)
            .bind(entry.latency_upstream_ms)
            .bind(entry.input_tokens())
            .bind(entry.output_tokens())
            .bind(entry.cache_read_tokens())
            .bind(entry.is_stream)
            .bind(entry.stream_chunks_count)
            .bind(entry.stream_first_chunk_ms)
            .execute(&self.pool)
            .await?;
        }
        Ok(())
    }

    async fn query(&self, query: LogQuery) -> anyhow::Result<LogPage> {
        let mut count_sql = String::from("SELECT COUNT(*) AS total FROM request_logs WHERE 1=1");
        // List query skips heavy body/header columns (NULL placeholders preserve struct layout).
        let mut data_sql = String::from(
            "SELECT id, COALESCE(created_at, 0) AS created_at, api_key_id, api_key_name, \
             client_protocol, upstream_protocol, provider_id, provider_name, model_id, model_name, upstream_url, \
             client_model, upstream_model, method, path, \
             CAST(NULL AS CHAR) AS client_request_headers, CAST(NULL AS CHAR) AS client_request_body, \
             CAST(NULL AS CHAR) AS client_response_headers, CAST(NULL AS CHAR) AS client_response_body, \
             CAST(NULL AS CHAR) AS upstream_request_headers, CAST(NULL AS CHAR) AS upstream_request_body, \
             CAST(NULL AS CHAR) AS upstream_response_headers, CAST(NULL AS CHAR) AS upstream_response_body, \
             upstream_status_code, client_status_code, \
             latency_total_ms, latency_upstream_ms, \
             input_tokens, output_tokens, COALESCE(cache_read_tokens, 0) AS cache_read_tokens, \
             COALESCE(is_stream, 0) AS is_stream, stream_chunks_count, stream_first_chunk_ms \
             FROM request_logs WHERE 1=1",
        );
        let mut bind_values: Vec<String> = Vec::new();

        if let Some(provider) = query.provider.filter(|v| !v.is_empty()) {
            count_sql.push_str(" AND provider_id = ?");
            data_sql.push_str(" AND provider_id = ?");
            bind_values.push(provider);
        }
        if let Some(model) = query.model.filter(|v| !v.is_empty()) {
            count_sql.push_str(" AND upstream_model = ?");
            data_sql.push_str(" AND upstream_model = ?");
            bind_values.push(model);
        }
        if let Some(status_min) = query.status_min {
            count_sql.push_str(" AND client_status_code >= ?");
            data_sql.push_str(" AND client_status_code >= ?");
            bind_values.push(status_min.to_string());
        }
        if let Some(status_max) = query.status_max {
            count_sql.push_str(" AND client_status_code <= ?");
            data_sql.push_str(" AND client_status_code <= ?");
            bind_values.push(status_max.to_string());
        }

        data_sql.push_str(" ORDER BY created_at DESC LIMIT ? OFFSET ?");

        let mut count_query = sqlx::query_scalar::<_, i64>(&count_sql);
        let mut data_query = sqlx::query_as::<_, RequestLog>(&data_sql);
        for value in &bind_values {
            count_query = count_query.bind(value);
            data_query = data_query.bind(value);
        }

        let total = count_query.fetch_one(&self.pool).await?;
        let items = data_query
            .bind(query.limit.unwrap_or(50))
            .bind(query.offset.unwrap_or(0))
            .fetch_all(&self.pool)
            .await?;
        Ok(LogPage { items, total })
    }

    async fn find_by_id(&self, id: &str) -> anyhow::Result<Option<RequestLog>> {
        let row = sqlx::query_as::<_, RequestLog>(
            "SELECT id, COALESCE(created_at, 0) AS created_at, api_key_id, api_key_name, \
             client_protocol, upstream_protocol, provider_id, provider_name, model_id, model_name, upstream_url, \
             client_model, upstream_model, method, path, \
             client_request_headers, client_request_body, \
             client_response_headers, client_response_body, \
             upstream_request_headers, upstream_request_body, \
             upstream_response_headers, upstream_response_body, \
             upstream_status_code, client_status_code, \
             latency_total_ms, latency_upstream_ms, \
             input_tokens, output_tokens, COALESCE(cache_read_tokens, 0) AS cache_read_tokens, \
             COALESCE(is_stream, 0) AS is_stream, stream_chunks_count, stream_first_chunk_ms \
             FROM request_logs WHERE id = ?",
        )
        .bind(id)
        .fetch_optional(&self.pool)
        .await?;
        Ok(row)
    }

    async fn cleanup_before(&self, cutoff_expression: &str) -> anyhow::Result<u64> {
        let interval = cutoff_expression.trim().trim_start_matches('-').trim();
        let sql = format!(
            "DELETE FROM request_logs WHERE created_at < UNIX_TIMESTAMP(NOW() - INTERVAL '{interval}') * 1000"
        );
        let result = sqlx::query(&sql).execute(&self.pool).await?;
        Ok(result.rows_affected())
    }

    async fn stats_overview(&self, hours: Option<i64>) -> anyhow::Result<StatsOverview> {
        let sql = if let Some(hours) = hours {
            format!(
                "SELECT COUNT(*) AS total_requests, CAST(COALESCE(SUM(input_tokens), 0) AS SIGNED) AS total_input_tokens, CAST(COALESCE(SUM(output_tokens), 0) AS SIGNED) AS total_output_tokens, CAST(COALESCE(AVG(latency_total_ms), 0) AS DOUBLE) AS avg_duration_ms, CAST(COALESCE(SUM(CASE WHEN client_status_code >= 400 THEN 1 ELSE 0 END), 0) AS SIGNED) AS error_count FROM request_logs WHERE created_at >= UNIX_TIMESTAMP(NOW() - INTERVAL {hours} HOUR) * 1000"
            )
        } else {
            "SELECT COUNT(*) AS total_requests, CAST(COALESCE(SUM(input_tokens), 0) AS SIGNED) AS total_input_tokens, CAST(COALESCE(SUM(output_tokens), 0) AS SIGNED) AS total_output_tokens, CAST(COALESCE(AVG(latency_total_ms), 0) AS DOUBLE) AS avg_duration_ms, CAST(COALESCE(SUM(CASE WHEN client_status_code >= 400 THEN 1 ELSE 0 END), 0) AS SIGNED) AS error_count FROM request_logs".to_string()
        };
        Ok(sqlx::query_as::<_, StatsOverview>(&sql)
            .fetch_one(&self.pool)
            .await?)
    }

    async fn stats_hourly(&self, hours: i64) -> anyhow::Result<Vec<StatsHourly>> {
        let sql = format!(
            "SELECT DATE_FORMAT(FROM_UNIXTIME(created_at/1000), '%Y-%m-%d %H:00:00') AS hour, COUNT(*) AS request_count, CAST(COALESCE(SUM(CASE WHEN client_status_code >= 400 THEN 1 ELSE 0 END), 0) AS SIGNED) AS error_count, CAST(COALESCE(SUM(input_tokens), 0) AS SIGNED) AS total_input_tokens, CAST(COALESCE(SUM(output_tokens), 0) AS SIGNED) AS total_output_tokens, CAST(COALESCE(AVG(latency_total_ms), 0) AS DOUBLE) AS avg_duration_ms FROM request_logs WHERE created_at >= UNIX_TIMESTAMP(NOW() - INTERVAL {hours} HOUR) * 1000 GROUP BY hour ORDER BY hour ASC"
        );
        Ok(sqlx::query_as::<_, StatsHourly>(&sql)
            .fetch_all(&self.pool)
            .await?)
    }

    async fn stats_by_model(&self, hours: Option<i64>) -> anyhow::Result<Vec<ModelStats>> {
        let sql = if let Some(hours) = hours {
            format!(
                "SELECT upstream_model AS model, COUNT(*) AS request_count, CAST(COALESCE(SUM(input_tokens), 0) AS SIGNED) AS total_input_tokens, CAST(COALESCE(SUM(output_tokens), 0) AS SIGNED) AS total_output_tokens, CAST(COALESCE(AVG(latency_total_ms), 0) AS DOUBLE) AS avg_duration_ms FROM request_logs WHERE created_at >= UNIX_TIMESTAMP(NOW() - INTERVAL {hours} HOUR) * 1000 GROUP BY upstream_model ORDER BY request_count DESC"
            )
        } else {
            "SELECT upstream_model AS model, COUNT(*) AS request_count, CAST(COALESCE(SUM(input_tokens), 0) AS SIGNED) AS total_input_tokens, CAST(COALESCE(SUM(output_tokens), 0) AS SIGNED) AS total_output_tokens, CAST(COALESCE(AVG(latency_total_ms), 0) AS DOUBLE) AS avg_duration_ms FROM request_logs GROUP BY upstream_model ORDER BY request_count DESC".to_string()
        };
        Ok(sqlx::query_as::<_, ModelStats>(&sql)
            .fetch_all(&self.pool)
            .await?)
    }

    async fn stats_by_provider(&self, hours: Option<i64>) -> anyhow::Result<Vec<ProviderStats>> {
        let sql = if let Some(hours) = hours {
            format!(
                "SELECT COALESCE(provider_name, provider_id, '') AS provider, COUNT(*) AS request_count, CAST(COALESCE(SUM(CASE WHEN client_status_code >= 400 THEN 1 ELSE 0 END), 0) AS SIGNED) AS error_count, CAST(COALESCE(AVG(latency_total_ms), 0) AS DOUBLE) AS avg_duration_ms FROM request_logs WHERE created_at >= UNIX_TIMESTAMP(NOW() - INTERVAL {hours} HOUR) * 1000 GROUP BY COALESCE(provider_name, provider_id, '') ORDER BY request_count DESC"
            )
        } else {
            "SELECT COALESCE(provider_name, provider_id, '') AS provider, COUNT(*) AS request_count, CAST(COALESCE(SUM(CASE WHEN client_status_code >= 400 THEN 1 ELSE 0 END), 0) AS SIGNED) AS error_count, CAST(COALESCE(AVG(latency_total_ms), 0) AS DOUBLE) AS avg_duration_ms FROM request_logs GROUP BY COALESCE(provider_name, provider_id, '') ORDER BY request_count DESC".to_string()
        };
        Ok(sqlx::query_as::<_, ProviderStats>(&sql)
            .fetch_all(&self.pool)
            .await?)
    }
}

// ---------------------------------------------------------------------------
// Bootstrap
// ---------------------------------------------------------------------------

#[derive(Clone)]
struct MysqlBootstrap {
    adapter: MysqlAdapter,
}

#[async_trait]
impl StorageBootstrap for MysqlBootstrap {
    async fn init(&self) -> anyhow::Result<()> {
        self.adapter.ping().await
    }

    async fn migrate(&self) -> anyhow::Result<()> {
        let pool = self.adapter.pool();

        sqlx::raw_sql(MYSQL_INIT_SQL).execute(pool).await?;

        // Add balance column to routes
        mysql_add_column_if_not_exists(
            pool,
            "routes",
            "balance",
            "VARCHAR(255) DEFAULT 'weighted'",
        )
        .await?;
        sqlx::query(
            "UPDATE routes SET balance = 'weighted' WHERE balance IS NULL OR TRIM(balance) = ''",
        )
        .execute(pool)
        .await?;

        // Add use_proxy to providers
        mysql_add_column_if_not_exists(
            pool,
            "providers",
            "use_proxy",
            "TINYINT(1) NOT NULL DEFAULT 0",
        )
        .await?;

        // Add auth_mode to providers
        mysql_add_column_if_not_exists(
            pool,
            "providers",
            "auth_mode",
            "VARCHAR(255) NOT NULL DEFAULT 'apikey'",
        )
        .await?;

        // Add OAuth columns to providers
        mysql_add_column_if_not_exists(pool, "providers", "access_token", "TEXT").await?;
        mysql_add_column_if_not_exists(pool, "providers", "refresh_token", "TEXT").await?;
        mysql_add_column_if_not_exists(pool, "providers", "expires_at", "DATETIME").await?;

        // Auth mode constraint: update api_key → apikey
        sqlx::query("UPDATE providers SET auth_mode = 'apikey' WHERE auth_mode = 'api_key'")
            .execute(pool)
            .await?;

        // Collapse provider protocol columns (MySQL variant)
        migrate_collapse_provider_protocol_columns_mysql(pool).await?;

        // Backfill route_targets
        sqlx::query(
            r#"
            INSERT IGNORE INTO route_targets (id, route_id, provider_id, model, weight, priority)
            SELECT UUID(), r.id, r.target_provider, r.target_model, 100, 1
            FROM routes r
            WHERE r.target_provider IS NOT NULL
              AND TRIM(r.target_provider) != ''
              AND NOT EXISTS (SELECT 1 FROM route_targets rt WHERE rt.route_id = r.id)
            "#,
        )
        .execute(pool)
        .await?;

        // Migrate: providers/routes is_active -> is_enabled
        mysql_add_column_if_not_exists(pool, "providers", "is_enabled", "TINYINT(1) DEFAULT 1")
            .await?;
        sqlx::query("UPDATE providers SET is_enabled = is_active WHERE is_active IS NOT NULL AND is_enabled <> is_active")
            .execute(pool)
            .await
            .ok();

        mysql_add_column_if_not_exists(pool, "routes", "is_enabled", "TINYINT(1) DEFAULT 1")
            .await?;
        sqlx::query("UPDATE routes SET is_enabled = is_active WHERE is_active IS NOT NULL AND is_enabled <> is_active")
            .execute(pool)
            .await
            .ok();

        // Migrate: api_keys status -> is_enabled
        mysql_add_column_if_not_exists(pool, "api_keys", "is_enabled", "TINYINT(1) DEFAULT 1")
            .await?;
        sqlx::query(
            "UPDATE api_keys SET is_enabled = CASE WHEN status = 'active' THEN 1 ELSE 0 END \
             WHERE status IS NOT NULL AND is_enabled <> (status = 'active')",
        )
        .execute(pool)
        .await
        .ok();

        // Migrate OAuth credentials from providers table to new dedicated table
        sqlx::query(
            r#"
            INSERT IGNORE INTO provider_oauth_credentials
                (provider_id, access_token, refresh_token, expires_at, status)
            SELECT id, COALESCE(access_token, ''), refresh_token, expires_at, 'connected'
            FROM providers
            WHERE auth_mode = 'oauth'
              AND (
                (access_token IS NOT NULL AND TRIM(access_token) != '')
                OR (refresh_token IS NOT NULL AND TRIM(refresh_token) != '')
              )
            "#,
        )
        .execute(pool)
        .await?;

        // Vendor name migrations
        for (from, to) in [("nyro", "custom"), ("zhipu", "zhipuai")] {
            sqlx::query("UPDATE providers SET vendor = ? WHERE LOWER(TRIM(vendor)) = ?")
                .bind(to)
                .bind(from)
                .execute(pool)
                .await?;
            sqlx::query("UPDATE providers SET preset_key = ? WHERE LOWER(TRIM(preset_key)) = ?")
                .bind(to)
                .bind(from)
                .execute(pool)
                .await?;
        }

        // Protocol normalization (MySQL variant)
        normalize_provider_protocols_mysql(pool).await?;

        // Drop route_type column
        if mysql_column_exists(pool, "routes", "route_type").await? {
            sqlx::query("ALTER TABLE routes DROP COLUMN route_type")
                .execute(pool)
                .await?;
        }

        // Add cache_read_tokens
        mysql_add_column_if_not_exists(
            pool,
            "request_logs",
            "cache_read_tokens",
            "INTEGER DEFAULT 0",
        )
        .await?;

        // Rename tables: routes → models, route_targets → model_backends, api_key_routes → api_key_models
        mysql_rename_table_if_needed(pool, "routes", "models").await?;
        mysql_rename_table_if_needed(pool, "route_targets", "model_backends").await?;
        mysql_rename_table_if_needed(pool, "api_key_routes", "api_key_models").await?;

        // Rename columns within renamed tables
        mysql_rename_column_if_needed(pool, "model_backends", "route_id", "model_id").await?;
        mysql_rename_column_if_needed(pool, "api_key_models", "route_id", "model_id").await?;

        // Rename columns in request_logs: route_id → model_id, route_name → model_name
        mysql_rename_column_if_needed(pool, "request_logs", "route_id", "model_id").await?;
        mysql_rename_column_if_needed(pool, "request_logs", "route_name", "model_name").await?;

        // Rename column: models strategy → balance
        mysql_rename_column_if_needed(pool, "models", "strategy", "balance").await?;

        // Merge virtual_model into name and drop the column
        if mysql_column_exists(pool, "models", "virtual_model").await? {
            tracing::info!("merging virtual_model into name on models table (mysql)");
            sqlx::query(
                "UPDATE models SET name = TRIM(virtual_model)
                 WHERE virtual_model IS NOT NULL AND TRIM(virtual_model) != ''",
            )
            .execute(pool)
            .await?;
            sqlx::query("ALTER TABLE models DROP COLUMN virtual_model")
                .execute(pool)
                .await?;
        }

        // Rename access_control → enable_auth on models table
        mysql_rename_column_if_needed(pool, "models", "access_control", "enable_auth").await?;

        mysql_add_column_if_not_exists(pool, "models", "enable_payload", "TINYINT(1) DEFAULT NULL")
            .await?;

        // Rename settings key log_record_payloads → enable_payload
        sqlx::query(
            "UPDATE settings SET name = 'enable_payload' WHERE name = 'log_record_payloads'",
        )
        .execute(pool)
        .await
        .ok();

        // Rename columns for compat: settings.key → settings.name, api_keys.key → api_keys.token
        mysql_rename_column_if_needed(pool, "settings", "key", "name").await?;
        mysql_rename_column_if_needed(pool, "api_keys", "key", "token").await?;

        Ok(())
    }

    async fn health(&self) -> anyhow::Result<StorageHealth> {
        let health = self.adapter.health().await;
        Ok(StorageHealth {
            backend: StorageBackend::Mysql,
            can_connect: health.can_connect,
            schema_compatible: health.schema_compatible,
            writable: health.can_connect,
        })
    }
}

// ---------------------------------------------------------------------------
// Migration helpers
// ---------------------------------------------------------------------------

async fn mysql_column_exists(
    pool: &Pool<MySql>,
    table_name: &str,
    column_name: &str,
) -> anyhow::Result<bool> {
    Ok(sqlx::query_scalar::<_, i64>(
        "SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = ? AND column_name = ?",
    )
    .bind(table_name)
    .bind(column_name)
    .fetch_one(pool)
    .await?
    > 0)
}

async fn mysql_table_exists(pool: &Pool<MySql>, table_name: &str) -> anyhow::Result<bool> {
    Ok(sqlx::query_scalar::<_, i64>(
        "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = ?",
    )
    .bind(table_name)
    .fetch_one(pool)
    .await?
    > 0)
}

async fn mysql_rename_table_if_needed(
    pool: &Pool<MySql>,
    old: &str,
    new: &str,
) -> anyhow::Result<()> {
    if mysql_table_exists(pool, old).await? && !mysql_table_exists(pool, new).await? {
        tracing::info!("renaming table {old} -> {new}");
        sqlx::query(&format!("RENAME TABLE `{old}` TO `{new}`"))
            .execute(pool)
            .await?;
    }
    Ok(())
}

async fn mysql_rename_column_if_needed(
    pool: &Pool<MySql>,
    table: &str,
    old: &str,
    new: &str,
) -> anyhow::Result<()> {
    if mysql_column_exists(pool, table, old).await?
        && !mysql_column_exists(pool, table, new).await?
    {
        tracing::info!("renaming column {table}.{old} -> {table}.{new}");
        sqlx::query(&format!(
            "ALTER TABLE `{table}` RENAME COLUMN `{old}` TO `{new}`"
        ))
        .execute(pool)
        .await?;
    }
    Ok(())
}

async fn mysql_add_column_if_not_exists(
    pool: &Pool<MySql>,
    table: &str,
    column: &str,
    definition: &str,
) -> anyhow::Result<()> {
    if !mysql_column_exists(pool, table, column).await? {
        sqlx::query(&format!(
            "ALTER TABLE `{table}` ADD COLUMN `{column}` {definition}"
        ))
        .execute(pool)
        .await?;
    }
    Ok(())
}

// ---------------------------------------------------------------------------
// Provider protocol collapse (MySQL variant)
// ---------------------------------------------------------------------------

async fn migrate_collapse_provider_protocol_columns_mysql(
    pool: &Pool<MySql>,
) -> anyhow::Result<()> {
    let has_default_protocol = mysql_column_exists(pool, "providers", "default_protocol").await?;
    let has_protocol_endpoints =
        mysql_column_exists(pool, "providers", "protocol_endpoints").await?;
    if !has_default_protocol && !has_protocol_endpoints {
        return Ok(());
    }

    if has_default_protocol {
        sqlx::query(
            "UPDATE providers \
             SET protocol = TRIM(default_protocol) \
             WHERE default_protocol IS NOT NULL AND TRIM(default_protocol) != ''",
        )
        .execute(pool)
        .await?;
    }

    if has_protocol_endpoints {
        let rows: Vec<(String, String, String, Option<String>)> =
            sqlx::query_as("SELECT id, protocol, base_url, protocol_endpoints FROM providers")
                .fetch_all(pool)
                .await?;
        for (id, protocol, base_url, raw_endpoints) in rows {
            if !base_url.trim().is_empty() {
                continue;
            }
            if let Some(next_base_url) =
                base_url_from_protocol_endpoints(raw_endpoints.as_deref().unwrap_or(""), &protocol)
            {
                sqlx::query("UPDATE providers SET base_url = ? WHERE id = ?")
                    .bind(next_base_url)
                    .bind(id)
                    .execute(pool)
                    .await?;
            }
        }
    }

    if mysql_column_exists(pool, "providers", "protocol_endpoints").await? {
        sqlx::query("ALTER TABLE providers DROP COLUMN protocol_endpoints")
            .execute(pool)
            .await?;
    }
    if mysql_column_exists(pool, "providers", "default_protocol").await? {
        sqlx::query("ALTER TABLE providers DROP COLUMN default_protocol")
            .execute(pool)
            .await?;
    }

    Ok(())
}

fn base_url_from_protocol_endpoints(raw: &str, protocol: &str) -> Option<String> {
    let reg = crate::protocol::registry::ProtocolRegistry::global();
    let target = reg.parse_protocol(protocol)?;
    let value = serde_json::from_str::<serde_json::Value>(raw.trim()).ok()?;
    let obj = value.as_object()?;
    let mut skipped = 0usize;
    let mut matched = None;
    for (key, entry) in obj {
        let Some(entry_protocol) = reg.parse_protocol(key) else {
            skipped += 1;
            continue;
        };
        if entry_protocol == target {
            matched = entry
                .as_object()
                .and_then(|object| object.get("base_url"))
                .and_then(|value| value.as_str())
                .map(str::trim)
                .filter(|value| !value.is_empty())
                .map(ToString::to_string);
            if matched.is_some() {
                break;
            }
        } else {
            skipped += 1;
        }
    }
    if skipped > 0 {
        tracing::warn!(
            protocol = protocol,
            skipped_entries = skipped,
            "dropping non-selected protocol_endpoints entries during provider protocol collapse (mysql)"
        );
    }
    matched
}

/// MySQL counterpart of provider protocol normalization.
async fn normalize_provider_protocols_mysql(pool: &Pool<MySql>) -> anyhow::Result<()> {
    use crate::protocol::registry::ProtocolRegistry;

    let reg = ProtocolRegistry::global();
    let rows: Vec<(String, String)> = sqlx::query_as("SELECT id, protocol FROM providers")
        .fetch_all(pool)
        .await?;

    for (id, raw_protocol) in rows {
        let new_protocol = normalize_provider_protocol_value(reg, &raw_protocol);
        if new_protocol == raw_protocol {
            continue;
        }

        tracing::info!(
            provider_id = %id,
            old_protocol = %raw_protocol,
            new_protocol = %new_protocol,
            "normalizing provider protocol identifier (mysql)"
        );

        sqlx::query("UPDATE providers SET protocol = ? WHERE id = ?")
            .bind(&new_protocol)
            .bind(&id)
            .execute(pool)
            .await?;
    }
    Ok(())
}

fn normalize_provider_protocol_value(
    reg: &crate::protocol::registry::ProtocolRegistry,
    raw: &str,
) -> String {
    let trimmed = raw.trim();
    if trimmed.is_empty() {
        return String::new();
    }
    match reg.parse_protocol(trimmed) {
        Some(protocol) => protocol.as_str().to_string(),
        None => {
            tracing::warn!(
                value = trimmed,
                "leaving unrecognized provider protocol identifier unchanged (mysql)"
            );
            trimmed.to_string()
        }
    }
}

// ---------------------------------------------------------------------------
// Select helpers
// ---------------------------------------------------------------------------

fn provider_select(suffix: Option<&str>) -> String {
    let mut sql = String::from(
        "SELECT id, name, vendor, protocol, base_url, preset_key, channel, models_source, static_models, api_key, COALESCE(auth_mode, 'apikey') AS auth_mode, COALESCE(use_proxy, 0) AS use_proxy, last_test_success, DATE_FORMAT(last_test_at, '%Y-%m-%d %H:%i:%S') AS last_test_at, COALESCE(is_enabled, 1) AS is_enabled, DATE_FORMAT(created_at, '%Y-%m-%d %H:%i:%S') AS created_at, DATE_FORMAT(updated_at, '%Y-%m-%d %H:%i:%S') AS updated_at FROM providers",
    );
    if let Some(suffix) = suffix {
        sql.push(' ');
        sql.push_str(suffix);
    } else {
        sql.push_str(" ORDER BY created_at DESC");
    }
    sql
}

fn model_select(suffix: Option<&str>) -> String {
    let mut sql = String::from(
        "SELECT id, name, COALESCE(balance, 'weighted') AS balance, target_provider, target_model, COALESCE(enable_auth, 0) AS enable_auth, enable_payload, COALESCE(is_enabled, 1) AS is_enabled, DATE_FORMAT(created_at, '%Y-%m-%d %H:%i:%S') AS created_at FROM models",
    );
    if let Some(suffix) = suffix {
        sql.push(' ');
        sql.push_str(suffix);
    }
    sql
}

fn api_key_select(suffix: Option<&str>) -> String {
    let mut sql = String::from(
        "SELECT id, token, name, rpm, rpd, tpm, tpd, COALESCE(is_enabled, 1) AS is_enabled, DATE_FORMAT(expires_at, '%Y-%m-%d %H:%i:%S') AS expires_at, DATE_FORMAT(created_at, '%Y-%m-%d %H:%i:%S') AS created_at, DATE_FORMAT(updated_at, '%Y-%m-%d %H:%i:%S') AS updated_at FROM api_keys",
    );
    if let Some(suffix) = suffix {
        sql.push(' ');
        sql.push_str(suffix);
    } else {
        sql.push_str(" ORDER BY created_at DESC");
    }
    sql
}

fn api_key_with_bindings(row: ApiKey, model_ids: Vec<String>) -> ApiKeyWithBindings {
    ApiKeyWithBindings {
        id: row.id,
        token: row.token,
        name: row.name,
        rpm: row.rpm,
        rpd: row.rpd,
        tpm: row.tpm,
        tpd: row.tpd,
        is_enabled: row.is_enabled,
        expires_at: row.expires_at,
        created_at: row.created_at,
        updated_at: row.updated_at,
        model_ids,
    }
}

fn normalize_provider_vendor(vendor: Option<&str>) -> Option<String> {
    vendor
        .map(str::trim)
        .filter(|v| !v.is_empty() && *v != "custom")
        .map(|v| v.to_lowercase())
}

async fn list_api_key_model_ids(
    pool: &Pool<MySql>,
    api_key_id: &str,
) -> anyhow::Result<Vec<String>> {
    Ok(sqlx::query_scalar::<_, String>(
        "SELECT model_id FROM api_key_models WHERE api_key_id = ? ORDER BY model_id ASC",
    )
    .bind(api_key_id)
    .fetch_all(pool)
    .await?)
}

async fn replace_api_key_models(
    pool: &Pool<MySql>,
    api_key_id: &str,
    model_ids: &[String],
) -> anyhow::Result<()> {
    let mut tx = pool.begin().await?;
    sqlx::query("DELETE FROM api_key_models WHERE api_key_id = ?")
        .bind(api_key_id)
        .execute(&mut *tx)
        .await?;

    for model_id in model_ids.iter().filter(|id| !id.trim().is_empty()) {
        sqlx::query("INSERT IGNORE INTO api_key_models (api_key_id, model_id) VALUES (?, ?)")
            .bind(api_key_id)
            .bind(model_id.trim())
            .execute(&mut *tx)
            .await?;
    }

    tx.commit().await?;
    Ok(())
}

// ---------------------------------------------------------------------------
// DDL
// ---------------------------------------------------------------------------

const MYSQL_INIT_SQL: &str = r#"
CREATE TABLE IF NOT EXISTS providers (
    id VARCHAR(36) PRIMARY KEY,
    name VARCHAR(255) NOT NULL,
    vendor VARCHAR(255),
    protocol VARCHAR(255) NOT NULL,
    base_url TEXT NOT NULL,
    preset_key VARCHAR(255),
    channel VARCHAR(255),
    models_source TEXT,
    static_models TEXT,
    api_key TEXT NOT NULL,
    auth_mode VARCHAR(255) NOT NULL DEFAULT 'apikey',
    access_token TEXT,
    refresh_token TEXT,
    expires_at DATETIME,
    use_proxy TINYINT(1) NOT NULL DEFAULT 0,
    last_test_success TINYINT(1),
    last_test_at DATETIME,
    is_enabled TINYINT(1) DEFAULT 1,
    priority INTEGER DEFAULT 0,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS routes (
    id VARCHAR(36) PRIMARY KEY,
    name VARCHAR(255) NOT NULL,
    balance VARCHAR(255) DEFAULT 'weighted',
    target_provider VARCHAR(36) NOT NULL,
    target_model VARCHAR(255) NOT NULL,
    enable_auth TINYINT(1) DEFAULT 0,
    enable_payload TINYINT(1) DEFAULT NULL,
    is_enabled TINYINT(1) DEFAULT 1,
    priority INTEGER DEFAULT 0,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (target_provider) REFERENCES providers(id)
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS route_targets (
    id VARCHAR(36) PRIMARY KEY,
    route_id VARCHAR(36) NOT NULL,
    provider_id VARCHAR(36) NOT NULL,
    model VARCHAR(255) NOT NULL,
    weight INTEGER DEFAULT 100,
    priority INTEGER DEFAULT 1,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (route_id) REFERENCES routes(id) ON DELETE CASCADE,
    FOREIGN KEY (provider_id) REFERENCES providers(id)
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

CREATE INDEX idx_route_targets_route_id ON route_targets(route_id);

CREATE TABLE IF NOT EXISTS request_logs (
    id                        VARCHAR(36) PRIMARY KEY,
    created_at                BIGINT NOT NULL DEFAULT 0,
    api_key_id                VARCHAR(36),
    api_key_name              VARCHAR(255),
    client_protocol           VARCHAR(255),
    upstream_protocol         VARCHAR(255),
    provider_id               VARCHAR(36),
    provider_name             VARCHAR(255),
    model_id                  VARCHAR(36),
    model_name                VARCHAR(255),
    upstream_url              TEXT,
    client_model              VARCHAR(255),
    upstream_model            VARCHAR(255),
    method                    VARCHAR(255),
    path                      TEXT,
    client_request_headers    TEXT,
    client_request_body       LONGTEXT,
    client_response_headers   TEXT,
    client_response_body      LONGTEXT,
    upstream_request_headers  TEXT,
    upstream_request_body     LONGTEXT,
    upstream_response_headers TEXT,
    upstream_response_body    LONGTEXT,
    upstream_status_code      INTEGER,
    client_status_code        INTEGER,
    latency_total_ms          BIGINT,
    latency_upstream_ms       BIGINT,
    input_tokens              INTEGER DEFAULT 0,
    output_tokens             INTEGER DEFAULT 0,
    cache_read_tokens         INTEGER DEFAULT 0,
    is_stream                 TINYINT(1) DEFAULT 0,
    stream_chunks_count       INTEGER DEFAULT 0,
    stream_first_chunk_ms     BIGINT
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

CREATE INDEX idx_logs_created_at ON request_logs(created_at);
CREATE INDEX idx_logs_provider_id ON request_logs(provider_id);
CREATE INDEX idx_logs_client_status ON request_logs(client_status_code);
CREATE INDEX idx_logs_upstream_model ON request_logs(upstream_model);
CREATE INDEX idx_logs_api_key ON request_logs(api_key_id);

CREATE TABLE IF NOT EXISTS settings (
    name VARCHAR(255) PRIMARY KEY,
    value TEXT NOT NULL,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS api_keys (
    id VARCHAR(36) PRIMARY KEY,
    token VARCHAR(255) NOT NULL UNIQUE,
    name VARCHAR(255) NOT NULL,
    rpm INTEGER,
    rpd INTEGER,
    tpm INTEGER,
    tpd INTEGER,
    is_enabled TINYINT(1) DEFAULT 1,
    expires_at DATETIME,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS api_key_routes (
    api_key_id VARCHAR(36) NOT NULL,
    route_id VARCHAR(36) NOT NULL,
    PRIMARY KEY (api_key_id, route_id),
    FOREIGN KEY (api_key_id) REFERENCES api_keys(id) ON DELETE CASCADE,
    FOREIGN KEY (route_id) REFERENCES routes(id) ON DELETE CASCADE
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

CREATE INDEX idx_api_keys_token ON api_keys(token);
CREATE INDEX idx_api_key_routes_route_id ON api_key_routes(route_id);

CREATE TABLE IF NOT EXISTS provider_oauth_credentials (
    provider_id       VARCHAR(36) PRIMARY KEY,
    driver_key        TEXT NOT NULL,
    scheme            VARCHAR(255) NOT NULL DEFAULT '',
    access_token      TEXT NOT NULL,
    refresh_token     TEXT,
    expires_at        DATETIME,
    resource_url      TEXT,
    subject_id        VARCHAR(255),
    scopes            TEXT NOT NULL,
    meta              TEXT NOT NULL,
    status            VARCHAR(255) NOT NULL DEFAULT 'connected',
    status_version    INTEGER NOT NULL DEFAULT 0,
    last_error        TEXT,
    last_refresh_at   DATETIME,
    created_at        DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at        DATETIME DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    FOREIGN KEY (provider_id) REFERENCES providers(id) ON DELETE CASCADE
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

CREATE INDEX idx_oauth_creds_status ON provider_oauth_credentials(status);
CREATE INDEX idx_oauth_creds_expires ON provider_oauth_credentials(expires_at);
"#;
