use super::*;

impl AdminService {
    // ── API Keys ──

    pub async fn list_api_keys(&self) -> anyhow::Result<Vec<ApiKeyWithBindings>> {
        self.api_keys_store()?.list().await
    }

    pub async fn get_api_key(&self, id: &str) -> anyhow::Result<ApiKeyWithBindings> {
        self.api_keys_store()?
            .get(id)
            .await?
            .context("api key not found")
    }

    pub async fn create_api_key(&self, input: CreateApiKey) -> anyhow::Result<ApiKeyWithBindings> {
        let name = normalize_name(&input.name, "api key name")?;
        self.ensure_api_key_name_unique(None, &name).await?;
        self.api_keys_store()?
            .create(CreateApiKey {
                name,
                rpm: input.rpm,
                rpd: input.rpd,
                tpm: input.tpm,
                tpd: input.tpd,
                expires_at: input.expires_at,
                model_ids: input.model_ids,
            })
            .await
    }

    pub async fn update_api_key(
        &self,
        id: &str,
        input: UpdateApiKey,
    ) -> anyhow::Result<ApiKeyWithBindings> {
        let current = self
            .api_keys_store()?
            .get(id)
            .await?
            .context("api key not found")?;

        let name = normalize_name(&input.name.unwrap_or(current.name), "api key name")?;
        self.ensure_api_key_name_unique(Some(id), &name).await?;
        let rpm = input.rpm.or(current.rpm);
        let rpd = input.rpd.or(current.rpd);
        let tpm = input.tpm.or(current.tpm);
        let tpd = input.tpd.or(current.tpd);
        let is_enabled = input.is_enabled.unwrap_or(current.is_enabled);
        let expires_at = input.expires_at.or(current.expires_at);

        self.api_keys_store()?
            .update(
                id,
                UpdateApiKey {
                    name: Some(name),
                    rpm,
                    rpd,
                    tpm,
                    tpd,
                    is_enabled: Some(is_enabled),
                    expires_at,
                    model_ids: input.model_ids,
                },
            )
            .await
    }

    pub async fn delete_api_key(&self, id: &str) -> anyhow::Result<()> {
        self.api_keys_store()?.delete(id).await?;
        Ok(())
    }
    async fn ensure_api_key_name_unique(
        &self,
        exclude_id: Option<&str>,
        name: &str,
    ) -> anyhow::Result<()> {
        if self
            .api_keys_store()?
            .exists_by_name(name, exclude_id)
            .await?
        {
            return Err(coded_error(
                "API_KEY_NAME_CONFLICT",
                &format!("api key name already exists: {name}"),
                serde_json::json!({ "name": name }),
            ));
        }
        Ok(())
    }
    fn api_keys_store(&self) -> anyhow::Result<&dyn crate::storage::traits::ApiKeyStore> {
        self.gw
            .storage
            .api_keys()
            .context("selected storage backend does not support api key management")
    }
}
