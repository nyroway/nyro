use std::sync::Arc;

use anyhow::Context;
use async_trait::async_trait;
use sqlx::SqlitePool;
use std::time::Duration;

use crate::config::GatewayConfig;
use crate::db;
use crate::db::models::{
    ApiKey, ApiKeyWithBindings, CreateApiKey, CreateModel, CreateModelBackend, CreateProvider,
    LogPage, LogQuery, Model, ModelBackend, ModelStats, OAuthCredential, Provider, ProviderStats,
    RequestLog, StatsHourly, StatsOverview, UpdateApiKey, UpdateModel, UpdateProvider,
    UpsertOAuthCredential, is_valid_provider_auth_mode,
};
use crate::logging::LogEntry;
use crate::storage::traits::{
    ApiKeyAccessRecord, ApiKeyStore, AuthAccessStore, LogStore, ModelBackendStore,
    ModelSnapshotStore, ModelStore, OAuthCredentialStore, ProviderStore, ProviderTestResult,
    SettingsStore, Storage, StorageBackend, StorageBootstrap, StorageHealth, UsageWindow,
};

#[derive(Clone)]
pub struct SqliteStorage {
    pool: SqlitePool,
    provider_store: Arc<SqliteProviderStore>,
    model_store: Arc<SqliteModelStore>,
    model_backend_store: Arc<SqliteModelBackendStore>,
    settings_store: Arc<SqliteSettingsStore>,
    api_key_store: Arc<SqliteApiKeyStore>,
    auth_store: Arc<SqliteAuthAccessStore>,
    oauth_credential_store: Arc<SqliteOAuthCredentialStore>,
    log_store: Arc<SqliteLogStore>,
    bootstrap: Arc<SqliteBootstrap>,
}

impl SqliteStorage {
    pub async fn from_config(config: &GatewayConfig) -> anyhow::Result<Self> {
        let pool = db::init_pool(&config.data_dir).await?;
        let storage = Self::from_pool(pool);
        storage.bootstrap().migrate().await?;
        Ok(storage)
    }

    pub fn from_pool(pool: SqlitePool) -> Self {
        let provider_store = Arc::new(SqliteProviderStore { pool: pool.clone() });
        let model_store = Arc::new(SqliteModelStore { pool: pool.clone() });
        let model_backend_store = Arc::new(SqliteModelBackendStore { pool: pool.clone() });
        let settings_store = Arc::new(SqliteSettingsStore { pool: pool.clone() });
        let api_key_store = Arc::new(SqliteApiKeyStore { pool: pool.clone() });
        let auth_store = Arc::new(SqliteAuthAccessStore { pool: pool.clone() });
        let oauth_credential_store = Arc::new(SqliteOAuthCredentialStore { pool: pool.clone() });
        let log_store = Arc::new(SqliteLogStore { pool: pool.clone() });
        let bootstrap = Arc::new(SqliteBootstrap { pool: pool.clone() });
        Self {
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
        }
    }

    pub fn pool(&self) -> &SqlitePool {
        &self.pool
    }
}

impl Storage for SqliteStorage {
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

#[derive(Clone)]
struct SqliteOAuthCredentialStore {
    pool: SqlitePool,
}

#[async_trait]
impl OAuthCredentialStore for SqliteOAuthCredentialStore {
    async fn get(&self, provider_id: &str) -> anyhow::Result<Option<OAuthCredential>> {
        let row = sqlx::query_as::<_, OAuthCredential>(
            r#"SELECT provider_id, driver_key, scheme, access_token, refresh_token,
                      expires_at, resource_url, subject_id, scopes, meta,
                      status, status_version, last_error, last_refresh_at,
                      created_at, updated_at
               FROM provider_oauth_credentials WHERE provider_id = ?"#,
        )
        .bind(provider_id)
        .fetch_optional(&self.pool)
        .await?;
        Ok(row)
    }

