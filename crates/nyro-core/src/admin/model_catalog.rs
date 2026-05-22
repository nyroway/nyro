use super::*;

pub(super) const MODELS_DEV_SNAPSHOT: &str = include_str!("../../assets/models.dev.json");
pub(super) const MODELS_DEV_RUNTIME_FILE: &str = "models.dev.json";
pub(super) const MODELS_DEV_SOURCE_URL: &str = "https://models.dev/api.json";
pub(super) const MODELS_DEV_RUNTIME_TTL: Duration = Duration::from_secs(24 * 60 * 60);
pub(super) fn resolve_models_endpoint(provider: &Provider) -> Option<String> {
    if let Some(endpoint) = provider.effective_models_source() {
        let trimmed = endpoint.trim();
        if !trimmed.is_empty() {
            return Some(trimmed.to_string());
        }
    }

    let base = provider.base_url.trim_end_matches('/');
    match provider.protocol.as_str() {
        "openai" | "openai-compatible" | "openai-compat" | "openai-responses" | "openai-resps"
        | "anthropic" | "anthropic-messages" | "anthropic-msgs" => {
            let has_base_path = reqwest::Url::parse(base)
                .ok()
                .map(|url| {
                    let pathname = url.path().trim_end_matches('/');
                    !pathname.is_empty() && pathname != "/"
                })
                .unwrap_or(false);
            if has_base_path {
                Some(format!("{base}/models"))
            } else {
                Some(format!("{base}/v1/models"))
            }
        }
        "gemini" | "google-gemini" | "google-genai" => Some(format!("{base}/v1beta/models")),
        _ => None,
    }
}

pub(super) fn runtime_binding_headers(binding: &RuntimeBinding) -> anyhow::Result<HeaderMap> {
    let mut headers = HeaderMap::new();
    for (key, value) in &binding.extra_headers {
        headers.insert(
            reqwest::header::HeaderName::from_bytes(key.as_bytes())?,
            HeaderValue::from_str(value)?,
        );
    }
    Ok(headers)
}

pub(super) fn build_model_headers(
    protocol: &str,
    vendor: Option<&str>,
    api_key: &str,
) -> anyhow::Result<HeaderMap> {
    let mut headers = HeaderMap::new();
    let is_google_vendor = vendor
        .map(str::trim)
        .is_some_and(|value| value.eq_ignore_ascii_case("google"));
    match protocol {
        "anthropic" => {
            headers.insert("x-api-key", HeaderValue::from_str(api_key)?);
            headers.insert("anthropic-version", HeaderValue::from_static("2023-06-01"));
        }
        "gemini" => {
            // Google providers may expose OpenAI-compatible /v1/models endpoints.
            // Add Bearer auth in addition to Gemini key query param.
            if is_google_vendor {
                headers.insert(
                    AUTHORIZATION,
                    HeaderValue::from_str(&format!("Bearer {api_key}"))?,
                );
            }
        }
        _ => {
            headers.insert(
                AUTHORIZATION,
                HeaderValue::from_str(&format!("Bearer {api_key}"))?,
            );
        }
    }
    Ok(headers)
}

pub(super) fn extract_models_from_response(
    _protocol: &str,
    vendor: Option<&str>,
    json: &Value,
) -> Vec<String> {
    let is_google_vendor = vendor
        .map(str::trim)
        .is_some_and(|value| value.eq_ignore_ascii_case("google"));
    let mut models = json
        .get("data")
        .and_then(|value| value.as_array())
        .into_iter()
        .flatten()
        .filter_map(|item| item.get("id").and_then(|value| value.as_str()))
        .map(|id| {
            if is_google_vendor {
                id.strip_prefix("models/").unwrap_or(id).to_string()
            } else {
                id.to_string()
            }
        })
        .collect::<Vec<_>>();

    if models.is_empty() {
        models = json
            .get("models")
            .and_then(|value| value.as_array())
            .into_iter()
            .flatten()
            .filter_map(|item| {
                item.get("name")
                    .and_then(|value| value.as_str())
                    .or_else(|| item.get("slug").and_then(|value| value.as_str()))
                    .or_else(|| item.get("id").and_then(|value| value.as_str()))
            })
            .map(|name| {
                let normalized = name.rsplit('/').next().unwrap_or(name);
                if is_google_vendor {
                    normalized
                        .strip_prefix("models/")
                        .unwrap_or(normalized)
                        .to_string()
                } else {
                    normalized.to_string()
                }
            })
            .collect::<Vec<_>>();
    }

    models.sort();
    models.dedup();
    models
}

