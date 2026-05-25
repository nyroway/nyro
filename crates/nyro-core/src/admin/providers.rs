use super::*;

impl AdminService {
    // ── Providers ──

    pub async fn list_providers(&self) -> anyhow::Result<Vec<Provider>> {
        self.gw.storage.providers().list().await
    }

    pub async fn list_provider_presets(&self) -> anyhow::Result<Vec<Value>> {
        parse_provider_presets_snapshot()
    }

    pub async fn get_provider(&self, id: &str) -> anyhow::Result<Provider> {
        self.gw
            .storage
            .providers()
            .get(id)
            .await?
            .ok_or_else(|| anyhow::anyhow!("provider not found: {id}"))
    }
    pub async fn create_provider(&self, input: CreateProvider) -> anyhow::Result<Provider> {
        let name = normalize_name(&input.name, "provider name")?;
        self.ensure_provider_name_unique(None, &name).await?;
        let vendor = normalize_vendor(input.vendor.as_deref());
        let auth_mode = resolve_admin_preset_channel_auth_mode(
            input.preset_key.as_deref(),
            input.channel.as_deref(),
        )
        .unwrap_or(input.auth_mode);
        let api_key = if auth_mode == "oauth" {
            String::new()
        } else {
            input.api_key
        };
        self.gw
            .storage
            .providers()
            .create(CreateProvider {
                name,
                vendor,
                protocol: input.protocol,
                base_url: input.base_url,
                preset_key: input.preset_key,
                channel: input.channel,
                models_source: input.models_source,
                static_models: input.static_models,
                api_key,
                auth_mode,
                use_proxy: input.use_proxy,
            })
            .await
    }

    pub async fn copy_provider(&self, id: &str) -> anyhow::Result<Provider> {
        self.copy_provider_with_options(id, CopyProviderOptions::default())
            .await
    }

    pub async fn copy_provider_with_options(
        &self,
        id: &str,
        options: CopyProviderOptions,
    ) -> anyhow::Result<Provider> {
        let original = self.get_provider(id).await?;
        let name = self.next_provider_copy_name(&original.name).await?;
        let copied = self
            .create_provider(CreateProvider {
                name,
                vendor: original.vendor.clone(),
                protocol: original.protocol.clone(),
                base_url: original.base_url.clone(),
                preset_key: original.preset_key.clone(),
                channel: original.channel.clone(),
                models_source: original.models_source.clone(),
                static_models: original.static_models.clone(),
                api_key: original.api_key.clone(),
                auth_mode: original.auth_mode.clone(),
                use_proxy: original.use_proxy,
            })
            .await?;
        let copied = self
            .update_provider(
                &copied.id,
                UpdateProvider {
                    is_enabled: Some(false),
                    ..Default::default()
                },
            )
            .await?;

        let copied = if original.effective_auth_mode() == "oauth" {
            match self
                .gw
                .storage
                .oauth_credentials()
                .get(&original.id)
                .await?
            {
                Some(credential) => {
                    let credential_input = upsert_credential_from_oauth(&credential);
                    let provisioned = async {
                        self.gw
                            .storage
                            .oauth_credentials()
                            .upsert(&copied.id, credential_input)
                            .await?;
                        let driver_key = credential.driver_key.clone();
                        let stored = stored_credential_from_oauth(&credential, &driver_key);
                        self.sync_provider_runtime_fields(&copied, &stored).await
                    }
                    .await;

                    match provisioned {
                        Ok(provider) => provider,
                        Err(error) => {
                            if let Err(cleanup_error) = self.delete_provider(&copied.id).await {
                                tracing::warn!(
                                    "failed to rollback copied oauth provider {} after provisioning error: {}",
                                    copied.id,
                                    cleanup_error
                                );
                            }
                            return Err(error.context("copy oauth provider"));
                        }
                    }
                }
                None => copied,
            }
        } else {
            copied
        };

        if options.append_targets {
            self.append_provider_targets(&original.id, &copied.id)
                .await?;
        }

        Ok(copied)
    }

