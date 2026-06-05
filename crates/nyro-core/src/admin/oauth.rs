use super::*;

impl AdminService {
    pub async fn init_oauth_session(
        &self,
        vendor: &str,
        use_proxy: bool,
    ) -> anyhow::Result<AuthSessionInitData> {
        let driver_key = auth::normalize_driver_key(vendor);
        if driver_key.is_empty() {
            anyhow::bail!("auth vendor cannot be empty");
        }
        let driver = auth::build_driver(&driver_key)
            .ok_or_else(|| anyhow::anyhow!("auth vendor not implemented: {driver_key}"))?;
        let client = self.gw.http_client_for_provider(use_proxy).await?;
        let created = driver
            .start(StartAuthContext {
                use_proxy,
                http_client: Some(client),
                ..Default::default()
            })
            .await?;
        let session = self.create_auth_session_record(created).await?;
        build_auth_session_init_data(&session)
    }

    pub async fn get_oauth_session_status(
        &self,
        session_id: &str,
    ) -> anyhow::Result<AuthSessionStatusData> {
        let session = self
            .get_auth_session_record(session_id)
            .await?
            .ok_or_else(|| anyhow::anyhow!("auth session not found: {session_id}"))?;

        if is_expired_at(session.expires_at.as_deref()) {
            self.delete_auth_session_record(&session.id).await?;
            return Ok(AuthSessionStatusData::Error {
                code: "AUTH_TIMEOUT".to_string(),
                message: "auth session expired".to_string(),
            });
        }

        match session.status.as_str() {
            "ready" => {
                let bundle = parse_auth_session_bundle(&session)?;
                return Ok(build_auth_session_ready_data(&session, &bundle));
            }
            "error" => {
                let message = session
                    .last_error
                    .filter(|value| !value.trim().is_empty())
                    .unwrap_or_else(|| "auth session failed".to_string());
                self.delete_auth_session_record(&session.id).await?;
                return Ok(AuthSessionStatusData::Error {
                    code: "AUTH_SESSION_ERROR".to_string(),
                    message,
                });
            }
            "cancelled" => {
                self.delete_auth_session_record(&session.id).await?;
                return Ok(AuthSessionStatusData::Error {
                    code: "AUTH_SESSION_CANCELLED".to_string(),
                    message: "auth session cancelled".to_string(),
                });
            }
            _ => {}
        }

        if session.scheme == AuthScheme::OAuthAuthCodePkce.as_str()
            || session.scheme == AuthScheme::SetupToken.as_str()
        {
            return Ok(build_auth_session_pending_data(&session));
        }

        let driver = auth::build_driver(&session.driver_key).ok_or_else(|| {
            anyhow::anyhow!("auth vendor not implemented: {}", session.driver_key)
        })?;
        let client = self.gw.http_client_for_provider(session.use_proxy).await?;

        match driver
            .poll(
                &session,
                RefreshAuthContext {
                    use_proxy: session.use_proxy,
                    http_client: Some(client),
                    ..Default::default()
                },
            )
            .await?
        {
            AuthPollState::Pending(progress) => {
                let updated = self
                    .update_auth_session_record(
                        &session.id,
                        UpdateAuthSession {
                            user_code: progress.user_code,
                            verification_uri: progress.verification_uri,
                            verification_uri_complete: progress.verification_uri_complete,
                            expires_at: progress.expires_at,
                            poll_interval_seconds: progress.poll_interval_seconds,
                            ..Default::default()
                        },
                    )
                    .await?;
                Ok(build_auth_session_pending_data(&updated))
            }
            AuthPollState::Ready(bundle) => {
                let updated = self
                    .update_auth_session_record(
                        &session.id,
                        UpdateAuthSession {
                            status: Some(AuthSessionStatus::Ready.as_str().to_string()),
                            result_json: Some(serde_json::to_string(&bundle)?),
                            expires_at: bundle.expires_at.clone(),
                            last_error: Some(String::new()),
                            ..Default::default()
                        },
                    )
                    .await?;
                Ok(build_auth_session_ready_data(&updated, &bundle))
            }
            AuthPollState::Error { code, message } => {
                self.delete_auth_session_record(&session.id).await?;
                Ok(AuthSessionStatusData::Error { code, message })
            }
        }
    }

