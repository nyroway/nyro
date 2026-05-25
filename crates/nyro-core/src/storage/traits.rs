use std::sync::Arc;
use std::time::Duration;

use async_trait::async_trait;

use crate::db::models::{
    ApiKeyWithBindings, CreateApiKey, CreateModel, CreateModelBackend, CreateProvider, LogPage,
    LogQuery, Model, ModelBackend, ModelStats, OAuthCredential, Provider, ProviderStats,
    RequestLog, StatsHourly, StatsOverview, UpdateApiKey, UpdateModel, UpdateProvider,
    UpsertOAuthCredential,
};
use crate::logging::LogEntry;

#[derive(Debug, Clone)]
pub struct ProviderTestResult {
    pub success: bool,
    pub tested_at: String,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum UsageWindow {
    Minute,
    Day,
}

#[derive(Debug, Clone)]
pub struct ApiKeyAccessRecord {
    pub id: String,
    pub name: String,
    pub is_enabled: bool,
    pub expires_at: Option<String>,
    pub rpm: Option<i32>,
    pub rpd: Option<i32>,
    pub tpm: Option<i32>,
    pub tpd: Option<i32>,
}

#[derive(Debug, Clone)]
pub enum StorageBackend {
    Sqlite,
    Postgres,
}

#[derive(Debug, Clone)]
pub struct StorageHealth {
    pub backend: StorageBackend,
    pub can_connect: bool,
    pub schema_compatible: bool,
    pub writable: bool,
}

#[async_trait]
pub trait ProviderStore: Send + Sync {
    async fn list(&self) -> anyhow::Result<Vec<Provider>>;
    async fn get(&self, id: &str) -> anyhow::Result<Option<Provider>>;
    async fn create(&self, input: CreateProvider) -> anyhow::Result<Provider>;
    async fn update(&self, id: &str, input: UpdateProvider) -> anyhow::Result<Provider>;
    async fn delete(&self, id: &str) -> anyhow::Result<()>;
    async fn exists_by_name(&self, name: &str, exclude_id: Option<&str>) -> anyhow::Result<bool>;
    async fn record_test_result(
        &self,
        provider_id: &str,
        result: ProviderTestResult,
    ) -> anyhow::Result<()>;
}

#[async_trait]
pub trait ModelStore: Send + Sync {
    async fn list(&self) -> anyhow::Result<Vec<Model>>;
    async fn get(&self, id: &str) -> anyhow::Result<Option<Model>>;
    async fn create(&self, input: CreateModel) -> anyhow::Result<Model>;
    async fn update(&self, id: &str, input: UpdateModel) -> anyhow::Result<Model>;
    async fn delete(&self, id: &str) -> anyhow::Result<()>;
    async fn exists_by_name(&self, name: &str, exclude_id: Option<&str>) -> anyhow::Result<bool>;
    async fn exists_by_virtual_model(
        &self,
        virtual_model: &str,
        exclude_id: Option<&str>,
    ) -> anyhow::Result<bool>;
}

#[async_trait]
pub trait ModelSnapshotStore: Send + Sync {
    async fn load_active_snapshot(&self) -> anyhow::Result<Vec<Model>>;
}

#[async_trait]
pub trait ModelBackendStore: Send + Sync {
    async fn list_backends_by_model(&self, model_id: &str) -> anyhow::Result<Vec<ModelBackend>>;
    async fn set_backends(
        &self,
        model_id: &str,
        backends: &[CreateModelBackend],
    ) -> anyhow::Result<Vec<ModelBackend>>;
    async fn delete_backends_by_model(&self, model_id: &str) -> anyhow::Result<()>;
}

#[async_trait]
pub trait SettingsStore: Send + Sync {
    async fn get(&self, key: &str) -> anyhow::Result<Option<String>>;
    async fn set(&self, key: &str, value: &str) -> anyhow::Result<()>;
    async fn list_all(&self) -> anyhow::Result<Vec<(String, String)>>;
}

#[async_trait]
pub trait ApiKeyStore: Send + Sync {
    async fn list(&self) -> anyhow::Result<Vec<ApiKeyWithBindings>>;
    async fn get(&self, id: &str) -> anyhow::Result<Option<ApiKeyWithBindings>>;
    async fn create(&self, input: CreateApiKey) -> anyhow::Result<ApiKeyWithBindings>;
    async fn update(&self, id: &str, input: UpdateApiKey) -> anyhow::Result<ApiKeyWithBindings>;
    async fn delete(&self, id: &str) -> anyhow::Result<()>;
    async fn exists_by_name(&self, name: &str, exclude_id: Option<&str>) -> anyhow::Result<bool>;
}

#[async_trait]
pub trait AuthAccessStore: Send + Sync {
    async fn find_api_key(&self, raw_key: &str) -> anyhow::Result<Option<ApiKeyAccessRecord>>;
    async fn model_binding_exists(&self, api_key_id: &str, model_id: &str) -> anyhow::Result<bool>;
    async fn list_bound_model_ids(&self, api_key_id: &str) -> anyhow::Result<Vec<String>>;
    async fn request_count_since(
        &self,
        api_key_id: &str,
        window: UsageWindow,
    ) -> anyhow::Result<i64>;
    async fn token_count_since(&self, api_key_id: &str, window: UsageWindow)
    -> anyhow::Result<i64>;
}

#[async_trait]
pub trait LogStore: Send + Sync {
    async fn append_batch(&self, entries: Vec<LogEntry>) -> anyhow::Result<()>;
    async fn query(&self, query: LogQuery) -> anyhow::Result<LogPage>;
    async fn find_by_id(&self, id: &str) -> anyhow::Result<Option<RequestLog>>;
    async fn cleanup_before(&self, cutoff_expression: &str) -> anyhow::Result<u64>;
    async fn stats_overview(&self, hours: Option<i64>) -> anyhow::Result<StatsOverview>;
    async fn stats_hourly(&self, hours: i64) -> anyhow::Result<Vec<StatsHourly>>;
    async fn stats_by_model(&self, hours: Option<i64>) -> anyhow::Result<Vec<ModelStats>>;
    async fn stats_by_provider(&self, hours: Option<i64>) -> anyhow::Result<Vec<ProviderStats>>;
}

#[async_trait]
pub trait OAuthCredentialStore: Send + Sync {
    async fn get(&self, provider_id: &str) -> anyhow::Result<Option<OAuthCredential>>;
    async fn upsert(
        &self,
        provider_id: &str,
        input: UpsertOAuthCredential,
    ) -> anyhow::Result<OAuthCredential>;
    async fn delete(&self, provider_id: &str) -> anyhow::Result<()>;
    async fn try_begin_refresh(
        &self,
        provider_id: &str,
        expected_version: i32,
    ) -> anyhow::Result<Option<OAuthCredential>>;
    async fn complete_refresh(
        &self,
        provider_id: &str,
        input: UpsertOAuthCredential,
    ) -> anyhow::Result<OAuthCredential>;
    async fn fail_refresh(&self, provider_id: &str, error_message: &str) -> anyhow::Result<()>;
    async fn list_expiring(&self, before: Duration) -> anyhow::Result<Vec<OAuthCredential>>;
    async fn recover_stale_refreshing(&self, timeout: Duration) -> anyhow::Result<u64>;
}

#[async_trait]
pub trait StorageBootstrap: Send + Sync {
    async fn init(&self) -> anyhow::Result<()>;
    async fn migrate(&self) -> anyhow::Result<()>;
    async fn health(&self) -> anyhow::Result<StorageHealth>;
}

pub trait Storage: Send + Sync {
    fn providers(&self) -> &dyn ProviderStore;
    fn models(&self) -> &dyn ModelStore;
    fn snapshots(&self) -> &dyn ModelSnapshotStore;
    fn model_backends(&self) -> Option<&dyn ModelBackendStore> {
        None
    }
    fn settings(&self) -> &dyn SettingsStore;
    fn api_keys(&self) -> Option<&dyn ApiKeyStore> {
        None
    }
    fn auth(&self) -> Option<&dyn AuthAccessStore> {
        None
    }
    fn logs(&self) -> &dyn LogStore;
    fn oauth_credentials(&self) -> &dyn OAuthCredentialStore;
    fn bootstrap(&self) -> &dyn StorageBootstrap;
}

pub type DynStorage = Arc<dyn Storage>;
