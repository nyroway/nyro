use super::*;

/// Settings key used to signal config changes to other replicas.
pub const CONFIG_EPOCH_KEY: &str = "config_epoch";

impl AdminService {
    // ── Settings ──

    pub async fn get_setting(&self, key: &str) -> anyhow::Result<Option<String>> {
        self.gw.storage.settings().get(key).await
    }

    pub async fn set_setting(&self, key: &str, value: &str) -> anyhow::Result<()> {
        self.gw.storage.settings().set(key, value).await
    }

    /// Increment the shared `config_epoch` counter so other replicas know they
    /// must reload their in-memory `model_cache`.
    pub(super) async fn bump_config_epoch(&self) -> anyhow::Result<()> {
        let store = self.gw.storage.settings();
        let current: i64 = store
            .get(CONFIG_EPOCH_KEY)
            .await?
            .as_deref()
            .and_then(|v| v.parse().ok())
            .unwrap_or(0);
        store
            .set(CONFIG_EPOCH_KEY, &(current + 1).to_string())
            .await
    }
}