    pub async fn cancel_oauth_session(&self, session_id: &str) -> anyhow::Result<()> {
        self.delete_auth_session_record(session_id).await
    }

    pub async fn complete_oauth_session(
        &self,
        session_id: &str,
        input: auth::AuthExchangeInput,
    ) -> anyhow::Result<AuthSessionStatusData> {
        let session = self
            .get_auth_session_record(session_id)
            .await?
            .ok_or_else(|| anyhow::anyhow!("auth session not found: {session_id}"))?;
        if !session
            .status
            .eq_ignore_ascii_case(AuthSessionStatus::Pending.as_str())
        {
            anyhow::bail!("auth session is not pending");
        }
        if is_expired_at(session.expires_at.as_deref()) {
            self.delete_auth_session_record(&session.id).await?;
            anyhow::bail!("auth session expired");
        }

        let driver = auth::build_driver(&session.driver_key).ok_or_else(|| {
            anyhow::anyhow!("auth vendor not implemented: {}", session.driver_key)
        })?;

        let client = self.gw.http_client_for_provider(session.use_proxy).await?;
        let bundle = match driver
            .exchange(
                &session,
                input,
                ExchangeAuthContext {
                    use_proxy: session.use_proxy,
                    http_client: Some(client),
                    ..Default::default()
                },
            )
            .await
        {
            Ok(bundle) => bundle,
            Err(error) => {
                self.delete_auth_session_record(&session.id).await?;
                return Err(error);
            }
        };
        let updated = self
            .update_auth_session_record(
                &session.id,
                UpdateAuthSession {
                    status: Some(AuthSessionStatus::Ready.as_str().to_string()),
                    result_json: Some(serde_json::to_string(&bundle)?),
                    expires_at: bundle.expires_at.clone(),
                    last_error: Some(String::new()),
                    ..Default::default()
                },
            )
            .await?;

        Ok(build_auth_session_ready_data(&updated, &bundle))
    }

    async fn create_auth_session_record(
        &self,
        input: auth::CreateAuthSession,
    ) -> anyhow::Result<AuthSession> {
        // NOTE (multi-replica): auth_sessions is an in-process HashMap. In a
        // multi-replica deployment the OAuth callback HTTP request must reach
        // the same replica that initiated the session (sticky session /
        // session-affinity). A future improvement is to persist sessions in the
        // shared DB, but for now callers must ensure session affinity.
        if !self.gw.config.config_poll_interval.is_zero() {
            tracing::debug!(
                "creating oauth session in multi-replica mode \
                 — ensure the callback reaches this replica (session affinity required)"
            );
        }
        let now = now_rfc3339();
        let session = AuthSession {
            id: uuid::Uuid::new_v4().to_string(),
            provider_id: input.provider_id,
            driver_key: input.driver_key,
            scheme: input.scheme,
            status: input.status,
            use_proxy: input.use_proxy,
            user_code: input.user_code,
            verification_uri: input.verification_uri,
            verification_uri_complete: input.verification_uri_complete,
            state_json: input.state_json,
            context_json: input.context_json,
            result_json: input.result_json,
            expires_at: input.expires_at,
            poll_interval_seconds: input.poll_interval_seconds,
            last_error: input.last_error,
            created_at: now.clone(),
            updated_at: now,
        };
        self.gw
            .auth_sessions
            .write()
            .await
            .insert(session.id.clone(), session.clone());
        Ok(session)
    }

    pub(super) async fn get_auth_session_record(
        &self,
        id: &str,
    ) -> anyhow::Result<Option<AuthSession>> {
        Ok(self.gw.auth_sessions.read().await.get(id).cloned())
    }