    pub async fn update_provider(
        &self,
        id: &str,
        input: UpdateProvider,
    ) -> anyhow::Result<Provider> {
        let current = self.get_provider(id).await?;
        let current_base_url = current.base_url.clone();
        let models_source_input = input.models_source.map(|value| value.trim().to_string());

        let name = normalize_name(&input.name.unwrap_or(current.name), "provider name")?;
        self.ensure_provider_name_unique(Some(id), &name).await?;
        let vendor = if input.vendor.is_some() {
            normalize_vendor(input.vendor.as_deref())
        } else {
            normalize_vendor(current.vendor.as_deref())
        };
        let models_source = models_source_input
            .or_else(|| current.models_source.as_deref().map(ToString::to_string));
        let protocol = input.protocol.unwrap_or(current.protocol);
        let base_url = input.base_url.unwrap_or(current.base_url);
        let preset_key = input.preset_key.or(current.preset_key);
        let channel = input.channel.or(current.channel);
        let static_models = input.static_models.or(current.static_models);
        let api_key = input.api_key.unwrap_or(current.api_key);
        let auth_mode =
            resolve_admin_preset_channel_auth_mode(preset_key.as_deref(), channel.as_deref())
                .or(input.auth_mode)
                .unwrap_or(current.auth_mode);
        let api_key = if auth_mode == "oauth" {
            Some(String::new())
        } else {
            Some(api_key)
        };
        let use_proxy = input.use_proxy.unwrap_or(current.use_proxy);
        let is_enabled = input.is_enabled.unwrap_or(current.is_enabled);
        let base_url_changed = base_url != current_base_url;

        let provider = self
            .gw
            .storage
            .providers()
            .update(
                id,
                UpdateProvider {
                    name: Some(name),
                    vendor,
                    protocol: Some(protocol),
                    base_url: Some(base_url),
                    preset_key,
                    channel,
                    models_source,
                    static_models,
                    api_key,
                    auth_mode: Some(auth_mode),
                    use_proxy: Some(use_proxy),
                    is_enabled: Some(is_enabled),
                },
            )
            .await?;

        if base_url_changed {
            self.gw.clear_ollama_capability_cache_for_provider(id).await;
        }

        Ok(provider)
    }

    pub async fn delete_provider(&self, id: &str) -> anyhow::Result<()> {
        self.gw.storage.providers().delete(id).await?;
        self.reload_model_cache().await?;
        self.gw.clear_ollama_capability_cache_for_provider(id).await;
        Ok(())
    }

    async fn ensure_provider_name_unique(
        &self,
        exclude_id: Option<&str>,
        name: &str,
    ) -> anyhow::Result<()> {
        if self
            .gw
            .storage
            .providers()
            .exists_by_name(name, exclude_id)
            .await?
        {
            return Err(coded_error(
                "PROVIDER_NAME_CONFLICT",
                &format!("provider name already exists: {name}"),
                serde_json::json!({ "name": name }),
            ));
        }
        Ok(())
    }

    async fn next_provider_copy_name(&self, original_name: &str) -> anyhow::Result<String> {
        let base = format!("{}_Copy", normalize_name(original_name, "provider name")?);
        if !self
            .gw
            .storage
            .providers()
            .exists_by_name(&base, None)
            .await?
        {
            return Ok(base);
        }

        for index in 2.. {
            let candidate = format!("{base}{index}");
            if !self
                .gw
                .storage
                .providers()
                .exists_by_name(&candidate, None)
                .await?
            {
                return Ok(candidate);
            }
        }

        unreachable!("unbounded provider copy name search must return");
    }

