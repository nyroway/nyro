use std::time::Duration;

use async_trait::async_trait;
use sqlx::{MySql, Pool};

use crate::db::models::{
    ApiKeyWithBindings, CreateApiKey, CreateModel, CreateModelBackend, CreateProvider,
    LogPage, LogQuery, Model, ModelBackend, ModelStats, OAuthCredential, Provider, ProviderStats,
    RequestLog, StatsHourly, StatsOverview, UpdateApiKey, UpdateModel, UpdateProvider,
    UpsertOAuthCredential,
};
use crate::logging::LogEntry;
use crate::storage::sql::config::SqlBackendConfig;
use crate::storage::traits::{
    ApiKeyAccessRecord, ApiKeyStore, AuthAccessStore, LogStore, ModelBackendStore,
    ModelSnapshotStore, ModelStore, OAuthCredentialStore, ProviderStore, SettingsStore,
    Storage, StorageBootstrap, StorageHealth, UsageWindow,
};

pub struct MysqlStorage {
    pool: Pool<MySql>,
}

impl MysqlStorage {
    pub async fn connect(_config: SqlBackendConfig) -> anyhow::Result<Self> {
        todo!("MySQL storage implementation coming in Task 7")
    }

    pub fn pool(&self) -> &Pool<MySql> {
        &self.pool
    }
}

#[async_trait]
impl ProviderStore for MysqlStorage {
    async fn list(&self) -> anyhow::Result<Vec<Provider>> { todo!() }
    async fn get(&self, _id: &str) -> anyhow::Result<Option<Provider>> { todo!() }
    async fn create(&self, _input: CreateProvider) -> anyhow::Result<Provider> { todo!() }
    async fn update(&self, _id: &str, _input: UpdateProvider) -> anyhow::Result<Provider> { todo!() }
    async fn delete(&self, _id: &str) -> anyhow::Result<()> { todo!() }
    async fn exists_by_name(&self, _name: &str, _exclude_id: Option<&str>) -> anyhow::Result<bool> { todo!() }
    async fn record_test_result(&self, _provider_id: &str, _result: crate::storage::traits::ProviderTestResult) -> anyhow::Result<()> { todo!() }
}

#[async_trait]
impl ModelStore for MysqlStorage {
    async fn list(&self) -> anyhow::Result<Vec<Model>> { todo!() }
    async fn get(&self, _id: &str) -> anyhow::Result<Option<Model>> { todo!() }
    async fn create(&self, _input: CreateModel) -> anyhow::Result<Model> { todo!() }
    async fn update(&self, _id: &str, _input: UpdateModel) -> anyhow::Result<Model> { todo!() }
    async fn delete(&self, _id: &str) -> anyhow::Result<()> { todo!() }
    async fn exists_by_name(&self, _name: &str, _exclude_id: Option<&str>) -> anyhow::Result<bool> { todo!() }
}

#[async_trait]
impl ModelSnapshotStore for MysqlStorage {
    async fn load_active_snapshot(&self) -> anyhow::Result<Vec<Model>> { todo!() }
}

#[async_trait]
impl ModelBackendStore for MysqlStorage {
    async fn list_backends_by_model(&self, _model_id: &str) -> anyhow::Result<Vec<ModelBackend>> { todo!() }
    async fn set_backends(&self, _model_id: &str, _backends: &[CreateModelBackend]) -> anyhow::Result<Vec<ModelBackend>> { todo!() }
    async fn delete_backends_by_model(&self, _model_id: &str) -> anyhow::Result<()> { todo!() }
}

#[async_trait]
impl SettingsStore for MysqlStorage {
    async fn get(&self, _key: &str) -> anyhow::Result<Option<String>> { todo!() }
    async fn set(&self, _key: &str, _value: &str) -> anyhow::Result<()> { todo!() }
    async fn list_all(&self) -> anyhow::Result<Vec<(String, String)>> { todo!() }
}

#[async_trait]
impl ApiKeyStore for MysqlStorage {
    async fn list(&self) -> anyhow::Result<Vec<ApiKeyWithBindings>> { todo!() }
    async fn get(&self, _id: &str) -> anyhow::Result<Option<ApiKeyWithBindings>> { todo!() }
    async fn create(&self, _input: CreateApiKey) -> anyhow::Result<ApiKeyWithBindings> { todo!() }
    async fn update(&self, _id: &str, _input: UpdateApiKey) -> anyhow::Result<ApiKeyWithBindings> { todo!() }
    async fn delete(&self, _id: &str) -> anyhow::Result<()> { todo!() }
    async fn exists_by_name(&self, _name: &str, _exclude_id: Option<&str>) -> anyhow::Result<bool> { todo!() }
}