    pub(super) async fn take_ready_auth_session_record(
        &self,
        id: &str,
    ) -> anyhow::Result<AuthSession> {
        let mut sessions = self.gw.auth_sessions.write().await;
        let session = sessions
            .remove(id)
            .ok_or_else(|| anyhow::anyhow!("auth session not found: {id}"))?;
        if !session
            .status
            .eq_ignore_ascii_case(AuthSessionStatus::Ready.as_str())
        {
            sessions.insert(id.to_string(), session);
            anyhow::bail!("auth session is not ready");
        }
        Ok(session)
    }

    pub(super) async fn update_auth_session_record(
        &self,
        id: &str,
        input: UpdateAuthSession,
    ) -> anyhow::Result<AuthSession> {
        let mut sessions = self.gw.auth_sessions.write().await;
        let current = sessions
            .get_mut(id)
            .ok_or_else(|| anyhow::anyhow!("auth session not found: {id}"))?;
        if let Some(value) = input.status {
            current.status = value;
        }
        if let Some(value) = input.user_code {
            current.user_code = Some(value);
        }
        if let Some(value) = input.verification_uri {
            current.verification_uri = Some(value);
        }
        if let Some(value) = input.verification_uri_complete {
            current.verification_uri_complete = Some(value);
        }
        if let Some(value) = input.state_json {
            current.state_json = Some(value);
        }
        if let Some(value) = input.context_json {
            current.context_json = Some(value);
        }
        if let Some(value) = input.result_json {
            current.result_json = Some(value);
        }
        if let Some(value) = input.expires_at {
            current.expires_at = Some(value);
        }
        if let Some(value) = input.poll_interval_seconds {
            current.poll_interval_seconds = Some(value);
        }
        if let Some(value) = input.last_error {
            current.last_error = Some(value);
        }
        current.updated_at = now_rfc3339();
        Ok(current.clone())
    }

    async fn delete_auth_session_record(&self, id: &str) -> anyhow::Result<()> {
        self.gw.auth_sessions.write().await.remove(id);
        Ok(())
    }

    pub(super) async fn restore_auth_session_record(
        &self,
        mut session: AuthSession,
    ) -> anyhow::Result<()> {
        session.updated_at = now_rfc3339();
        self.gw
            .auth_sessions
            .write()
            .await
            .insert(session.id.clone(), session);
        Ok(())
    }
    pub(crate) async fn cleanup_auth_sessions(&self) -> anyhow::Result<usize> {
        let mut sessions = self.gw.auth_sessions.write().await;
        let before = sessions.len();
        sessions.retain(|_, session| {
            let terminal = matches!(session.status.as_str(), "error" | "cancelled");
            !terminal && !is_expired_at(session.expires_at.as_deref())
        });
        Ok(before.saturating_sub(sessions.len()))
    }
    pub async fn create_provider_with_oauth_session(
        &self,
        session_id: &str,
        mut input: CreateProvider,
    ) -> anyhow::Result<Provider> {
        let session = self.take_ready_auth_session_record(session_id).await?;
        if is_expired_at(session.expires_at.as_deref()) {
            anyhow::bail!("auth session expired");
        }

        let bundle = parse_auth_session_bundle(&session)?;
        bundle
            .access_token
            .as_deref()
            .filter(|value| !value.trim().is_empty())
            .ok_or_else(|| anyhow::anyhow!("auth session missing access token"))?;

        if input.vendor.as_deref().unwrap_or("").trim().is_empty() {
            input.vendor = Some(session.driver_key.clone());
        }
        input.auth_mode = "oauth".to_string();

        let provider = match self.create_provider(input).await {
            Ok(provider) => provider,
            Err(error) => {
                self.restore_auth_session_record(session).await?;
                return Err(error);
            }
        };

        let credential_input =
            upsert_credential_from_bundle(&session.driver_key, &session.scheme, &bundle);
        let provisioned = async {
            self.gw
                .storage
                .oauth_credentials()
                .upsert(&provider.id, credential_input)
                .await?;
            let credential =
                stored_credential_from_bundle(&session.driver_key, &session.scheme, &bundle);
            self.sync_provider_runtime_fields(&provider, &credential)
                .await
        }
        .await;

        let provider = match provisioned {
            Ok(provider) => provider,
            Err(error) => {
                if let Err(cleanup_error) = self.delete_provider(&provider.id).await {
                    tracing::warn!(
                        "failed to rollback oauth provider {} after provisioning error: {}",
                        provider.id,
                        cleanup_error
                    );
                }
                self.restore_auth_session_record(session).await?;
                return Err(error.context("create oauth provider"));
            }
        };

        Ok(provider)
    }