pub(super) fn parse_static_models(raw: Option<&str>) -> Vec<String> {
    let mut models = raw
        .unwrap_or("")
        .lines()
        .flat_map(|line| line.split(','))
        .map(str::trim)
        .filter(|line| !line.is_empty())
        .map(ToString::to_string)
        .collect::<Vec<_>>();
    models.sort();
    models.dedup();
    models
}

/// Resolve the `CapabilitiesSource` strategy for a provider from its preset channel.
/// Falls back to `Auto` when no matching preset/channel is found.
pub(super) fn preset_capabilities_source(provider: &Provider) -> CapabilitiesSource {
    let Some(ref preset_key) = provider.preset_key else {
        return CapabilitiesSource::Auto;
    };
    let Some(meta) = VendorRegistry::global().metadata(preset_key) else {
        return CapabilitiesSource::Auto;
    };
    let channel_id = provider.channel.as_deref().unwrap_or("default");
    let Some(ch) = meta.channels.iter().find(|c| c.id == channel_id) else {
        return CapabilitiesSource::Auto;
    };
    ch.capabilities_source
}

pub(super) fn is_ollama_show_endpoint(url: &str) -> bool {
    url.trim_end_matches('/').ends_with("/api/show")
}

pub(super) fn parse_ollama_capability(json: &Value, model: &str) -> ModelCapabilities {
    let model_info = json.get("model_info").and_then(Value::as_object);
    let caps = json
        .get("capabilities")
        .and_then(Value::as_array)
        .map(|arr| {
            arr.iter()
                .filter_map(Value::as_str)
                .map(ToString::to_string)
                .collect::<Vec<_>>()
        })
        .unwrap_or_default();
    let has_vision = caps.iter().any(|c| c.eq_ignore_ascii_case("vision"));
    let context_window = model_info
        .and_then(extract_ollama_context_window)
        .unwrap_or(8 * 1024);
    let embedding_length = model_info.and_then(extract_ollama_embedding_length);

    ModelCapabilities {
        provider: "ollama".to_string(),
        model_id: model.to_string(),
        context_window,
        embedding_length,
        output_max_tokens: None,
        tool_call: caps.iter().any(|c| c == "tools"),
        reasoning: caps.iter().any(|c| c == "thinking"),
        input_modalities: if has_vision {
            vec!["text".to_string(), "image".to_string()]
        } else {
            vec!["text".to_string()]
        },
        output_modalities: vec!["text".to_string()],
        input_cost: Some(0.0),
        output_cost: Some(0.0),
    }
}

pub(super) fn extract_ollama_context_window(
    model_info: &serde_json::Map<String, Value>,
) -> Option<u64> {
    let arch = model_info.get("general.architecture")?.as_str()?;
    let key = format!("{arch}.context_length");
    model_info
        .get(&key)
        .and_then(Value::as_u64)
        .filter(|value| *value > 0)
}

pub(super) fn extract_ollama_embedding_length(
    model_info: &serde_json::Map<String, Value>,
) -> Option<u64> {
    if let Some(arch) = model_info
        .get("general.architecture")
        .and_then(Value::as_str)
    {
        let key = format!("{arch}.embedding_length");
        if let Some(value) = model_info
            .get(&key)
            .and_then(Value::as_u64)
            .filter(|value| *value > 0)
        {
            return Some(value);
        }
    }
    model_info
        .get("embedding_length")
        .and_then(Value::as_u64)
        .or_else(|| {
            model_info
                .get("general.embedding_length")
                .and_then(Value::as_u64)
        })
        .filter(|value| *value > 0)
}

