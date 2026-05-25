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
        ensure_virtual_model(&input.virtual_model)?;
        self.ensure_model_unique(None, &input.virtual_model).await?;
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
                virtual_model: input.virtual_model,
                balance: Some(balance),
                target_provider: primary_backend.provider_id.clone(),
                target_model: primary_backend.model.clone(),
                targets: vec![],
                access_control: input.access_control,
            })
            .await?;
        if let Some(store) = self.gw.storage.model_backends() {
            store.set_backends(&model.id, &backends).await?;
        }
        self.reload_model_cache().await?;
        self.get_model_by_id(&model.id).await
    }

    pub async fn update_model(&self, id: &str, input: UpdateModel) -> anyhow::Result<Model> {
        let current = self.get_model_by_id(id).await?;

        let name = normalize_name(
            &input.name.clone().unwrap_or_else(|| current.name.clone()),
            "model name",
        )?;
        self.ensure_model_name_unique(Some(id), &name).await?;
        let virtual_model = input
            .virtual_model
            .clone()
            .unwrap_or_else(|| current.virtual_model.clone());
        let balance = normalize_model_balance(input.balance.as_deref().or(Some(&current.balance)))?;
        let backends = normalize_update_model_backends(&current, &input)?;
        ensure_model_backends_valid(&backends)?;
        let primary_backend = backends
            .first()
            .ok_or_else(|| anyhow::anyhow!("at least one model backend is required"))?;
        let access_control = input.access_control.unwrap_or(current.access_control);
        let is_enabled = input.is_enabled.unwrap_or(current.is_enabled);
        ensure_virtual_model(&virtual_model)?;
        self.ensure_model_unique(Some(id), &virtual_model).await?;

        self.gw
            .storage
            .models()
            .update(
                id,
                UpdateModel {
                    name: Some(name),
                    virtual_model: Some(virtual_model),
                    balance: Some(balance),
                    target_provider: Some(primary_backend.provider_id.clone()),
                    target_model: Some(primary_backend.model.clone()),
                    targets: None,
                    access_control: Some(access_control),
                    is_enabled: Some(is_enabled),
                },
            )
            .await?;
        if let Some(store) = self.gw.storage.model_backends() {
            store.set_backends(id, &backends).await?;
        }
        self.reload_model_cache().await?;
        self.get_model_by_id(id).await
    }

    pub async fn delete_model(&self, id: &str) -> anyhow::Result<()> {
        if let Some(store) = self.gw.storage.model_backends() {
            store.delete_backends_by_model(id).await?;
        }
        self.gw.storage.models().delete(id).await?;
        self.reload_model_cache().await?;
        Ok(())
    }
    async fn ensure_model_unique(
        &self,
        exclude_id: Option<&str>,
        virtual_model: &str,
    ) -> anyhow::Result<()> {
        if self
            .gw
            .storage
            .models()
            .exists_by_virtual_model(virtual_model, exclude_id)
            .await?
        {
            let normalized_model = virtual_model.trim();
            anyhow::bail!("model already exists for virtual_model={normalized_model}");
        }
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
