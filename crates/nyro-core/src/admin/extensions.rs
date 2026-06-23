use super::*;

impl AdminService {
    /// Read-only snapshot of every compile-time extension registered with the
    /// [`crate::plugin::PluginKernel`] — phase hooks, integration hooks,
    /// provider vendors and protocol endpoints.
    ///
    /// Powers the admin "loaded extensions" panel. Pure in-memory aggregation;
    /// no storage access.
    pub async fn list_loaded_extensions(&self) -> anyhow::Result<Vec<Value>> {
        Ok(crate::plugin::PluginKernel::global()
            .manifests()
            .into_iter()
            .map(|m| {
                serde_json::json!({
                    "id": m.id,
                    "capability": m.capability.as_str(),
                })
            })
            .collect())
    }
}