pub async fn refresh_models_dev_runtime_cache_if_stale(
    data_dir: PathBuf,
    http_client: reqwest::Client,
) {
    if let Err(err) = refresh_models_dev_runtime_cache_inner(&data_dir, &http_client, false).await {
        tracing::warn!("models.dev runtime refresh skipped: {err}");
    }
}

pub async fn refresh_models_dev_runtime_cache_on_startup(
    data_dir: PathBuf,
    http_client: reqwest::Client,
) {
    if let Err(err) = refresh_models_dev_runtime_cache_inner(&data_dir, &http_client, true).await {
        tracing::warn!(
            "models.dev startup refresh failed, fallback to local cache/snapshot: {err}"
        );
    }
}

pub(super) fn models_dev_runtime_cache_path(data_dir: &Path) -> PathBuf {
    data_dir.join(MODELS_DEV_RUNTIME_FILE)
}

async fn refresh_models_dev_runtime_cache_inner(
    data_dir: &Path,
    http_client: &reqwest::Client,
    force_refresh: bool,
) -> anyhow::Result<()> {
    let cache_path = models_dev_runtime_cache_path(data_dir);
    if !force_refresh
        && let Ok(meta) = std::fs::metadata(&cache_path)
        && let Ok(modified_at) = meta.modified()
        && let Ok(elapsed) = modified_at.elapsed()
        && elapsed < MODELS_DEV_RUNTIME_TTL
    {
        return Ok(());
    }

    let resp = http_client
        .get(MODELS_DEV_SOURCE_URL)
        .timeout(Duration::from_secs(20))
        .send()
        .await
        .map_err(|e| anyhow::anyhow!("request models.dev failed: {e}"))?;
    if !resp.status().is_success() {
        anyhow::bail!("models.dev returned status {}", resp.status());
    }
    let body = resp
        .text()
        .await
        .map_err(|e| anyhow::anyhow!("read models.dev body failed: {e}"))?;

    // Validate payload shape before replacing local cache.
    let _: HashMap<String, ModelsDevVendor> = serde_json::from_str(&body)
        .map_err(|e| anyhow::anyhow!("invalid models.dev payload: {e}"))?;

    std::fs::create_dir_all(data_dir)?;
    let tmp_path = data_dir.join(format!("{MODELS_DEV_RUNTIME_FILE}.tmp"));
    std::fs::write(&tmp_path, body.as_bytes())?;
    std::fs::rename(&tmp_path, &cache_path)?;
    Ok(())
}

pub(super) fn parse_provider_presets_snapshot() -> anyhow::Result<Vec<Value>> {
    Ok(VendorRegistry::global().list_metadata_legacy_json())
}

pub(super) fn resolve_admin_preset_channel_auth_mode(
    preset_key: Option<&str>,
    channel_id: Option<&str>,
) -> Option<String> {
    crate::db::models::resolve_preset_channel_auth_mode(preset_key, channel_id)
}

fn parse_models_dev_data(data_dir: &Path) -> anyhow::Result<HashMap<String, ModelsDevVendor>> {
    let cache_path = models_dev_runtime_cache_path(data_dir);
    if let Ok(content) = std::fs::read_to_string(&cache_path) {
        if let Ok(parsed) = serde_json::from_str::<HashMap<String, ModelsDevVendor>>(&content) {
            return Ok(parsed);
        }
        tracing::warn!(
            "invalid models.dev runtime cache at {}, fallback to embedded snapshot",
            cache_path.display()
        );
    }
    parse_models_dev_snapshot()
}

pub(super) fn lookup_models_dev_models(
    data_dir: &Path,
    source: &str,
) -> anyhow::Result<Option<Vec<String>>> {
    let trimmed = source.trim();
    if trimmed.is_empty() {
        return Ok(None);
    }
    let vendor_key = if trimmed.eq_ignore_ascii_case("ai://models.dev") {
        ""
    } else if let Some(key) = trimmed.strip_prefix("ai://models.dev/") {
        key
    } else {
        return Ok(None);
    };
    let data = parse_models_dev_data(data_dir)?;
    if vendor_key.trim().is_empty() {
        let mut models = data
            .values()
            .flat_map(|vendor| vendor.models.keys().cloned())
            .collect::<Vec<_>>();
        models.sort();
        models.dedup();
        return Ok(Some(models));
    }
    let Some(vendor) = data.get(vendor_key) else {
        return Ok(Some(Vec::new()));
    };
    let mut models = vendor.models.keys().cloned().collect::<Vec<_>>();
    models.sort();
    Ok(Some(models))
}