    async fn append_provider_targets(
        &self,
        original_provider_id: &str,
        copied_provider_id: &str,
    ) -> anyhow::Result<()> {
        let models = self.list_models().await?;
        for model in models.into_iter().filter(|model| {
            model
                .targets
                .iter()
                .any(|target| target.provider_id == original_provider_id)
        }) {
            let mut targets = model
                .targets
                .iter()
                .map(|target| CreateModelBackend {
                    provider_id: target.provider_id.clone(),
                    model: target.model.clone(),
                    weight: Some(target.weight),
                    priority: Some(target.priority),
                })
                .collect::<Vec<_>>();

            let copied_targets = model
                .targets
                .iter()
                .filter(|target| target.provider_id == original_provider_id)
                .map(|target| CreateModelBackend {
                    provider_id: copied_provider_id.to_string(),
                    model: target.model.clone(),
                    weight: Some(target.weight),
                    priority: Some(target.priority),
                });
            targets.extend(copied_targets);

            self.update_model(
                &model.id,
                UpdateModel {
                    targets: Some(
                        targets
                            .into_iter()
                            .map(|target| UpsertModelBackend {
                                id: None,
                                provider_id: target.provider_id,
                                model: target.model,
                                weight: target.weight,
                                priority: target.priority,
                            })
                            .collect(),
                    ),
                    ..UpdateModel::default()
                },
            )
            .await?;
        }
        Ok(())
    }
    pub async fn test_provider(&self, id: &str) -> anyhow::Result<TestResult> {
        let provider = self.get_provider(id).await?;
        self.gw
            .clear_ollama_capability_cache_for_provider(&provider.id)
            .await;
        let start = Instant::now();
        let protocol = provider.protocol.trim();
        let vertex_runtime = if vertexai::is_vertex_vendor(&provider) {
            Some(self.resolve_provider_runtime(&provider).await?)
        } else {
            None
        };
        let base_url_owned = vertex_runtime
            .as_ref()
            .and_then(|runtime| runtime.binding.base_url_override.as_deref())
            .map(str::to_string)
            .unwrap_or_else(|| provider.base_url.clone());
        let base_url = base_url_owned.trim();

        let result = if base_url.is_empty() {
            TestResult {
                success: false,
                latency_ms: 0,
                model: None,
                error: Some("Base URL is empty".to_string()),
            }
        } else {
            let mut failures: Vec<String> = Vec::new();
            if reqwest::Url::parse(base_url).is_err() {
                failures.push(format!("{protocol}: Base URL format is invalid"));
            } else {
                let mut request = self
                    .gw
                    .http_client
                    .get(base_url)
                    .timeout(Duration::from_secs(10));
                if let Some(runtime) = &vertex_runtime {
                    let mut headers = runtime_binding_headers(&runtime.binding)?;
                    if !runtime.binding.disable_default_auth {
                        headers.insert(
                            AUTHORIZATION,
                            HeaderValue::from_str(&format!("Bearer {}", runtime.access_token))?,
                        );
                    }
                    request = request.headers(headers);
                }
                if let Err(e) = request.send().await {
                    failures.push(format!("{protocol}: {}", format_connectivity_error(&e)));
                }
            }

            if failures.is_empty() {
                TestResult {
                    success: true,
                    latency_ms: start.elapsed().as_millis() as u64,
                    model: None,
                    error: None,
                }
            } else {
                TestResult {
                    success: false,
                    latency_ms: start.elapsed().as_millis() as u64,
                    model: None,
                    error: Some(format!(
                        "Connectivity check failed for provider endpoint: {}",
                        failures.join("; ")
                    )),
                }
            }
        };
        self.record_provider_test_result(&provider.id, &result)
            .await?;
        Ok(result)
    }

    async fn record_provider_test_result(
        &self,
        provider_id: &str,
        result: &TestResult,
    ) -> anyhow::Result<()> {
        self.gw
            .storage
            .providers()
            .record_test_result(
                provider_id,
                ProviderTestResult {
                    success: result.success,
                    tested_at: String::new(),
                },
            )
            .await
    }

