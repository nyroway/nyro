use super::*;

impl AdminService {
    // ── Routes ──

    pub async fn list_routes(&self) -> anyhow::Result<Vec<Route>> {
        let mut routes = self.gw.storage.routes().list().await?;
        if let Some(store) = self.gw.storage.route_targets() {
            for route in &mut routes {
                route.targets = store.list_targets_by_route(&route.id).await?;
            }
        }
        Ok(routes)
    }

    pub async fn create_route(&self, input: CreateRoute) -> anyhow::Result<Route> {
        let name = normalize_name(&input.name, "route name")?;
        self.ensure_route_name_unique(None, &name).await?;
        ensure_virtual_model(&input.virtual_model)?;
        self.ensure_route_unique(None, &input.virtual_model).await?;
        let strategy = normalize_route_strategy(input.strategy.as_deref())?;
        let targets = normalize_create_route_targets(&input)?;
        ensure_route_targets_valid(&targets)?;
        let primary_target = targets
            .first()
            .ok_or_else(|| anyhow::anyhow!("at least one route target is required"))?;

        let route = self
            .gw
            .storage
            .routes()
            .create(CreateRoute {
                name,
                virtual_model: input.virtual_model,
                strategy: Some(strategy),
                target_provider: primary_target.provider_id.clone(),
                target_model: primary_target.model.clone(),
                targets: vec![],
                access_control: input.access_control,
            })
            .await?;
        if let Some(store) = self.gw.storage.route_targets() {
            store.set_targets(&route.id, &targets).await?;
        }
        self.reload_route_cache().await?;
        self.get_route_by_id(&route.id).await
    }

    pub async fn update_route(&self, id: &str, input: UpdateRoute) -> anyhow::Result<Route> {
        let current = self.get_route_by_id(id).await?;

        let name = normalize_name(
            &input.name.clone().unwrap_or_else(|| current.name.clone()),
            "route name",
        )?;
        self.ensure_route_name_unique(Some(id), &name).await?;
        let virtual_model = input
            .virtual_model
            .clone()
            .unwrap_or_else(|| current.virtual_model.clone());
        let strategy =
            normalize_route_strategy(input.strategy.as_deref().or(Some(&current.strategy)))?;
        let targets = normalize_update_route_targets(&current, &input)?;
        ensure_route_targets_valid(&targets)?;
        let primary_target = targets
            .first()
            .ok_or_else(|| anyhow::anyhow!("at least one route target is required"))?;
        let access_control = input.access_control.unwrap_or(current.access_control);
        let is_enabled = input.is_enabled.unwrap_or(current.is_enabled);
        ensure_virtual_model(&virtual_model)?;
        self.ensure_route_unique(Some(id), &virtual_model).await?;

        self.gw
            .storage
            .routes()
            .update(
                id,
                UpdateRoute {
                    name: Some(name),
                    virtual_model: Some(virtual_model),
                    strategy: Some(strategy),
                    target_provider: Some(primary_target.provider_id.clone()),
                    target_model: Some(primary_target.model.clone()),
                    targets: None,
                    access_control: Some(access_control),
                    is_enabled: Some(is_enabled),
                },
            )
            .await?;
        if let Some(store) = self.gw.storage.route_targets() {
            store.set_targets(id, &targets).await?;
        }
        self.reload_route_cache().await?;
        self.get_route_by_id(id).await
    }

    pub async fn delete_route(&self, id: &str) -> anyhow::Result<()> {
        if let Some(store) = self.gw.storage.route_targets() {
            store.delete_targets_by_route(id).await?;
        }
        self.gw.storage.routes().delete(id).await?;
        self.reload_route_cache().await?;
        Ok(())
    }
    async fn ensure_route_unique(
        &self,
        exclude_id: Option<&str>,
        virtual_model: &str,
    ) -> anyhow::Result<()> {
        if self
            .gw
            .storage
            .routes()
            .exists_by_virtual_model(virtual_model, exclude_id)
            .await?
        {
            let normalized_model = virtual_model.trim();
            anyhow::bail!("route already exists for model={normalized_model}");
        }
        Ok(())
    }

    async fn ensure_route_name_unique(
        &self,
        exclude_id: Option<&str>,
        name: &str,
    ) -> anyhow::Result<()> {
        if self
            .gw
            .storage
            .routes()
            .exists_by_name(name, exclude_id)
            .await?
        {
            return Err(coded_error(
                "ROUTE_NAME_CONFLICT",
                &format!("route name already exists: {name}"),
                serde_json::json!({ "name": name }),
            ));
        }
        Ok(())
    }
    async fn get_route_by_id(&self, id: &str) -> anyhow::Result<Route> {
        let mut route = self
            .gw
            .storage
            .routes()
            .get(id)
            .await?
            .context("route not found")?;
        if let Some(store) = self.gw.storage.route_targets() {
            route.targets = store.list_targets_by_route(&route.id).await?;
        }
        Ok(route)
    }

    pub(super) async fn reload_route_cache(&self) -> anyhow::Result<()> {
        self.gw
            .route_cache
            .write()
            .await
            .reload(self.gw.storage.snapshots())
            .await
    }
}