    pub async fn get_provider_oauth_status(
        &self,
        id: &str,
    ) -> anyhow::Result<ProviderOAuthStatusData> {
        let provider = self.get_provider(id).await?;
        let driver_key = provider
            .vendor
            .as_deref()
            .map(auth::normalize_driver_key)
            .unwrap_or_default();

        if driver_key.is_empty() {
            return Ok(build_provider_oauth_status(&provider, "", None, None));
        }

        let oauth_cred = self.gw.storage.oauth_credentials().get(id).await?;
        match oauth_cred {
            Some(cred) => Ok(build_provider_oauth_status_from_credential(
                &provider,
                &driver_key,
                &cred,
            )),
            None => Ok(build_provider_oauth_status(
                &provider,
                &driver_key,
                None,
                None,
            )),
        }
    }

    pub async fn reconnect_provider_oauth(
        &self,
        id: &str,
    ) -> anyhow::Result<ProviderOAuthStatusData> {
        let provider = self.get_provider(id).await?;
        let driver_key = provider
            .vendor
            .as_deref()
            .map(auth::normalize_driver_key)
            .unwrap_or_default();

        if driver_key.is_empty() {
            anyhow::bail!("provider vendor is empty");
        }
        let driver = auth::build_driver(&driver_key)
            .ok_or_else(|| anyhow::anyhow!("auth vendor not implemented: {driver_key}"))?;
        if !driver.metadata().supports_existing_provider {
            anyhow::bail!("auth vendor does not support reconnect: {driver_key}");
        }

        let oauth_cred = self
            .gw
            .storage
            .oauth_credentials()
            .get(&provider.id)
            .await?
            .ok_or_else(|| anyhow::anyhow!("provider oauth credential not found"))?;

        let credential = stored_credential_from_oauth(&oauth_cred, &driver_key);
        let refresh_token = credential
            .refresh_token
            .as_deref()
            .unwrap_or("")
            .trim()
            .to_string();
        if refresh_token.is_empty() {
            anyhow::bail!("provider oauth refresh token is missing");
        }

        let client = self.gw.http_client_for_provider(provider.use_proxy).await?;
        let bundle = match driver
            .refresh(
                &credential,
                RefreshAuthContext {
                    use_proxy: provider.use_proxy,
                    http_client: Some(client),
                    ..Default::default()
                },
            )
            .await
        {
            Ok(bundle) => bundle,
            Err(error) => {
                let _ = self
                    .gw
                    .storage
                    .oauth_credentials()
                    .fail_refresh(&provider.id, &error.to_string())
                    .await;
                return Ok(build_provider_oauth_status(
                    &provider,
                    &driver_key,
                    Some(AuthBindingStatus::Error.as_str().to_string()),
                    Some(error.to_string()),
                ));
            }
        };

        let refreshed_credential =
            stored_credential_from_bundle(&driver_key, driver.metadata().scheme.as_str(), &bundle);
        let credential_input =
            upsert_credential_from_bundle(&driver_key, driver.metadata().scheme.as_str(), &bundle);
        self.gw
            .storage
            .oauth_credentials()
            .upsert(&provider.id, credential_input)
            .await?;
        let refreshed_provider = self
            .sync_provider_runtime_fields(&provider, &refreshed_credential)
            .await?;

        Ok(build_provider_oauth_status(
            &refreshed_provider,
            &driver_key,
            Some(AuthBindingStatus::Connected.as_str().to_string()),
            None,
        ))
    }