    async fn upsert(
        &self,
        provider_id: &str,
        input: UpsertOAuthCredential,
    ) -> anyhow::Result<OAuthCredential> {
        sqlx::query(
            r#"INSERT INTO provider_oauth_credentials
                   (provider_id, driver_key, scheme, access_token, refresh_token,
                    expires_at, resource_url, subject_id, scopes, meta,
                    status, status_version, last_error, created_at, updated_at)
               VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'connected', 0, NULL, datetime('now'), datetime('now'))
               ON CONFLICT(provider_id) DO UPDATE SET
                   driver_key = excluded.driver_key,
                   scheme = excluded.scheme,
                   access_token = excluded.access_token,
                   refresh_token = excluded.refresh_token,
                   expires_at = excluded.expires_at,
                   resource_url = excluded.resource_url,
                   subject_id = excluded.subject_id,
                   scopes = excluded.scopes,
                   meta = excluded.meta,
                   status = 'connected',
                   status_version = status_version + 1,
                   last_error = NULL,
                   updated_at = datetime('now')
            "#,
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
            r#"UPDATE provider_oauth_credentials
               SET status = 'refreshing', status_version = status_version + 1,
                   updated_at = datetime('now')
               WHERE provider_id = ? AND status = 'connected' AND status_version = ?"#,
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
            r#"UPDATE provider_oauth_credentials SET
                   driver_key = ?, scheme = ?,
                   access_token = ?, refresh_token = ?, expires_at = ?,
                   resource_url = ?, subject_id = ?,
                   scopes = ?, meta = ?,
                   status = 'connected', status_version = status_version + 1,
                   last_error = NULL, last_refresh_at = datetime('now'),
                   updated_at = datetime('now')
               WHERE provider_id = ?"#,
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
            r#"UPDATE provider_oauth_credentials SET
                   status = 'error', last_error = ?,
                   status_version = status_version + 1,
                   updated_at = datetime('now')
               WHERE provider_id = ?"#,
        )
        .bind(error_message)
        .bind(provider_id)
        .execute(&self.pool)
        .await?;
        Ok(())
    }

    async fn list_expiring(&self, before: Duration) -> anyhow::Result<Vec<OAuthCredential>> {
        let seconds = before.as_secs() as i64;
        let rows = sqlx::query_as::<_, OAuthCredential>(
            r#"SELECT provider_id, driver_key, scheme, access_token, refresh_token,
                      expires_at, resource_url, subject_id, scopes, meta,
                      status, status_version, last_error, last_refresh_at,
                      created_at, updated_at
               FROM provider_oauth_credentials
               WHERE status = 'connected'
                 AND expires_at IS NOT NULL
                 AND datetime(expires_at) <= datetime('now', '+' || ? || ' seconds')"#,
        )
        .bind(seconds)
        .fetch_all(&self.pool)
        .await?;
        Ok(rows)
    }

    async fn recover_stale_refreshing(&self, timeout: Duration) -> anyhow::Result<u64> {
        let seconds = timeout.as_secs() as i64;
        let result = sqlx::query(
            r#"UPDATE provider_oauth_credentials SET
                   status = 'error',
                   last_error = 'refresh timeout: process did not complete within timeout',
                   status_version = status_version + 1,
                   updated_at = datetime('now')
               WHERE status = 'refreshing'
                 AND datetime(updated_at, '+' || ? || ' seconds') < datetime('now')"#,
        )
        .bind(seconds)
        .execute(&self.pool)
        .await?;
        Ok(result.rows_affected())
    }
}

#[derive(Clone)]
struct SqliteProviderStore {
    pool: SqlitePool,
}

#[async_trait]
impl ProviderStore for SqliteProviderStore {
    async fn list(&self) -> anyhow::Result<Vec<Provider>> {
        Ok(sqlx::query_as::<_, Provider>(
            "SELECT id, name, vendor, protocol, base_url, preset_key, channel, models_source, static_models, api_key, COALESCE(auth_mode, 'apikey') AS auth_mode, COALESCE(use_proxy, 0) AS use_proxy, last_test_success, last_test_at, COALESCE(is_enabled, 1) AS is_enabled, created_at, updated_at FROM providers ORDER BY created_at DESC",
        )
        .fetch_all(&self.pool)
        .await?)
    }