pub(super) fn lookup_models_dev_capability(
    data_dir: &Path,
    vendor_key: &str,
    model: &str,
) -> Option<ModelCapabilities> {
    let data = parse_models_dev_data(data_dir).ok()?;
    match_models_dev_capability(&data, vendor_key, model)
}

pub(super) fn fuzzy_match_models_dev(data_dir: &Path, model: &str) -> Option<ModelCapabilities> {
    let data = parse_models_dev_data(data_dir).ok()?;
    match_models_dev_capability(&data, "", model)
}

fn match_models_dev_capability(
    data: &HashMap<String, ModelsDevVendor>,
    vendor_key: &str,
    model: &str,
) -> Option<ModelCapabilities> {
    let needle = model.trim().to_lowercase();
    if needle.is_empty() {
        return None;
    }

    if vendor_key.trim().is_empty() {
        for (vk, vendor) in data {
            for (model_id, entry) in &vendor.models {
                if model_id.eq_ignore_ascii_case(model) {
                    return Some(to_models_dev_capability(vk, entry));
                }
            }
        }
        let mut best: Option<(usize, ModelCapabilities)> = None;
        for (vk, vendor) in data {
            for (model_id, entry) in &vendor.models {
                if model_id.to_lowercase().contains(&needle) {
                    let cap = to_models_dev_capability(vk, entry);
                    let len = model_id.len();
                    let replace = best
                        .as_ref()
                        .map(|(prev_len, _)| len < *prev_len)
                        .unwrap_or(true);
                    if replace {
                        best = Some((len, cap));
                    }
                }
            }
        }
        return best.map(|(_, cap)| cap);
    }

    let vendor = data.get(vendor_key)?;
    for (model_id, entry) in &vendor.models {
        if model_id.eq_ignore_ascii_case(model) {
            return Some(to_models_dev_capability(vendor_key, entry));
        }
    }
    let mut best: Option<(usize, ModelCapabilities)> = None;
    for (model_id, entry) in &vendor.models {
        if model_id.to_lowercase().contains(&needle) {
            let cap = to_models_dev_capability(vendor_key, entry);
            let len = model_id.len();
            let replace = best
                .as_ref()
                .map(|(prev_len, _)| len < *prev_len)
                .unwrap_or(true);
            if replace {
                best = Some((len, cap));
            }
        }
    }
    best.map(|(_, cap)| cap)
}

pub(super) fn parse_http_capability(json: &Value, model: &str) -> Option<ModelCapabilities> {
    let arr = json.get("data").and_then(Value::as_array)?;
    let item = arr.iter().find(|entry| {
        entry
            .get("id")
            .and_then(Value::as_str)
            .is_some_and(|id| id.eq_ignore_ascii_case(model))
    })?;

    let model_id = item.get("id").and_then(Value::as_str).unwrap_or(model);
    let context_window = item
        .get("context_length")
        .and_then(Value::as_u64)
        .filter(|v| *v > 0)
        .unwrap_or(128 * 1024);
    let output_max_tokens = item
        .get("top_provider")
        .and_then(Value::as_object)
        .and_then(|obj| obj.get("max_completion_tokens"))
        .and_then(Value::as_u64)
        .filter(|v| *v > 0);
    let supported_parameters = item
        .get("supported_parameters")
        .and_then(Value::as_array)
        .cloned()
        .unwrap_or_default();
    let input_modalities = item
        .get("architecture")
        .and_then(Value::as_object)
        .and_then(|obj| obj.get("input_modalities"))
        .and_then(Value::as_array)
        .map(|arr| {
            arr.iter()
                .filter_map(Value::as_str)
                .map(ToString::to_string)
                .collect::<Vec<_>>()
        })
        .unwrap_or_else(|| vec!["text".to_string()]);
    let output_modalities = item
        .get("architecture")
        .and_then(Value::as_object)
        .and_then(|obj| obj.get("output_modalities"))
        .and_then(Value::as_array)
        .map(|arr| {
            arr.iter()
                .filter_map(Value::as_str)
                .map(ToString::to_string)
                .collect::<Vec<_>>()
        })
        .unwrap_or_else(|| vec!["text".to_string()]);
    let input_cost = item
        .get("pricing")
        .and_then(Value::as_object)
        .and_then(|obj| obj.get("prompt"))
        .and_then(parse_maybe_price_per_token);
    let output_cost = item
        .get("pricing")
        .and_then(Value::as_object)
        .and_then(|obj| obj.get("completion"))
        .and_then(parse_maybe_price_per_token);
    let tool_call = supported_parameters
        .iter()
        .any(|v| v.as_str() == Some("tools"));
    let model_lower = model_id.to_lowercase();
    let reasoning = model_lower.contains("reason")
        || model_lower.contains("thinking")
        || model_lower.contains("o1")
        || model_lower.contains("o3")
        || model_lower.contains("o4");

    Some(ModelCapabilities {
        provider: "openrouter".to_string(),
        model_id: model_id.to_string(),
        context_window,
        embedding_length: None,
        output_max_tokens,
        tool_call,
        reasoning,
        input_modalities,
        output_modalities,
        input_cost,
        output_cost,
    })
}

