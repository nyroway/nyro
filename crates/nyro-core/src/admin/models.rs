use super::*;

impl AdminService {
    // ── Models ──

    pub async fn list_models(&self) -> anyhow::Result<Vec<Model>> {
        let mut models = self.gw.storage.models().list().await?;
        if let Some(store) = self.gw.storage.model_backends() {
            for model in &mut models {
                model.targets = store.list_backends_by_model(&model.id).await?;
            }
        }
        Ok(models)
    }

    pub async fn create_model(&self, input: CreateModel) -> anyhow::Result<Model> {
        let name = normalize_name(&input.name, "model name")?;
        self.ensure_model_name_unique(None, &name).await?;
        let balance = normalize_model_balance(input.balance.as_deref())?;
        let backends = normalize_create_model_backends(&input)?;
        ensure_model_backends_valid(&backends)?;
        let primary_backend = backends
            .first()
            .ok_or_else(|| anyhow::anyhow!("at least one model backend is required"))?;

        let model = self
            .gw
            .storage
            .models()
            .create(CreateModel {
                name,
                balance: Some(balance),
                target_provider: primary_backend.provider_id.clone(),
                target_model: primary_backend.model.clone(),
                targets: vec![],
                enable_auth: input.enable_auth,
                enable_payload: input.enable_payload,
            })
            .await?;
        if let Some(store) = self.gw.storage.model_backends() {
            store.set_backends(&model.id, &backends).await?;
        }
        self.reload_model_cache().await?;
        self.bump_config_epoch().await?;
        self.get_model_by_id(&model.id).await
    }

    pub async fn update_model(&self, id: &str, input: UpdateModel) -> anyhow::Result<Model> {
        let current = self.get_model_by_id(id).await?;

        let name = normalize_name(
            &input.name.clone().unwrap_or_else(|| current.name.clone()),
            "model name",
        )?;
        self.ensure_model_name_unique(Some(id), &name).await?;
        let balance = normalize_model_balance(input.balance.as_deref().or(Some(&current.balance)))?;
        let backends = normalize_update_model_backends(&current, &input)?;
        ensure_model_backends_valid(&backends)?;
        let primary_backend = backends
            .first()
            .ok_or_else(|| anyhow::anyhow!("at least one model backend is required"))?;
        let enable_auth = input.enable_auth.unwrap_or(current.enable_auth);
        let enable_payload = input.enable_payload.unwrap_or(current.enable_payload);
        let is_enabled = input.is_enabled.unwrap_or(current.is_enabled);

        self.gw
            .storage
            .models()
            .update(
                id,
                UpdateModel {
                    name: Some(name),
                    balance: Some(balance),
                    target_provider: Some(primary_backend.provider_id.clone()),
                    target_model: Some(primary_backend.model.clone()),
                    targets: None,
                    enable_auth: Some(enable_auth),
                    enable_payload: Some(enable_payload),
                    is_enabled: Some(is_enabled),
                },
            )
            .await?;
        if let Some(store) = self.gw.storage.model_backends() {
            store.set_backends(id, &backends).await?;
        }
        self.reload_model_cache().await?;
        self.bump_config_epoch().await?;
        self.get_model_by_id(id).await
    }

    pub async fn delete_model(&self, id: &str) -> anyhow::Result<()> {
        if let Some(store) = self.gw.storage.model_backends() {
            store.delete_backends_by_model(id).await?;
        }
        self.gw.storage.models().delete(id).await?;
        self.reload_model_cache().await?;
        self.bump_config_epoch().await?;
        Ok(())
    }
    async fn ensure_model_name_unique(
        &self,
        exclude_id: Option<&str>,
        name: &str,
    ) -> anyhow::Result<()> {
        if self
            .gw
            .storage
            .models()
            .exists_by_name(name, exclude_id)
            .await?
        {
            return Err(coded_error(
                "MODEL_NAME_CONFLICT",
                &format!("model name already exists: {name}"),
                serde_json::json!({ "name": name }),
            ));
        }
        Ok(())
    }
    async fn get_model_by_id(&self, id: &str) -> anyhow::Result<Model> {
        let mut model = self
            .gw
            .storage
            .models()
            .get(id)
            .await?
            .context("model not found")?;
        if let Some(store) = self.gw.storage.model_backends() {
            model.targets = store.list_backends_by_model(&model.id).await?;
        }
        Ok(model)
    }

    pub(super) async fn reload_model_cache(&self) -> anyhow::Result<()> {
        self.gw
            .model_cache
            .write()
            .await
            .reload(self.gw.storage.snapshots())
            .await
    }
}