    async fn get(&self, id: &str) -> anyhow::Result<Option<Provider>> {
        Ok(sqlx::query_as::<_, Provider>(
            "SELECT id, name, vendor, protocol, base_url, preset_key, channel, models_source, static_models, api_key, COALESCE(auth_mode, 'apikey') AS auth_mode, COALESCE(use_proxy, 0) AS use_proxy, last_test_success, last_test_at, COALESCE(is_enabled, 1) AS is_enabled, created_at, updated_at FROM providers WHERE id = ?",
        )
        .bind(id)
        .fetch_optional(&self.pool)
        .await?)
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
        .bind(&input.name)
        .bind(&vendor)
        .bind(&input.protocol)
        .bind(&input.base_url)
        .bind(&input.preset_key)
        .bind(&input.channel)
        .bind(&models_source)
        .bind(&input.static_models)
        .bind(&input.api_key)
        .bind(&input.auth_mode)
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
            "UPDATE providers SET name=?, vendor=?, protocol=?, base_url=?, preset_key=?, channel=?, models_source=?, static_models=?, api_key=?, auth_mode=?, use_proxy=?, is_enabled=?, updated_at=datetime('now') WHERE id=?",
        )
        .bind(name)
        .bind(vendor)
        .bind(&protocol)
        .bind(base_url)
        .bind(preset_key)
        .bind(channel)
        .bind(&models_source)
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
        let sql = if exclude_id.is_some() {
            "SELECT id FROM providers WHERE lower(trim(name)) = lower(trim(?)) AND id != ? LIMIT 1"
        } else {
            "SELECT id FROM providers WHERE lower(trim(name)) = lower(trim(?)) LIMIT 1"
        };
        let row = if let Some(exclude_id) = exclude_id {
            sqlx::query_scalar::<_, String>(sql)
                .bind(name)
                .bind(exclude_id)
                .fetch_optional(&self.pool)
                .await?
        } else {
            sqlx::query_scalar::<_, String>(sql)
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
        sqlx::query(
            "UPDATE providers SET last_test_success = ?, last_test_at = datetime('now') WHERE id = ?",
        )
        .bind(result.success)
        .bind(provider_id)
        .execute(&self.pool)
        .await?;
        Ok(())
    }
}

fn normalize_provider_vendor(vendor: Option<&str>) -> Option<String> {
    vendor
        .map(str::trim)
        .filter(|v| !v.is_empty() && *v != "custom")
        .map(|v| v.to_lowercase())
}

#[derive(Clone)]
struct SqliteModelStore {
    pool: SqlitePool,
}

#[async_trait]
impl ModelStore for SqliteModelStore {
    async fn list(&self) -> anyhow::Result<Vec<Model>> {
        Ok(sqlx::query_as::<_, Model>(
            "SELECT id, name, COALESCE(balance, 'weighted') AS balance, target_provider, target_model, COALESCE(enable_auth, 0) AS enable_auth, enable_payload, COALESCE(is_enabled, 1) AS is_enabled, created_at FROM models ORDER BY created_at DESC",
        )
        .fetch_all(&self.pool)
        .await?)
    }

    async fn get(&self, id: &str) -> anyhow::Result<Option<Model>> {
        Ok(sqlx::query_as::<_, Model>(
            "SELECT id, name, COALESCE(balance, 'weighted') AS balance, target_provider, target_model, COALESCE(enable_auth, 0) AS enable_auth, enable_payload, COALESCE(is_enabled, 1) AS is_enabled, created_at FROM models WHERE id = ?",
        )
        .bind(id)
        .fetch_optional(&self.pool)
        .await?)
    }

    async fn create(&self, input: CreateModel) -> anyhow::Result<Model> {
        let id = uuid::Uuid::new_v4().to_string();
        let balance = input.balance.unwrap_or_else(|| "weighted".to_string());
        sqlx::query(
            "INSERT INTO models (id, name, balance, target_provider, target_model, enable_auth, enable_payload) VALUES (?, ?, ?, ?, ?, ?, ?)",
        )
        .bind(&id)
        .bind(input.name.trim())
        .bind(balance)
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
        let sql = if exclude_id.is_some() {
            "SELECT id FROM models WHERE lower(trim(name)) = lower(trim(?)) AND id != ? LIMIT 1"
        } else {
            "SELECT id FROM models WHERE lower(trim(name)) = lower(trim(?)) LIMIT 1"
        };
        let row = if let Some(exclude_id) = exclude_id {
            sqlx::query_scalar::<_, String>(sql)
                .bind(name)
                .bind(exclude_id)
                .fetch_optional(&self.pool)
                .await?
        } else {
            sqlx::query_scalar::<_, String>(sql)
                .bind(name)
                .fetch_optional(&self.pool)
                .await?
        };
        Ok(row.is_some())
    }
}