    pub async fn logout_provider_oauth(&self, id: &str) -> anyhow::Result<ProviderOAuthStatusData> {
        let provider = self.get_provider(id).await?;
        let driver_key = provider
            .vendor
            .as_deref()
            .map(auth::normalize_driver_key)
            .unwrap_or_default();

        if driver_key.is_empty() {
            return Ok(build_provider_oauth_status(&provider, "", None, None));
        }

        self.gw
            .storage
            .oauth_credentials()
            .delete(&provider.id)
            .await?;

        let updated = self
            .gw
            .storage
            .providers()
            .update(
                &provider.id,
                UpdateProvider {
                    auth_mode: Some("oauth".to_string()),
                    api_key: Some(String::new()),
                    ..Default::default()
                },
            )
            .await?;

        Ok(build_provider_oauth_status(
            &updated,
            &driver_key,
            Some(AuthBindingStatus::Disconnected.as_str().to_string()),
            None,
        ))
    }

    pub async fn bind_provider_with_oauth_session(
        &self,
        provider_id: &str,
        session_id: &str,
    ) -> anyhow::Result<Provider> {
        let provider = self.get_provider(provider_id).await?;
        let session = self.take_ready_auth_session_record(session_id).await?;
        if is_expired_at(session.expires_at.as_deref()) {
            anyhow::bail!("auth session expired");
        }

        let bundle = parse_auth_session_bundle(&session)?;
        bundle
            .access_token
            .as_deref()
            .filter(|value| !value.trim().is_empty())
            .ok_or_else(|| anyhow::anyhow!("auth session missing access token"))?;

        let credential =
            stored_credential_from_bundle(&session.driver_key, &session.scheme, &bundle);
        let credential_input =
            upsert_credential_from_bundle(&session.driver_key, &session.scheme, &bundle);
        match self
            .gw
            .storage
            .oauth_credentials()
            .upsert(&provider.id, credential_input)
            .await
        {
            Ok(_) => {}
            Err(error) => {
                self.restore_auth_session_record(session).await?;
                return Err(error);
            }
        }
        let provider = match self
            .sync_provider_runtime_fields(&provider, &credential)
            .await
        {
            Ok(provider) => provider,
            Err(error) => {
                let _ = self
                    .gw
                    .storage
                    .oauth_credentials()
                    .delete(&provider.id)
                    .await;
                self.restore_auth_session_record(session).await?;
                return Err(error);
            }
        };

        Ok(provider)
    }
    pub(crate) async fn resolve_provider_runtime(
        &self,
        provider: &Provider,
    ) -> anyhow::Result<ResolvedProviderRuntime> {
        if provider.effective_auth_mode().trim() != "oauth" {
            let api_key = provider.api_key.trim().to_string();
            if api_key.is_empty() {
                anyhow::bail!("provider api key is empty");
            }
            if vertexai::is_vertex_vendor(provider) {
                let access_token = vertexai::vertex_access_token(&api_key).await?;
                let binding = RuntimeBinding {
                    base_url_override: Some(vertexai::expand_vertex_base_url(
                        &provider.base_url,
                        &api_key,
                    )),
                    ..RuntimeBinding::default()
                };
                return Ok(ResolvedProviderRuntime {
                    access_token,
                    binding,
                });
            }
            return Ok(ResolvedProviderRuntime {
                access_token: api_key,
                binding: RuntimeBinding::default(),
            });
        }

        let oauth_cred = self
            .gw
            .storage
            .oauth_credentials()
            .get(&provider.id)
            .await?;

        let oauth_cred = match oauth_cred {
            Some(c) => c,
            None => anyhow::bail!("provider oauth credential not found"),
        };

        let driver_key = if oauth_cred.driver_key.is_empty() {
            provider
                .vendor
                .as_deref()
                .map(auth::normalize_driver_key)
                .unwrap_or_default()
        } else {
            oauth_cred.driver_key.clone()
        };

        let credential = stored_credential_from_oauth(&oauth_cred, &driver_key);
        let access_token = credential
            .access_token
            .as_deref()
            .unwrap_or("")
            .trim()
            .to_string();

        if !access_token.is_empty() && !is_expired_at(credential.expires_at.as_deref()) {
            let binding = if let Some(driver) = auth::build_driver(&driver_key) {
                driver.bind_runtime(provider, &credential)?
            } else {
                RuntimeBinding::default()
            };
            return Ok(ResolvedProviderRuntime {
                access_token,
                binding,
            });
        }

        let refresh_token = credential
            .refresh_token
            .as_deref()
            .unwrap_or("")
            .trim()
            .to_string();
        if refresh_token.is_empty() {
            anyhow::bail!("provider oauth refresh token is missing");
        }

        let Some(driver) = auth::build_driver(&driver_key) else {
            anyhow::bail!("no auth driver found for key: {driver_key}");
        };

        // CAS lock: transition connected → refreshing to prevent concurrent refresh
        let oauth_store = self.gw.storage.oauth_credentials();
        let locked = oauth_store
            .try_begin_refresh(&provider.id, oauth_cred.status_version)
            .await?;
        if locked.is_none() {
            // Another caller is already refreshing — re-read and use whatever is there
            let refreshed = oauth_store.get(&provider.id).await?.ok_or_else(|| {
                anyhow::anyhow!("provider oauth credential disappeared during refresh")
            })?;
            let refreshed_token = refreshed.access_token.trim().to_string();
            if !refreshed_token.is_empty() {
                let cred = stored_credential_from_oauth(&refreshed, &driver_key);
                let binding = driver.bind_runtime(provider, &cred)?;
                return Ok(ResolvedProviderRuntime {
                    access_token: refreshed_token,
                    binding,
                });
            }
            anyhow::bail!("concurrent refresh in progress but no valid token available");
        }

        let client = self.gw.http_client_for_provider(provider.use_proxy).await?;
        let bundle = match driver
            .refresh(
                &credential,
                RefreshAuthContext {
                    use_proxy: provider.use_proxy,
                    http_client: Some(client),
                    ..Default::default()
                },
            )
            .await
        {
            Ok(bundle) => bundle,
            Err(error) => {
                let _ = oauth_store
                    .fail_refresh(&provider.id, &error.to_string())
                    .await;
                return Err(error.context("refresh oauth access token"));
            }
        };

        let refreshed_credential =
            stored_credential_from_bundle(&driver_key, driver.metadata().scheme.as_str(), &bundle);
        let credential_input =
            upsert_credential_from_bundle(&driver_key, driver.metadata().scheme.as_str(), &bundle);
        self.gw
            .storage
            .oauth_credentials()
            .complete_refresh(&provider.id, credential_input)
            .await?;
        let refreshed_provider = self
            .sync_provider_runtime_fields(provider, &refreshed_credential)
            .await?;
        let new_access_token = bundle
            .access_token
            .as_deref()
            .filter(|v| !v.trim().is_empty())
            .map(ToString::to_string)
            .ok_or_else(|| {
                anyhow::anyhow!("provider credential refresh returned empty access token")
            })?;

        Ok(ResolvedProviderRuntime {
            access_token: new_access_token,
            binding: driver.bind_runtime(&refreshed_provider, &refreshed_credential)?,
        })
    }