pub(super) fn parse_maybe_price_per_token(value: &Value) -> Option<f64> {
    let parsed = if let Some(v) = value.as_f64() {
        Some(v)
    } else if let Some(s) = value.as_str() {
        s.parse::<f64>().ok()
    } else {
        None
    }?;
    if parsed <= 0.0 {
        return None;
    }
    Some(parsed * 1_000_000.0)
}

#[derive(Debug, Clone, serde::Deserialize)]
struct ModelsDevVendor {
    #[serde(default)]
    models: HashMap<String, ModelsDevModelEntry>,
}

#[derive(Debug, Clone, serde::Deserialize)]
struct ModelsDevModelEntry {
    id: String,
    #[serde(default)]
    reasoning: bool,
    #[serde(default)]
    tool_call: bool,
    #[serde(default)]
    modalities: ModelsDevModalities,
    #[serde(default)]
    cost: ModelsDevCost,
    #[serde(default)]
    limit: ModelsDevLimit,
}

#[derive(Debug, Clone, serde::Deserialize, Default)]
struct ModelsDevModalities {
    #[serde(default)]
    input: Vec<String>,
    #[serde(default)]
    output: Vec<String>,
}

#[derive(Debug, Clone, serde::Deserialize, Default)]
struct ModelsDevCost {
    input: Option<f64>,
    output: Option<f64>,
}

#[derive(Debug, Clone, serde::Deserialize, Default)]
struct ModelsDevLimit {
    context: Option<u64>,
    output: Option<u64>,
}

fn parse_models_dev_snapshot() -> anyhow::Result<HashMap<String, ModelsDevVendor>> {
    let parsed = serde_json::from_str::<HashMap<String, ModelsDevVendor>>(MODELS_DEV_SNAPSHOT)
        .map_err(|e| anyhow::anyhow!("failed to parse models.dev snapshot: {e}"))?;
    Ok(parsed)
}

fn to_models_dev_capability(vendor_key: &str, model: &ModelsDevModelEntry) -> ModelCapabilities {
    let input_modalities = if model.modalities.input.is_empty() {
        vec!["text".to_string()]
    } else {
        model.modalities.input.clone()
    };
    let output_modalities = if model.modalities.output.is_empty() {
        vec!["text".to_string()]
    } else {
        model.modalities.output.clone()
    };

    ModelCapabilities {
        provider: vendor_key.to_string(),
        model_id: model.id.clone(),
        context_window: model.limit.context.filter(|v| *v > 0).unwrap_or(128 * 1024),
        embedding_length: None,
        output_max_tokens: model.limit.output.filter(|v| *v > 0),
        tool_call: model.tool_call,
        reasoning: model.reasoning,
        input_modalities,
        output_modalities,
        input_cost: model.cost.input,
        output_cost: model.cost.output,
    }
}