#[async_trait]
impl ModelSnapshotStore for SqliteModelStore {
    async fn load_active_snapshot(&self) -> anyhow::Result<Vec<Model>> {
        Ok(sqlx::query_as::<_, Model>(
            r#"SELECT
                id, name,
                COALESCE(balance, 'weighted') AS balance,
                target_provider, target_model,
                COALESCE(enable_auth, 0) AS enable_auth,
                enable_payload,
                COALESCE(is_enabled, 1) AS is_enabled,
                created_at
            FROM models
            WHERE COALESCE(is_enabled, 1) = 1"#,
        )
        .fetch_all(&self.pool)
        .await?)
    }
}

#[derive(Clone)]
struct SqliteModelBackendStore {
    pool: SqlitePool,
}

#[async_trait]
impl ModelBackendStore for SqliteModelBackendStore {
    async fn list_backends_by_model(&self, model_id: &str) -> anyhow::Result<Vec<ModelBackend>> {
        Ok(sqlx::query_as::<_, ModelBackend>(
            "SELECT id, model_id, provider_id, model, weight, priority, created_at FROM model_backends WHERE model_id = ? ORDER BY priority ASC, created_at ASC",
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

#[derive(Clone)]
struct SqliteSettingsStore {
    pool: SqlitePool,
}

#[async_trait]
impl SettingsStore for SqliteSettingsStore {
    async fn get(&self, key: &str) -> anyhow::Result<Option<String>> {
        let row: Option<(String,)> = sqlx::query_as("SELECT value FROM settings WHERE name = ?")
            .bind(key)
            .fetch_optional(&self.pool)
            .await?;
        Ok(row.map(|r| r.0))
    }

    async fn set(&self, key: &str, value: &str) -> anyhow::Result<()> {
        sqlx::query(
            "INSERT INTO settings (name, value, updated_at) VALUES (?, ?, datetime('now')) ON CONFLICT(name) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at",
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

#[derive(Clone)]
struct SqliteApiKeyStore {
    pool: SqlitePool,
}

#[async_trait]
impl ApiKeyStore for SqliteApiKeyStore {
    async fn list(&self) -> anyhow::Result<Vec<ApiKeyWithBindings>> {
        let rows = sqlx::query_as::<_, ApiKey>(
            "SELECT id, token, name, rpm, rpd, tpm, tpd, COALESCE(is_enabled, 1) AS is_enabled, expires_at, created_at, updated_at FROM api_keys ORDER BY created_at DESC",
        )
        .fetch_all(&self.pool)
        .await?;

        let mut items = Vec::with_capacity(rows.len());
        for row in rows {
            let model_ids = list_api_key_model_ids(&self.pool, &row.id).await?;
            items.push(ApiKeyWithBindings {
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
            });
        }
        Ok(items)
    }

    async fn get(&self, id: &str) -> anyhow::Result<Option<ApiKeyWithBindings>> {
        let row = sqlx::query_as::<_, ApiKey>(
            "SELECT id, token, name, rpm, rpd, tpm, tpd, COALESCE(is_enabled, 1) AS is_enabled, expires_at, created_at, updated_at FROM api_keys WHERE id = ?",
        )
        .bind(id)
        .fetch_optional(&self.pool)
        .await?;

        let Some(row) = row else {
            return Ok(None);
        };
        let model_ids = list_api_key_model_ids(&self.pool, id).await?;
        Ok(Some(ApiKeyWithBindings {
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
        }))
    }

    async fn create(&self, input: CreateApiKey) -> anyhow::Result<ApiKeyWithBindings> {
        let id = uuid::Uuid::new_v4().to_string();
        let key = format!("sk-{}", uuid::Uuid::new_v4().simple());
        sqlx::query(
            "INSERT INTO api_keys (id, token, name, rpm, rpd, tpm, tpd, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
        )
        .bind(&id)
        .bind(&key)
        .bind(input.name.trim())
        .bind(input.rpm)
        .bind(input.rpd)
        .bind(input.tpm)
        .bind(input.tpd)
        .bind(input.expires_at.as_ref().map(|v| v.trim()).filter(|v| !v.is_empty()))
        .execute(&self.pool)
        .await?;

        replace_api_key_models(&self.pool, &id, &input.model_ids).await?;
        self.get(&id).await?.context("api key missing after create")
    }

    async fn update(&self, id: &str, input: UpdateApiKey) -> anyhow::Result<ApiKeyWithBindings> {
        let current = sqlx::query_as::<_, ApiKey>(
            "SELECT id, token, name, rpm, rpd, tpm, tpd, COALESCE(is_enabled, 1) AS is_enabled, expires_at, created_at, updated_at FROM api_keys WHERE id = ?",
        )
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
            "UPDATE api_keys SET name=?, rpm=?, rpd=?, tpm=?, tpd=?, is_enabled=?, expires_at=?, updated_at=datetime('now') WHERE id=?",
        )
        .bind(name.trim())
        .bind(rpm)
        .bind(rpd)
        .bind(tpm)
        .bind(tpd)
        .bind(is_enabled)
        .bind(expires_at.as_ref().map(|v| v.trim()).filter(|v| !v.is_empty()))
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
        let sql = if exclude_id.is_some() {
            "SELECT id FROM api_keys WHERE lower(trim(name)) = lower(trim(?)) AND id != ? LIMIT 1"
        } else {
            "SELECT id FROM api_keys WHERE lower(trim(name)) = lower(trim(?)) LIMIT 1"
        };

        let row = if let Some(exclude_id) = exclude_id {
            sqlx::query_scalar::<_, String>(sql)
                .bind(name)
                .bind(exclude_id)
                .fetch_optional(&self.pool)
                .await?
        } else {
            sqlx::query_scalar::<_, String>(sql)
                .bind(name)
                .fetch_optional(&self.pool)
                .await?
        };
        Ok(row.is_some())
    }
}

#[derive(Clone)]
struct SqliteAuthAccessStore {
    pool: SqlitePool,
}

#[async_trait]
impl AuthAccessStore for SqliteAuthAccessStore {
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
        >("SELECT id, COALESCE(name, '') AS name, COALESCE(is_enabled, 1) AS is_enabled, expires_at, rpm, rpd, tpm, tpd FROM api_keys WHERE token = ?")
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
        let expr = match window {
            UsageWindow::Minute => "-1 minute",
            UsageWindow::Day => "-1 day",
        };
        Ok(sqlx::query_scalar::<_, i64>(
            "SELECT COUNT(*) FROM request_logs WHERE api_key_id = ? AND created_at >= CAST(strftime('%s', 'now', ?) AS INTEGER) * 1000",
        )
        .bind(api_key_id)
        .bind(expr)
        .fetch_one(&self.pool)
        .await?)
    }

    async fn token_count_since(
        &self,
        api_key_id: &str,
        window: UsageWindow,
    ) -> anyhow::Result<i64> {
        let expr = match window {
            UsageWindow::Minute => "-1 minute",
            UsageWindow::Day => "-1 day",
        };
        Ok(sqlx::query_scalar::<_, i64>(
            "SELECT COALESCE(SUM(input_tokens + output_tokens), 0) FROM request_logs WHERE api_key_id = ? AND created_at >= CAST(strftime('%s', 'now', ?) AS INTEGER) * 1000",
        )
        .bind(api_key_id)
        .bind(expr)
        .fetch_one(&self.pool)
        .await?)
    }
}

async fn list_api_key_model_ids(
    pool: &SqlitePool,
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
    pool: &SqlitePool,
    api_key_id: &str,
    model_ids: &[String],
) -> anyhow::Result<()> {
    let mut tx = pool.begin().await?;
    sqlx::query("DELETE FROM api_key_models WHERE api_key_id = ?")
        .bind(api_key_id)
        .execute(&mut *tx)
        .await?;

    for model_id in model_ids.iter().filter(|id| !id.trim().is_empty()) {
        sqlx::query("INSERT OR IGNORE INTO api_key_models (api_key_id, model_id) VALUES (?, ?)")
            .bind(api_key_id)
            .bind(model_id.trim())
            .execute(&mut *tx)
            .await?;
    }

    tx.commit().await?;
    Ok(())
}

#[derive(Clone)]
struct SqliteLogStore {
    pool: SqlitePool,
}

#[async_trait]
impl LogStore for SqliteLogStore {
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
                VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)"#,
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
        // List query skips the heavy body/header columns (NULL placeholders preserve struct layout).
        let mut data_sql = String::from(
            "SELECT id, COALESCE(CAST(created_at AS INTEGER), 0) AS created_at, api_key_id, api_key_name, \
             client_protocol, upstream_protocol, provider_id, provider_name, model_id, model_name, upstream_url, \
             client_model, upstream_model, method, path, \
             NULL AS client_request_headers, NULL AS client_request_body, \
             NULL AS client_response_headers, NULL AS client_response_body, \
             NULL AS upstream_request_headers, NULL AS upstream_request_body, \
             NULL AS upstream_response_headers, NULL AS upstream_response_body, \
             upstream_status_code, client_status_code, \
             CAST(latency_total_ms AS INTEGER) AS latency_total_ms, latency_upstream_ms, \
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
            "SELECT id, COALESCE(CAST(created_at AS INTEGER), 0) AS created_at, api_key_id, api_key_name, \
             client_protocol, upstream_protocol, provider_id, provider_name, model_id, model_name, upstream_url, \
             client_model, upstream_model, method, path, \
             client_request_headers, client_request_body, \
             client_response_headers, client_response_body, \
             upstream_request_headers, upstream_request_body, \
             upstream_response_headers, upstream_response_body, \
             upstream_status_code, client_status_code, \
             CAST(latency_total_ms AS INTEGER) AS latency_total_ms, latency_upstream_ms, \
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
        // created_at is Unix milliseconds; convert cutoff to ms via strftime.
        let result = sqlx::query(
            "DELETE FROM request_logs WHERE created_at < CAST(strftime('%s', 'now', ?) AS INTEGER) * 1000"
        )
            .bind(cutoff_expression)
            .execute(&self.pool)
            .await?;
        Ok(result.rows_affected())
    }

    async fn stats_overview(&self, hours: Option<i64>) -> anyhow::Result<StatsOverview> {
        if let Some(hours) = hours {
            Ok(sqlx::query_as::<_, StatsOverview>(
                "SELECT COUNT(*) AS total_requests, COALESCE(SUM(input_tokens), 0) AS total_input_tokens, COALESCE(SUM(output_tokens), 0) AS total_output_tokens, COALESCE(AVG(latency_total_ms), 0.0) AS avg_duration_ms, COALESCE(SUM(CASE WHEN client_status_code >= 400 THEN 1 ELSE 0 END), 0) AS error_count FROM request_logs WHERE created_at >= CAST(strftime('%s', 'now', ?) AS INTEGER) * 1000",
            )
            .bind(format!("-{hours} hours"))
            .fetch_one(&self.pool)
            .await?)
        } else {
            Ok(sqlx::query_as::<_, StatsOverview>(
                "SELECT COUNT(*) AS total_requests, COALESCE(SUM(input_tokens), 0) AS total_input_tokens, COALESCE(SUM(output_tokens), 0) AS total_output_tokens, COALESCE(AVG(latency_total_ms), 0.0) AS avg_duration_ms, COALESCE(SUM(CASE WHEN client_status_code >= 400 THEN 1 ELSE 0 END), 0) AS error_count FROM request_logs",
            )
            .fetch_one(&self.pool)
            .await?)
        }
    }

    async fn stats_hourly(&self, hours: i64) -> anyhow::Result<Vec<StatsHourly>> {
        Ok(sqlx::query_as::<_, StatsHourly>(
            "SELECT strftime('%Y-%m-%d %H:00:00', datetime(created_at/1000, 'unixepoch')) AS hour, COUNT(*) AS request_count, COALESCE(SUM(CASE WHEN client_status_code >= 400 THEN 1 ELSE 0 END), 0) AS error_count, COALESCE(SUM(input_tokens), 0) AS total_input_tokens, COALESCE(SUM(output_tokens), 0) AS total_output_tokens, COALESCE(AVG(latency_total_ms), 0.0) AS avg_duration_ms FROM request_logs WHERE created_at >= CAST(strftime('%s', 'now', ?) AS INTEGER) * 1000 GROUP BY hour ORDER BY hour ASC",
        )
        .bind(format!("-{hours} hours"))
        .fetch_all(&self.pool)
        .await?)
    }

    async fn stats_by_model(&self, hours: Option<i64>) -> anyhow::Result<Vec<ModelStats>> {
        if let Some(hours) = hours {
            Ok(sqlx::query_as::<_, ModelStats>(
                "SELECT upstream_model AS model, COUNT(*) AS request_count, COALESCE(SUM(input_tokens), 0) AS total_input_tokens, COALESCE(SUM(output_tokens), 0) AS total_output_tokens, COALESCE(AVG(latency_total_ms), 0.0) AS avg_duration_ms FROM request_logs WHERE created_at >= CAST(strftime('%s', 'now', ?) AS INTEGER) * 1000 GROUP BY upstream_model ORDER BY request_count DESC",
            )
            .bind(format!("-{hours} hours"))
            .fetch_all(&self.pool)
            .await?)
        } else {
            Ok(sqlx::query_as::<_, ModelStats>(
                "SELECT upstream_model AS model, COUNT(*) AS request_count, COALESCE(SUM(input_tokens), 0) AS total_input_tokens, COALESCE(SUM(output_tokens), 0) AS total_output_tokens, COALESCE(AVG(latency_total_ms), 0.0) AS avg_duration_ms FROM request_logs GROUP BY upstream_model ORDER BY request_count DESC",
            )
            .fetch_all(&self.pool)
            .await?)
        }
    }

    async fn stats_by_provider(&self, hours: Option<i64>) -> anyhow::Result<Vec<ProviderStats>> {
        if let Some(hours) = hours {
            Ok(sqlx::query_as::<_, ProviderStats>(
                "SELECT COALESCE(provider_name, provider_id, '') AS provider, COUNT(*) AS request_count, COALESCE(SUM(CASE WHEN client_status_code >= 400 THEN 1 ELSE 0 END), 0) AS error_count, COALESCE(AVG(latency_total_ms), 0.0) AS avg_duration_ms FROM request_logs WHERE created_at >= CAST(strftime('%s', 'now', ?) AS INTEGER) * 1000 GROUP BY COALESCE(provider_name, provider_id, '') ORDER BY request_count DESC",
            )
            .bind(format!("-{hours} hours"))
            .fetch_all(&self.pool)
            .await?)
        } else {
            Ok(sqlx::query_as::<_, ProviderStats>(
                "SELECT COALESCE(provider_name, provider_id, '') AS provider, COUNT(*) AS request_count, COALESCE(SUM(CASE WHEN client_status_code >= 400 THEN 1 ELSE 0 END), 0) AS error_count, COALESCE(AVG(latency_total_ms), 0.0) AS avg_duration_ms FROM request_logs GROUP BY COALESCE(provider_name, provider_id, '') ORDER BY request_count DESC",
            )
            .fetch_all(&self.pool)
            .await?)
        }
    }
}

#[derive(Clone)]
struct SqliteBootstrap {
    pool: SqlitePool,
}

#[async_trait]
impl StorageBootstrap for SqliteBootstrap {
    async fn init(&self) -> anyhow::Result<()> {
        Ok(())
    }

    async fn migrate(&self) -> anyhow::Result<()> {
        db::migrate(&self.pool).await
    }

    async fn health(&self) -> anyhow::Result<StorageHealth> {
        let can_connect = sqlx::query("SELECT 1").execute(&self.pool).await.is_ok();
        // schema_compatible: verify the final-state `models` table exists,
        // which confirms migrations have completed (routes → models rename done).
        let schema_compatible = if can_connect {
            sqlx::query_scalar::<_, i64>(
                "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='models'",
            )
            .fetch_one(&self.pool)
            .await
            .unwrap_or(0)
                > 0
        } else {
            false
        };
        Ok(StorageHealth {
            backend: StorageBackend::Sqlite,
            can_connect,
            schema_compatible,
            writable: can_connect,
        })
    }
}