    pub async fn test_provider_models(&self, id: &str) -> anyhow::Result<Vec<String>> {
        let provider = self.get_provider(id).await?;
        let runtime = self.resolve_provider_runtime(&provider).await?;
        let credential = runtime.access_token.clone();
        if let Some(static_list) = runtime.binding.static_models_override.as_deref() {
            let models: Vec<String> = static_list
                .iter()
                .map(|s| s.trim().to_string())
                .filter(|s| !s.is_empty())
                .collect();
            if !models.is_empty() {
                return Ok(models);
            }
        }
        let endpoint = runtime
            .binding
            .models_source_override
            .clone()
            .or_else(|| provider.effective_models_source().map(ToString::to_string))
            .map(|value| value.trim().to_string())
            .filter(|value| !value.is_empty())
            .ok_or_else(|| anyhow::anyhow!("Model Discovery URL is empty"))?;

        if let Some(models) = lookup_models_dev_models(&self.gw.config.data_dir, &endpoint)? {
            if models.is_empty() {
                anyhow::bail!("Model list format is invalid or empty");
            }
            return Ok(models);
        }

        let mut headers = if runtime.binding.disable_default_auth {
            HeaderMap::new()
        } else {
            build_model_headers(&provider.protocol, provider.vendor.as_deref(), &credential)?
        };
        headers.extend(runtime_binding_headers(&runtime.binding)?);
        let mut request = self
            .gw
            .http_client
            .get(&endpoint)
            .headers(headers)
            .timeout(Duration::from_secs(10));

        if provider.protocol == "gemini" && !runtime.binding.disable_default_auth {
            let separator = if endpoint.contains('?') { '&' } else { '?' };
            let mut headers =
                build_model_headers(&provider.protocol, provider.vendor.as_deref(), &credential)?;
            headers.extend(runtime_binding_headers(&runtime.binding)?);
            request = self
                .gw
                .http_client
                .get(format!("{endpoint}{separator}key={}", credential))
                .headers(headers)
                .timeout(Duration::from_secs(10));
        }

        let resp = request
            .send()
            .await
            .map_err(|e| anyhow::anyhow!(format_connectivity_error(&e)))?;
        if !resp.status().is_success() {
            let status = resp.status().as_u16();
            let body = resp.text().await.unwrap_or_default();
            let preview = body.chars().take(200).collect::<String>();
            anyhow::bail!("HTTP {status}: {preview}");
        }

        let json: Value = resp.json().await.unwrap_or_default();
        let models =
            extract_models_from_response(&provider.protocol, provider.vendor.as_deref(), &json);
        if models.is_empty() {
            anyhow::bail!("Model list format is invalid or empty");
        }

        Ok(models)
    }
    pub async fn get_provider_models(&self, id: &str) -> anyhow::Result<Vec<String>> {
        let provider = self.get_provider(id).await?;
        let runtime = self.resolve_provider_runtime(&provider).await?;
        let credential = runtime.access_token.clone();
        if let Some(static_list) = runtime.binding.static_models_override.as_deref() {
            let models: Vec<String> = static_list
                .iter()
                .map(|s| s.trim().to_string())
                .filter(|s| !s.is_empty())
                .collect();
            if !models.is_empty() {
                return Ok(models);
            }
        }

        if let Some(endpoint) = runtime
            .binding
            .models_source_override
            .clone()
            .or_else(|| resolve_models_endpoint(&provider))
        {
            if let Some(models) = lookup_models_dev_models(&self.gw.config.data_dir, &endpoint)?
                && !models.is_empty()
            {
                return Ok(models);
            }

            let mut headers = if runtime.binding.disable_default_auth {
                HeaderMap::new()
            } else {
                build_model_headers(&provider.protocol, provider.vendor.as_deref(), &credential)?
            };
            headers.extend(runtime_binding_headers(&runtime.binding)?);
            let mut request = self.gw.http_client.get(&endpoint).headers(headers);

            if provider.protocol == "gemini" && !runtime.binding.disable_default_auth {
                let separator = if endpoint.contains('?') { '&' } else { '?' };
                let mut headers = build_model_headers(
                    &provider.protocol,
                    provider.vendor.as_deref(),
                    &credential,
                )?;
                headers.extend(runtime_binding_headers(&runtime.binding)?);
                request = self
                    .gw
                    .http_client
                    .get(format!("{endpoint}{separator}key={}", credential))
                    .headers(headers);
            }

            if let Ok(resp) = request.send().await
                && resp.status().is_success()
            {
                let json: Value = resp.json().await.unwrap_or_default();
                let models = extract_models_from_response(
                    &provider.protocol,
                    provider.vendor.as_deref(),
                    &json,
                );
                if !models.is_empty() {
                    return Ok(models);
                }
            }
        }

        Ok(parse_static_models(provider.static_models.as_deref()))
    }

