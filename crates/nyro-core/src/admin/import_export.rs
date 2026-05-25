use super::*;

pub(super) fn import_provider_protocol(provider: &ExportProvider) -> String {
    if provider.default_protocol.trim().is_empty() {
        provider.protocol.clone()
    } else {
        provider.default_protocol.clone()
    }
}

pub(super) fn import_provider_base_url(provider: &ExportProvider) -> String {
    if !provider.base_url.trim().is_empty() {
        return provider.base_url.clone();
    }
    base_url_from_protocol_endpoints(
        &provider.protocol_endpoints,
        &import_provider_protocol(provider),
    )
    .unwrap_or_default()
}

pub(super) fn base_url_from_protocol_endpoints(raw: &str, protocol: &str) -> Option<String> {
    let reg = crate::protocol::registry::ProtocolRegistry::global();
    let target = reg.parse_protocol(protocol)?;
    let value = serde_json::from_str::<serde_json::Value>(raw.trim()).ok()?;
    let obj = value.as_object()?;
    obj.iter().find_map(|(key, entry)| {
        let entry_protocol = reg.parse_protocol(key)?;
        if entry_protocol != target {
            return None;
        }
        entry
            .as_object()
            .and_then(|object| object.get("base_url"))
            .and_then(|value| value.as_str())
            .map(str::trim)
            .filter(|value| !value.is_empty())
            .map(ToString::to_string)
    })
}

impl AdminService {
    // ── Config Import/Export ──

    pub async fn export_config(&self) -> anyhow::Result<ExportData> {
        let providers = self.list_providers().await?;
        let models = self.list_models().await?;
        let settings = self.gw.storage.settings().list_all().await?;

        Ok(ExportData {
            version: 1,
            providers: providers
                .into_iter()
                .map(|p| ExportProvider {
                    name: p.name,
                    vendor: p.vendor,
                    protocol: p.protocol,
                    base_url: p.base_url,
                    default_protocol: String::new(),
                    protocol_endpoints: String::new(),
                    preset_key: p.preset_key,
                    channel: p.channel,
                    models_source: p.models_source,
                    static_models: p.static_models,
                    api_key: p.api_key,
                    auth_mode: p.auth_mode,
                    use_proxy: p.use_proxy,
                    is_enabled: p.is_enabled,
                })
                .collect(),
            models: models
                .into_iter()
                .map(|m| ExportModel {
                    name: m.name,
                    virtual_model: m.virtual_model,
                    target_model: m.target_model,
                    access_control: m.access_control,
                    is_enabled: m.is_enabled,
                })
                .collect(),
            settings: settings.into_iter().collect(),
        })
    }

    pub async fn import_config(&self, data: ExportData) -> anyhow::Result<ImportResult> {
        let mut providers_imported = 0u32;
        let mut models_imported = 0u32;
        let mut settings_imported = 0u32;

        for p in &data.providers {
            let exists = self
                .gw
                .storage
                .providers()
                .exists_by_name(&p.name, None)
                .await
                .unwrap_or(false);

            if !exists
                && self
                    .create_provider(CreateProvider {
                        name: p.name.clone(),
                        vendor: p.vendor.clone(),
                        protocol: import_provider_protocol(p),
                        base_url: import_provider_base_url(p),
                        preset_key: p.preset_key.clone(),
                        channel: p.channel.clone(),
                        models_source: p.models_source.clone(),
                        static_models: p.static_models.clone(),
                        api_key: p.api_key.clone(),
                        auth_mode: p.auth_mode.clone(),
                        use_proxy: p.use_proxy,
                    })
                    .await
                    .is_ok()
            {
                providers_imported += 1;
            }
        }

        let fallback_provider_id = self
            .list_providers()
            .await?
            .into_iter()
            .next()
            .map(|provider| provider.id);

        for m in &data.models {
            let exists = self
                .gw
                .storage
                .models()
                .exists_by_name(&m.name, None)
                .await
                .unwrap_or(false);

            if !exists
                && let Some(pid) = fallback_provider_id.clone()
                && self
                    .create_model(CreateModel {
                        name: m.name.clone(),
                        virtual_model: m.virtual_model.clone(),
                        balance: Some("weighted".to_string()),
                        target_provider: pid,
                        target_model: m.target_model.clone(),
                        targets: vec![],
                        access_control: Some(m.access_control),
                    })
                    .await
                    .is_ok()
            {
                models_imported += 1;
            }
        }

        for (key, value) in &data.settings {
            self.set_setting(key, value).await?;
            settings_imported += 1;
        }

        Ok(ImportResult {
            providers_imported,
            models_imported,
            settings_imported,
        })
    }
}