    pub(super) async fn sync_provider_runtime_fields(
        &self,
        provider: &Provider,
        credential: &StoredCredential,
    ) -> anyhow::Result<Provider> {
        let Some(driver) = auth::build_driver(&credential.driver_key) else {
            return Ok(provider.clone());
        };

        let binding = driver.bind_runtime(provider, credential)?;
        let base_url = binding
            .base_url_override
            .clone()
            .filter(|value| !value.trim().is_empty())
            .unwrap_or_else(|| provider.base_url.clone());
        let models_source = binding
            .models_source_override
            .clone()
            .filter(|value| !value.trim().is_empty())
            .or_else(|| provider.models_source.clone());

        self.gw
            .storage
            .providers()
            .update(
                &provider.id,
                UpdateProvider {
                    base_url: Some(base_url),
                    models_source,
                    api_key: Some(String::new()),
                    auth_mode: Some("oauth".to_string()),
                    is_enabled: Some(provider.is_enabled),
                    ..Default::default()
                },
            )
            .await
    }

    pub async fn refresh_oauth_providers(&self) -> anyhow::Result<usize> {
        let oauth_store = self.gw.storage.oauth_credentials();

        // Recover stale refreshing credentials (timeout = 60s)
        let recovered = oauth_store
            .recover_stale_refreshing(std::time::Duration::from_secs(60))
            .await
            .unwrap_or(0);
        if recovered > 0 {
            tracing::info!("recovered {recovered} stale refreshing oauth credentials");
        }

        // Find credentials expiring within 300 seconds
        let expiring = oauth_store
            .list_expiring(std::time::Duration::from_secs(300))
            .await?;

        let mut refreshed = 0usize;
        for cred in expiring {
            let has_refresh = cred
                .refresh_token
                .as_deref()
                .map(str::trim)
                .is_some_and(|value| !value.is_empty());
            if !has_refresh {
                continue;
            }

            let provider = match self.gw.storage.providers().get(&cred.provider_id).await? {
                Some(p) => p,
                None => continue,
            };

            match self.proactive_refresh_credential(&provider, &cred).await {
                Ok(_) => refreshed += 1,
                Err(error) => tracing::warn!(
                    "background oauth refresh failed for provider {} ({}): {}",
                    provider.id,
                    provider.name,
                    error
                ),
            }
        }

        Ok(refreshed)
    }