    pub async fn get_model_capabilities(
        &self,
        provider_id: &str,
        model: &str,
    ) -> anyhow::Result<ModelCapabilities> {
        let provider = self.get_provider(provider_id).await?;
        let trimmed_model = model.trim();
        if trimmed_model.is_empty() {
            anyhow::bail!("model cannot be empty");
        }
        self.resolve_provider_model_capabilities(&provider, trimmed_model)
            .await
    }

    async fn resolve_provider_model_capabilities(
        &self,
        provider: &Provider,
        model: &str,
    ) -> anyhow::Result<ModelCapabilities> {
        match preset_capabilities_source(provider) {
            CapabilitiesSource::ModelsDev(vendor_key) => {
                let matched =
                    lookup_models_dev_capability(&self.gw.config.data_dir, vendor_key, model);
                matched.ok_or_else(|| {
                    anyhow::anyhow!("no matched model capabilities found in models.dev")
                })
            }
            CapabilitiesSource::Http(url) => {
                if is_ollama_show_endpoint(url) {
                    self.query_ollama_show_capability(url, model).await
                } else {
                    self.query_http_capability(provider, url, model).await
                }
            }
            CapabilitiesSource::Auto => Ok(fuzzy_match_models_dev(&self.gw.config.data_dir, model)
                .ok_or_else(|| {
                    anyhow::anyhow!("no matched model capabilities found in auto mode")
                })?),
        }
    }

    async fn query_http_capability(
        &self,
        provider: &Provider,
        url: &str,
        model: &str,
    ) -> anyhow::Result<ModelCapabilities> {
        let runtime = self.resolve_provider_runtime(provider).await?;
        let credential = runtime.access_token;
        let mut headers = if runtime.binding.disable_default_auth {
            HeaderMap::new()
        } else {
            build_model_headers(&provider.protocol, provider.vendor.as_deref(), &credential)?
        };
        headers.extend(runtime_binding_headers(&runtime.binding)?);
        let mut request = self
            .gw
            .http_client
            .get(url)
            .headers(headers)
            .timeout(Duration::from_secs(10));

        if provider.protocol == "gemini" && !runtime.binding.disable_default_auth {
            let separator = if url.contains('?') { '&' } else { '?' };
            let mut headers =
                build_model_headers(&provider.protocol, provider.vendor.as_deref(), &credential)?;
            headers.extend(runtime_binding_headers(&runtime.binding)?);
            request = self
                .gw
                .http_client
                .get(format!("{url}{separator}key={}", credential))
                .headers(headers)
                .timeout(Duration::from_secs(10));
        }

        let resp = request
            .send()
            .await
            .map_err(|e| anyhow::anyhow!(format_connectivity_error(&e)))?;
        if !resp.status().is_success() {
            anyhow::bail!("capability source returned status {}", resp.status());
        }
        let json: Value = resp.json().await.unwrap_or_default();
        if let Some(cap) = parse_http_capability(&json, model) {
            return Ok(cap);
        }
        anyhow::bail!("no matched model capabilities found from capability source")
    }

    async fn query_ollama_show_capability(
        &self,
        url: &str,
        model: &str,
    ) -> anyhow::Result<ModelCapabilities> {
        let resp = self
            .gw
            .http_client
            .post(url)
            .json(&serde_json::json!({ "name": model }))
            .timeout(Duration::from_secs(10))
            .send()
            .await
            .map_err(|e| anyhow::anyhow!(format_connectivity_error(&e)))?;
        if !resp.status().is_success() {
            anyhow::bail!("ollama /api/show returned status {}", resp.status());
        }
        let json: Value = resp.json().await.unwrap_or_default();
        Ok(parse_ollama_capability(&json, model))
    }
}