#[async_trait]
impl AuthAccessStore for MysqlStorage {
    async fn find_api_key(&self, _raw_key: &str) -> anyhow::Result<Option<ApiKeyAccessRecord>> { todo!() }
    async fn model_binding_exists(&self, _api_key_id: &str, _model_id: &str) -> anyhow::Result<bool> { todo!() }
    async fn list_bound_model_ids(&self, _api_key_id: &str) -> anyhow::Result<Vec<String>> { todo!() }
    async fn request_count_since(&self, _api_key_id: &str, _window: UsageWindow) -> anyhow::Result<i64> { todo!() }
    async fn token_count_since(&self, _api_key_id: &str, _window: UsageWindow) -> anyhow::Result<i64> { todo!() }
}

#[async_trait]
impl LogStore for MysqlStorage {
    async fn append_batch(&self, _entries: Vec<LogEntry>) -> anyhow::Result<()> { todo!() }
    async fn query(&self, _query: LogQuery) -> anyhow::Result<LogPage> { todo!() }
    async fn find_by_id(&self, _id: &str) -> anyhow::Result<Option<RequestLog>> { todo!() }
    async fn cleanup_before(&self, _cutoff_expression: &str) -> anyhow::Result<u64> { todo!() }
    async fn stats_overview(&self, _hours: Option<i64>) -> anyhow::Result<StatsOverview> { todo!() }
    async fn stats_hourly(&self, _hours: i64) -> anyhow::Result<Vec<StatsHourly>> { todo!() }
    async fn stats_by_model(&self, _hours: Option<i64>) -> anyhow::Result<Vec<ModelStats>> { todo!() }
    async fn stats_by_provider(&self, _hours: Option<i64>) -> anyhow::Result<Vec<ProviderStats>> { todo!() }
}

#[async_trait]
impl OAuthCredentialStore for MysqlStorage {
    async fn get(&self, _provider_id: &str) -> anyhow::Result<Option<OAuthCredential>> { todo!() }
    async fn upsert(&self, _provider_id: &str, _input: UpsertOAuthCredential) -> anyhow::Result<OAuthCredential> { todo!() }
    async fn delete(&self, _provider_id: &str) -> anyhow::Result<()> { todo!() }
    async fn try_begin_refresh(&self, _provider_id: &str, _expected_version: i32) -> anyhow::Result<Option<OAuthCredential>> { todo!() }
    async fn complete_refresh(&self, _provider_id: &str, _input: UpsertOAuthCredential) -> anyhow::Result<OAuthCredential> { todo!() }
    async fn fail_refresh(&self, _provider_id: &str, _error_message: &str) -> anyhow::Result<()> { todo!() }
    async fn list_expiring(&self, _before: Duration) -> anyhow::Result<Vec<OAuthCredential>> { todo!() }
    async fn recover_stale_refreshing(&self, _timeout: Duration) -> anyhow::Result<u64> { todo!() }
}

#[async_trait]
impl StorageBootstrap for MysqlStorage {
    async fn init(&self) -> anyhow::Result<()> { todo!() }
    async fn migrate(&self) -> anyhow::Result<()> { todo!() }
    async fn health(&self) -> anyhow::Result<StorageHealth> { todo!() }
}

impl Storage for MysqlStorage {
    fn providers(&self) -> &dyn ProviderStore { self }
    fn models(&self) -> &dyn ModelStore { self }
    fn snapshots(&self) -> &dyn ModelSnapshotStore { self }
    fn model_backends(&self) -> Option<&dyn ModelBackendStore> { Some(self) }
    fn settings(&self) -> &dyn SettingsStore { self }
    fn api_keys(&self) -> Option<&dyn ApiKeyStore> { Some(self) }
    fn auth(&self) -> Option<&dyn AuthAccessStore> { Some(self) }
    fn logs(&self) -> &dyn LogStore { self }
    fn oauth_credentials(&self) -> &dyn OAuthCredentialStore { self }
    fn bootstrap(&self) -> &dyn StorageBootstrap { self }
}