    /// Proactively refresh an OAuth credential that is approaching expiry.
    /// Unlike `resolve_provider_runtime` (which skips refresh if the token is
    /// still valid), this always attempts to obtain a new token.
    async fn proactive_refresh_credential(
        &self,
        provider: &Provider,
        cred: &OAuthCredential,
    ) -> anyhow::Result<()> {
        let driver_key = if cred.driver_key.is_empty() {
            provider
                .vendor
                .as_deref()
                .map(auth::normalize_driver_key)
                .unwrap_or_default()
        } else {
            cred.driver_key.clone()
        };

        let credential = stored_credential_from_oauth(cred, &driver_key);

        let Some(driver) = auth::build_driver(&driver_key) else {
            anyhow::bail!("no auth driver found for key: {driver_key}");
        };

        // CAS lock: transition connected → refreshing
        let oauth_store = self.gw.storage.oauth_credentials();
        let locked = oauth_store
            .try_begin_refresh(&provider.id, cred.status_version)
            .await?;
        if locked.is_none() {
            // Another caller is already refreshing — skip
            return Ok(());
        }

        let client = self.gw.http_client_for_provider(provider.use_proxy).await?;
        let bundle = match driver
            .refresh(
                &credential,
                RefreshAuthContext {
                    use_proxy: provider.use_proxy,
                    http_client: Some(client),
                    ..Default::default()
                },
            )
            .await
        {
            Ok(bundle) => bundle,
            Err(error) => {
                let _ = oauth_store
                    .fail_refresh(&provider.id, &error.to_string())
                    .await;
                return Err(error.context("proactive oauth refresh"));
            }
        };

        let refreshed_credential =
            stored_credential_from_bundle(&driver_key, driver.metadata().scheme.as_str(), &bundle);
        let credential_input =
            upsert_credential_from_bundle(&driver_key, driver.metadata().scheme.as_str(), &bundle);
        oauth_store
            .complete_refresh(&provider.id, credential_input)
            .await?;
        self.sync_provider_runtime_fields(provider, &refreshed_credential)
            .await?;
        Ok(())
    }
}
