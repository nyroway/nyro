use nyro_core::Gateway;
use nyro_core::admin::ProviderOAuthStatusData;
use nyro_core::auth::{AuthExchangeInput, AuthSessionInitData, AuthSessionStatusData};
use nyro_core::db::models::*;
use serde::{Deserialize, Serialize};
use std::collections::HashMap;
use std::io::Write;
use std::path::{Path, PathBuf};
use std::{fs, time::SystemTime};
use tauri::{Manager, State};

// ── Providers ──

#[tauri::command]
pub async fn get_providers(gw: State<'_, Gateway>) -> Result<Vec<Provider>, String> {
    gw.admin().list_providers().await.map_err(|e| e.to_string())
}

#[tauri::command]
pub async fn get_provider(gw: State<'_, Gateway>, id: String) -> Result<Provider, String> {
    gw.admin()
        .get_provider(&id)
        .await
        .map_err(|e| e.to_string())
}

#[tauri::command]
pub async fn get_provider_presets(
    gw: State<'_, Gateway>,
) -> Result<Vec<serde_json::Value>, String> {
    gw.admin()
        .list_provider_presets()
        .await
        .map_err(|e| e.to_string())
}

#[tauri::command]
pub async fn create_provider(
    gw: State<'_, Gateway>,
    input: CreateProvider,
) -> Result<Provider, String> {
    gw.admin()
        .create_provider(input)
        .await
        .map_err(|e| e.to_string())
}

#[tauri::command]
pub async fn copy_provider(gw: State<'_, Gateway>, id: String) -> Result<Provider, String> {
    gw.admin()
        .copy_provider(&id)
        .await
        .map_err(|e| e.to_string())
}

#[tauri::command]
pub async fn update_provider(
    gw: State<'_, Gateway>,
    id: String,
    input: UpdateProvider,
) -> Result<Provider, String> {
    gw.admin()
        .update_provider(&id, input)
        .await
        .map_err(|e| e.to_string())
}

#[tauri::command]
pub async fn delete_provider(gw: State<'_, Gateway>, id: String) -> Result<(), String> {
    gw.admin()
        .delete_provider(&id)
        .await
        .map_err(|e| e.to_string())
}

#[tauri::command]
pub async fn test_provider(gw: State<'_, Gateway>, id: String) -> Result<TestResult, String> {
    gw.admin()
        .test_provider(&id)
        .await
        .map_err(|e| e.to_string())
}

#[tauri::command]
pub async fn test_provider_models(
    gw: State<'_, Gateway>,
    id: String,
) -> Result<Vec<String>, String> {
    gw.admin()
        .test_provider_models(&id)
        .await
        .map_err(|e| e.to_string())
}

#[tauri::command]
pub async fn get_provider_models(
    gw: State<'_, Gateway>,
    id: String,
) -> Result<Vec<String>, String> {
    gw.admin()
        .get_provider_models(&id)
        .await
        .map_err(|e| e.to_string())
}

#[tauri::command]
pub async fn get_model_capabilities(
    gw: State<'_, Gateway>,
    provider_id: String,
    model: String,
) -> Result<ModelCapabilities, String> {
    gw.admin()
        .get_model_capabilities(&provider_id, &model)
        .await
        .map_err(|e| e.to_string())
}

#[tauri::command]
pub async fn init_oauth_session(
    gw: State<'_, Gateway>,
    vendor: String,
    use_proxy: Option<bool>,
) -> Result<AuthSessionInitData, String> {
    gw.admin()
        .init_oauth_session(&vendor, use_proxy.unwrap_or(false))
        .await
        .map_err(|e| e.to_string())
}

#[tauri::command]
pub async fn get_oauth_session_status(
    gw: State<'_, Gateway>,
    session_id: String,
) -> Result<AuthSessionStatusData, String> {
    gw.admin()
        .get_oauth_session_status(&session_id)
        .await
        .map_err(|e| e.to_string())
}

#[tauri::command]
pub async fn cancel_oauth_session(
    gw: State<'_, Gateway>,
    session_id: String,
) -> Result<(), String> {
    gw.admin()
        .cancel_oauth_session(&session_id)
        .await
        .map_err(|e| e.to_string())
}

#[tauri::command]
pub async fn complete_oauth_session(
    gw: State<'_, Gateway>,
    session_id: String,
    code: Option<String>,
    callback_url: Option<String>,
    metadata: Option<serde_json::Value>,
) -> Result<AuthSessionStatusData, String> {
    gw.admin()
        .complete_oauth_session(
            &session_id,
            AuthExchangeInput {
                code,
                callback_url,
                metadata: metadata.unwrap_or(serde_json::Value::Null),
            },
        )
        .await
        .map_err(|e| e.to_string())
}

#[tauri::command]
pub async fn create_oauth_provider(
    gw: State<'_, Gateway>,
    session_id: String,
    input: CreateProvider,
) -> Result<Provider, String> {
    gw.admin()
        .create_provider_with_oauth_session(&session_id, input)
        .await
        .map_err(|e| e.to_string())
}

#[tauri::command]
pub async fn get_provider_oauth_status(
    gw: State<'_, Gateway>,
    id: String,
) -> Result<ProviderOAuthStatusData, String> {
    gw.admin()
        .get_provider_oauth_status(&id)
        .await
        .map_err(|e| e.to_string())
}

#[tauri::command]
pub async fn reconnect_provider_oauth(
    gw: State<'_, Gateway>,
    id: String,
) -> Result<ProviderOAuthStatusData, String> {
    gw.admin()
        .reconnect_provider_oauth(&id)
        .await
        .map_err(|e| e.to_string())
}

#[tauri::command]
pub async fn logout_provider_oauth(
    gw: State<'_, Gateway>,
    id: String,
) -> Result<ProviderOAuthStatusData, String> {
    gw.admin()
        .logout_provider_oauth(&id)
        .await
        .map_err(|e| e.to_string())
}

#[tauri::command]
pub async fn bind_provider_oauth(
    gw: State<'_, Gateway>,
    id: String,
    session_id: String,
) -> Result<nyro_core::db::models::Provider, String> {
    gw.admin()
        .bind_provider_with_oauth_session(&id, &session_id)
        .await
        .map_err(|e| e.to_string())
}

// ── Routes ──

#[tauri::command]
pub async fn list_routes(gw: State<'_, Gateway>) -> Result<Vec<Route>, String> {
    gw.admin().list_routes().await.map_err(|e| e.to_string())
}

#[tauri::command]
pub async fn create_route(gw: State<'_, Gateway>, input: CreateRoute) -> Result<Route, String> {
    gw.admin()
        .create_route(input)
        .await
        .map_err(|e| e.to_string())
}

#[tauri::command]
pub async fn update_route(
    gw: State<'_, Gateway>,
    id: String,
    input: UpdateRoute,
) -> Result<Route, String> {
    gw.admin()
        .update_route(&id, input)
        .await
        .map_err(|e| e.to_string())
}

#[tauri::command]
pub async fn delete_route(gw: State<'_, Gateway>, id: String) -> Result<(), String> {
    gw.admin()
        .delete_route(&id)
        .await
        .map_err(|e| e.to_string())
}

// ── API Keys ──

#[tauri::command]
pub async fn list_api_keys(gw: State<'_, Gateway>) -> Result<Vec<ApiKeyWithBindings>, String> {
    gw.admin().list_api_keys().await.map_err(|e| e.to_string())
}

#[tauri::command]
pub async fn get_api_key(gw: State<'_, Gateway>, id: String) -> Result<ApiKeyWithBindings, String> {
    gw.admin().get_api_key(&id).await.map_err(|e| e.to_string())
}

#[tauri::command]
pub async fn create_api_key(
    gw: State<'_, Gateway>,
    input: CreateApiKey,
) -> Result<ApiKeyWithBindings, String> {
    gw.admin()
        .create_api_key(input)
        .await
        .map_err(|e| e.to_string())
}

#[tauri::command]
pub async fn update_api_key(
    gw: State<'_, Gateway>,
    id: String,
    input: UpdateApiKey,
) -> Result<ApiKeyWithBindings, String> {
    gw.admin()
        .update_api_key(&id, input)
        .await
        .map_err(|e| e.to_string())
}

#[tauri::command]
pub async fn delete_api_key(gw: State<'_, Gateway>, id: String) -> Result<(), String> {
    gw.admin()
        .delete_api_key(&id)
        .await
        .map_err(|e| e.to_string())
}

// ── Logs ──

#[tauri::command]
pub async fn query_logs(gw: State<'_, Gateway>, query: LogQuery) -> Result<LogPage, String> {
    gw.admin()
        .query_logs(query)
        .await
        .map_err(|e| e.to_string())
}

#[tauri::command]
pub async fn get_log(gw: State<'_, Gateway>, id: String) -> Result<Option<RequestLog>, String> {
    gw.admin().get_log(&id).await.map_err(|e| e.to_string())
}

// ── Stats ──

#[tauri::command]
pub async fn get_stats_overview(
    gw: State<'_, Gateway>,
    hours: Option<i32>,
) -> Result<StatsOverview, String> {
    gw.admin()
        .get_stats_overview(hours)
        .await
        .map_err(|e| e.to_string())
}

#[tauri::command]
pub async fn get_stats_hourly(
    gw: State<'_, Gateway>,
    hours: Option<i32>,
) -> Result<Vec<StatsHourly>, String> {
    gw.admin()
        .get_stats_hourly(hours.unwrap_or(24))
        .await
        .map_err(|e| e.to_string())
}

#[tauri::command]
pub async fn get_stats_by_model(
    gw: State<'_, Gateway>,
    hours: Option<i32>,
) -> Result<Vec<ModelStats>, String> {
    gw.admin()
        .get_stats_by_model(hours)
        .await
        .map_err(|e| e.to_string())
}

#[tauri::command]
pub async fn get_stats_by_provider(
    gw: State<'_, Gateway>,
    hours: Option<i32>,
) -> Result<Vec<ProviderStats>, String> {
    gw.admin()
        .get_stats_by_provider(hours)
        .await
        .map_err(|e| e.to_string())
}

// ── Settings ──

#[tauri::command]
pub async fn get_setting(gw: State<'_, Gateway>, key: String) -> Result<Option<String>, String> {
    gw.admin()
        .get_setting(&key)
        .await
        .map_err(|e| e.to_string())
}

#[tauri::command]
pub async fn set_setting(gw: State<'_, Gateway>, key: String, value: String) -> Result<(), String> {
    gw.admin()
        .set_setting(&key, &value)
        .await
        .map_err(|e| e.to_string())
}

#[tauri::command]
pub async fn get_cache_settings(gw: State<'_, Gateway>) -> Result<serde_json::Value, String> {
    gw.admin()
        .get_cache_settings()
        .await
        .map_err(|e| e.to_string())
}

#[tauri::command]
pub async fn update_cache_settings(
    gw: State<'_, Gateway>,
    input: serde_json::Value,
) -> Result<(), String> {
    gw.admin()
        .update_cache_settings(input)
        .await
        .map_err(|e| e.to_string())
}

#[tauri::command]
pub async fn detect_embedding_dimensions(
    gw: State<'_, Gateway>,
    embedding_route: String,
) -> Result<u64, String> {
    gw.admin()
        .detect_embedding_dimensions(&embedding_route)
        .await
        .map_err(|e| e.to_string())
}

#[tauri::command]
pub async fn flush_cache(gw: State<'_, Gateway>) -> Result<(), String> {
    gw.admin().flush_cache().await.map_err(|e| e.to_string())
}

#[tauri::command]
pub async fn delete_cache_key(gw: State<'_, Gateway>, key: String) -> Result<(), String> {
    gw.admin()
        .delete_cache_key(&key)
        .await
        .map_err(|e| e.to_string())
}

#[tauri::command]
pub async fn get_cache_stats(gw: State<'_, Gateway>) -> Result<serde_json::Value, String> {
    gw.admin()
        .get_cache_stats()
        .await
        .map_err(|e| e.to_string())
}

// ── Status ──

#[tauri::command]
pub async fn get_gateway_status(gw: State<'_, Gateway>) -> Result<serde_json::Value, String> {
    Ok(serde_json::json!({
        "status": "running",
        "proxy_port": gw.config.proxy_port,
    }))
}

// ── Config Import/Export ──

#[tauri::command]
pub async fn export_config(gw: State<'_, Gateway>) -> Result<ExportData, String> {
    gw.admin().export_config().await.map_err(|e| e.to_string())
}

#[tauri::command]
pub async fn import_config(
    gw: State<'_, Gateway>,
    data: ExportData,
) -> Result<ImportResult, String> {
    gw.admin()
        .import_config(data)
        .await
        .map_err(|e| e.to_string())
}

fn resolve_home_dir() -> Option<PathBuf> {
    std::env::var_os("HOME")
        .map(PathBuf::from)
        .or_else(|| std::env::var_os("USERPROFILE").map(PathBuf::from))
}

fn get_claude_settings_path(home_dir: &Path) -> PathBuf {
    let dir = home_dir.join(".claude");
    let settings_path = dir.join("settings.json");
    if settings_path.exists() {
        return settings_path;
    }
    let legacy_path = dir.join("claude.json");
    if legacy_path.exists() {
        return legacy_path;
    }
    settings_path
}

fn get_codex_auth_path(home_dir: &Path) -> PathBuf {
    home_dir.join(".codex").join("auth.json")
}

fn get_codex_config_path(home_dir: &Path) -> PathBuf {
    home_dir.join(".codex").join("config.toml")
}

fn get_codex_models_path(home_dir: &Path) -> PathBuf {
    home_dir.join(".codex").join("nyro-models.json")
}

fn get_gemini_env_path(home_dir: &Path) -> PathBuf {
    home_dir.join(".gemini").join(".env")
}

fn get_gemini_settings_path(home_dir: &Path) -> PathBuf {
    home_dir.join(".gemini").join("settings.json")
}

fn get_opencode_config_path(home_dir: &Path) -> PathBuf {
    home_dir
        .join(".config")
        .join("opencode")
        .join("opencode.json")
}

#[derive(Debug, Clone, Serialize, Deserialize)]
struct CliBackupFile {
    existed: bool,
    content: Option<Vec<u8>>,
}

type CliBackupStore = HashMap<String, HashMap<String, CliBackupFile>>;

fn cli_backup_store_path(app: &tauri::AppHandle) -> PathBuf {
    let base = app
        .path()
        .app_data_dir()
        .unwrap_or_else(|_| PathBuf::from(".nyro"));
    base.join("cli-sync-backups.json")
}

fn load_cli_backup_store(app: &tauri::AppHandle) -> Result<CliBackupStore, String> {
    let path = cli_backup_store_path(app);
    if !path.exists() {
        return Ok(HashMap::new());
    }
    let content = fs::read_to_string(&path)
        .map_err(|e| format!("failed reading backup store {}: {e}", path.display()))?;
    serde_json::from_str::<CliBackupStore>(&content)
        .map_err(|e| format!("failed parsing backup store {}: {e}", path.display()))
}

fn save_cli_backup_store(app: &tauri::AppHandle, store: &CliBackupStore) -> Result<(), String> {
    let path = cli_backup_store_path(app);
    if let Some(parent) = path.parent() {
        fs::create_dir_all(parent)
            .map_err(|e| format!("failed creating backup dir {}: {e}", parent.display()))?;
    }
    let content = serde_json::to_vec_pretty(store)
        .map_err(|e| format!("failed serializing backup store: {e}"))?;
    atomic_write_bytes(&path, &content)
}

fn capture_backups_if_missing(
    app: &tauri::AppHandle,
    tool_id: &str,
    paths: &[PathBuf],
) -> Result<(), String> {
    let mut store = load_cli_backup_store(app)?;
    let entry = store.entry(tool_id.to_string()).or_default();
    for path in paths {
        let key = path.to_string_lossy().to_string();
        if entry.contains_key(&key) {
            continue;
        }
        if path.exists() {
            let bytes = fs::read(path)
                .map_err(|e| format!("failed backing up file {}: {e}", path.display()))?;
            entry.insert(
                key,
                CliBackupFile {
                    existed: true,
                    content: Some(bytes),
                },
            );
        } else {
            entry.insert(
                key,
                CliBackupFile {
                    existed: false,
                    content: None,
                },
            );
        }
    }
    save_cli_backup_store(app, &store)
}

fn atomic_write_bytes(path: &Path, data: &[u8]) -> Result<(), String> {
    if let Some(parent) = path.parent() {
        fs::create_dir_all(parent)
            .map_err(|e| format!("failed creating directory {}: {e}", parent.display()))?;
    }

    let parent = path
        .parent()
        .ok_or_else(|| format!("invalid target path: {}", path.display()))?;
    let file_name = path
        .file_name()
        .and_then(|v| v.to_str())
        .ok_or_else(|| format!("invalid file name: {}", path.display()))?;
    let ts = SystemTime::now()
        .duration_since(SystemTime::UNIX_EPOCH)
        .map_err(|e| format!("system time error: {e}"))?
        .as_nanos();
    let tmp_path = parent.join(format!("{file_name}.tmp.{ts}"));

    {
        let mut file = fs::File::create(&tmp_path)
            .map_err(|e| format!("failed creating temp file {}: {e}", tmp_path.display()))?;
        file.write_all(data)
            .map_err(|e| format!("failed writing temp file {}: {e}", tmp_path.display()))?;
        file.flush()
            .map_err(|e| format!("failed flushing temp file {}: {e}", tmp_path.display()))?;
    }

    #[cfg(windows)]
    {
        if path.exists() {
            let _ = fs::remove_file(path);
        }
    }

    fs::rename(&tmp_path, path).map_err(|e| {
        format!(
            "failed replacing {} with {}: {e}",
            path.display(),
            tmp_path.display()
        )
    })?;
    Ok(())
}

fn write_json_file(path: &Path, value: &serde_json::Value) -> Result<(), String> {
    let bytes = serde_json::to_vec_pretty(value)
        .map_err(|e| format!("failed serializing json {}: {e}", path.display()))?;
    atomic_write_bytes(path, &bytes)
}

fn write_text_file(path: &Path, text: &str) -> Result<(), String> {
    atomic_write_bytes(path, text.as_bytes())
}

fn sanitize_claude_settings_for_live(settings: &serde_json::Value) -> serde_json::Value {
    let mut value = settings.clone();
    if let Some(obj) = value.as_object_mut() {
        obj.remove("api_format");
        obj.remove("apiFormat");
        obj.remove("openrouter_compat_mode");
        obj.remove("openrouterCompatMode");
    }
    value
}

fn infer_claude_profile(model: &str) -> &'static str {
    let m = model.to_lowercase();
    if m.contains("haiku") {
        "haiku"
    } else if m.contains("sonnet") {
        "sonnet"
    } else {
        "opus"
    }
}

fn write_gemini_env_atomic(path: &Path, env_map: &HashMap<String, String>) -> Result<(), String> {
    if let Some(parent) = path.parent() {
        fs::create_dir_all(parent)
            .map_err(|e| format!("failed creating gemini dir {}: {e}", parent.display()))?;
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            let mut perms = fs::metadata(parent)
                .map_err(|e| format!("failed reading dir metadata {}: {e}", parent.display()))?
                .permissions();
            perms.set_mode(0o700);
            fs::set_permissions(parent, perms)
                .map_err(|e| format!("failed setting dir permissions {}: {e}", parent.display()))?;
        }
    }

    let mut keys = env_map.keys().cloned().collect::<Vec<_>>();
    keys.sort();
    let content = keys
        .into_iter()
        .map(|key| format!("{key}={}", env_map.get(&key).cloned().unwrap_or_default()))
        .collect::<Vec<_>>()
        .join("\n");

    write_text_file(path, &content)?;

    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        let mut perms = fs::metadata(path)
            .map_err(|e| format!("failed reading env file metadata {}: {e}", path.display()))?
            .permissions();
        perms.set_mode(0o600);
        fs::set_permissions(path, perms).map_err(|e| {
            format!(
                "failed setting env file permissions {}: {e}",
                path.display()
            )
        })?;
    }

    Ok(())
}

fn update_gemini_selected_type(settings_path: &Path, selected_type: &str) -> Result<(), String> {
    let mut settings = if settings_path.exists() {
        fs::read_to_string(settings_path)
            .ok()
            .and_then(|content| serde_json::from_str::<serde_json::Value>(&content).ok())
            .unwrap_or_else(|| serde_json::json!({}))
    } else {
        serde_json::json!({})
    };

    if !settings.is_object() {
        settings = serde_json::json!({});
    }
    let root = settings
        .as_object_mut()
        .ok_or_else(|| "failed to update gemini settings root".to_string())?;
    let security = root
        .entry("security".to_string())
        .or_insert_with(|| serde_json::json!({}));
    if !security.is_object() {
        *security = serde_json::json!({});
    }
    let security_obj = security
        .as_object_mut()
        .ok_or_else(|| "failed to update gemini security object".to_string())?;
    let auth = security_obj
        .entry("auth".to_string())
        .or_insert_with(|| serde_json::json!({}));
    if !auth.is_object() {
        *auth = serde_json::json!({});
    }
    let auth_obj = auth
        .as_object_mut()
        .ok_or_else(|| "failed to update gemini auth object".to_string())?;
    auth_obj.insert(
        "selectedType".to_string(),
        serde_json::Value::String(selected_type.to_string()),
    );

    write_json_file(settings_path, &settings)
}

#[derive(Debug, Clone, Serialize, Deserialize, Default)]
pub struct CliModelCapabilities {
    pub context_window: Option<u64>,
    pub reasoning: Option<bool>,
}

#[tauri::command]
pub async fn sync_cli_config(
    app: tauri::AppHandle,
    tool_id: String,
    host: String,
    api_key: String,
    model: String,
    _capabilities: Option<CliModelCapabilities>,
) -> Result<Vec<String>, String> {
    let home_dir =
        resolve_home_dir().ok_or_else(|| "failed to resolve home directory".to_string())?;
    let normalized_host = host.trim().trim_end_matches('/').to_string();
    let normalized_model = model.trim().to_string();
    if normalized_host.is_empty() {
        return Err("empty host is not allowed".to_string());
    }
    if api_key.trim().is_empty() {
        return Err("empty api key is not allowed".to_string());
    }
    if normalized_model.is_empty() {
        return Err("empty model is not allowed".to_string());
    }

    let tool = tool_id.trim().to_lowercase();
    match tool.as_str() {
        "claude-code" => {
            let settings_path = get_claude_settings_path(&home_dir);
            capture_backups_if_missing(&app, &tool, &[settings_path.clone()])?;

            let mut settings = if settings_path.exists() {
                fs::read_to_string(&settings_path)
                    .ok()
                    .and_then(|content| serde_json::from_str::<serde_json::Value>(&content).ok())
                    .unwrap_or_else(|| serde_json::json!({}))
            } else {
                serde_json::json!({})
            };
            if !settings.is_object() {
                settings = serde_json::json!({});
            }
            let root = settings
                .as_object_mut()
                .ok_or_else(|| "failed to parse claude settings object".to_string())?;
            let env = root
                .entry("env".to_string())
                .or_insert_with(|| serde_json::json!({}));
            if !env.is_object() {
                *env = serde_json::json!({});
            }
            let env_obj = env
                .as_object_mut()
                .ok_or_else(|| "failed to parse claude env object".to_string())?;
            let token_key = if env_obj.contains_key("ANTHROPIC_AUTH_TOKEN") {
                "ANTHROPIC_AUTH_TOKEN"
            } else if env_obj.contains_key("ANTHROPIC_API_KEY") {
                "ANTHROPIC_API_KEY"
            } else {
                "ANTHROPIC_AUTH_TOKEN"
            };
            env_obj.insert(
                token_key.to_string(),
                serde_json::Value::String(api_key.trim().to_string()),
            );
            env_obj.insert(
                "ANTHROPIC_BASE_URL".to_string(),
                serde_json::Value::String(normalized_host.clone()),
            );
            env_obj.insert(
                "ANTHROPIC_MODEL".to_string(),
                serde_json::Value::String(normalized_model.clone()),
            );
            env_obj.insert(
                "ANTHROPIC_DEFAULT_HAIKU_MODEL".to_string(),
                serde_json::Value::String(normalized_model.clone()),
            );
            env_obj.insert(
                "ANTHROPIC_DEFAULT_SONNET_MODEL".to_string(),
                serde_json::Value::String(normalized_model.clone()),
            );
            env_obj.insert(
                "ANTHROPIC_DEFAULT_OPUS_MODEL".to_string(),
                serde_json::Value::String(normalized_model.clone()),
            );
            env_obj.insert(
                "CLAUDE_CODE_NO_FLICKER".to_string(),
                serde_json::Value::String("1".to_string()),
            );
            root.insert(
                "model".to_string(),
                serde_json::Value::String(infer_claude_profile(&normalized_model).to_string()),
            );

            let settings = sanitize_claude_settings_for_live(&settings);
            write_json_file(&settings_path, &settings)?;
            Ok(vec![settings_path.to_string_lossy().to_string()])
        }
        "codex-cli" => {
            let auth_path = get_codex_auth_path(&home_dir);
            let config_path = get_codex_config_path(&home_dir);
            let models_path = get_codex_models_path(&home_dir);
            capture_backups_if_missing(
                &app,
                &tool,
                &[auth_path.clone(), config_path.clone(), models_path.clone()],
            )?;

            let mut auth_json = if auth_path.exists() {
                fs::read_to_string(&auth_path)
                    .ok()
                    .and_then(|content| serde_json::from_str::<serde_json::Value>(&content).ok())
                    .unwrap_or_else(|| serde_json::json!({}))
            } else {
                serde_json::json!({})
            };
            if !auth_json.is_object() {
                auth_json = serde_json::json!({});
            }
            let auth_obj = auth_json
                .as_object_mut()
                .ok_or_else(|| "failed to parse codex auth object".to_string())?;
            auth_obj.insert(
                "OPENAI_API_KEY".to_string(),
                serde_json::Value::String(api_key.trim().to_string()),
            );

            write_json_file(&auth_path, &auth_json)?;

            let config_lines = vec![
                r#"model_provider = "nyro""#.to_string(),
                format!(r#"model = "{normalized_model}""#),
                r#"model_reasoning_effort = "high""#.to_string(),
                r#"disable_response_storage = true"#.to_string(),
                String::new(),
                r#"[model_providers]"#.to_string(),
                r#"[model_providers.nyro]"#.to_string(),
                r#"name = "Nyro Gateway""#.to_string(),
                format!(r#"base_url = "{}/v1""#, normalized_host),
                r#"wire_api = "responses""#.to_string(),
                r#"requires_openai_auth = true"#.to_string(),
            ];
            let config_toml = config_lines.join("\n");
            write_text_file(&config_path, &config_toml)?;

            let mut changed_paths = vec![
                auth_path.to_string_lossy().to_string(),
                config_path.to_string_lossy().to_string(),
            ];
            if models_path.exists() {
                fs::remove_file(&models_path).map_err(|e| {
                    format!(
                        "failed removing codex model catalog {}: {e}",
                        models_path.display()
                    )
                })?;
                changed_paths.push(models_path.to_string_lossy().to_string());
            }
            Ok(changed_paths)
        }
        "gemini-cli" => {
            let env_path = get_gemini_env_path(&home_dir);
            let settings_path = get_gemini_settings_path(&home_dir);
            capture_backups_if_missing(&app, &tool, &[env_path.clone(), settings_path.clone()])?;

            let env_map = HashMap::from([
                ("GEMINI_API_KEY".to_string(), api_key.trim().to_string()),
                (
                    "GOOGLE_GEMINI_BASE_URL".to_string(),
                    normalized_host.to_string(),
                ),
                ("GEMINI_MODEL".to_string(), normalized_model),
            ]);
            write_gemini_env_atomic(&env_path, &env_map)?;
            update_gemini_selected_type(&settings_path, "gemini-api-key")?;
            Ok(vec![
                env_path.to_string_lossy().to_string(),
                settings_path.to_string_lossy().to_string(),
            ])
        }
        "opencode" => {
            let config_path = get_opencode_config_path(&home_dir);
            capture_backups_if_missing(&app, &tool, &[config_path.clone()])?;

            let mut config = if config_path.exists() {
                fs::read_to_string(&config_path)
                    .ok()
                    .and_then(|content| serde_json::from_str::<serde_json::Value>(&content).ok())
                    .unwrap_or_else(|| serde_json::json!({}))
            } else {
                serde_json::json!({})
            };

            if !config.is_object() {
                config = serde_json::json!({});
            }
            let root = config
                .as_object_mut()
                .ok_or_else(|| "failed to parse opencode config object".to_string())?;
            root.entry("$schema".to_string())
                .or_insert_with(|| serde_json::json!("https://opencode.ai/config.json"));
            root.insert(
                "model".to_string(),
                serde_json::json!(format!("nyro/{}", normalized_model)),
            );
            let provider = root
                .entry("provider".to_string())
                .or_insert_with(|| serde_json::json!({}));
            if !provider.is_object() {
                *provider = serde_json::json!({});
            }
            let provider_obj = provider
                .as_object_mut()
                .ok_or_else(|| "failed to update opencode provider object".to_string())?;
            let model_name = normalized_model.clone();
            provider_obj.insert(
                "nyro".to_string(),
                serde_json::json!({
                    "name": "Nyro Gateway",
                    "npm": "@ai-sdk/openai-compatible",
                    "options": {
                        "baseURL": format!("{}/v1", normalized_host),
                        "apiKey": api_key.trim(),
                        "model": model_name.clone(),
                    },
                    "models": {
                        (model_name.clone()): {
                            "name": model_name,
                        }
                    }
                }),
            );

            write_json_file(&config_path, &config)?;
            Ok(vec![config_path.to_string_lossy().to_string()])
        }
        _ => Err(format!("unsupported cli tool: {tool_id}")),
    }
}

#[tauri::command]
pub async fn restore_cli_config(
    app: tauri::AppHandle,
    tool_id: String,
) -> Result<Vec<String>, String> {
    let normalized_tool = tool_id.trim().to_lowercase();
    let mut store = load_cli_backup_store(&app)?;
    let Some(saved_files) = store.remove(&normalized_tool) else {
        return Ok(Vec::new());
    };

    let mut restored_paths = Vec::new();
    for (path_str, backup) in saved_files {
        let path = PathBuf::from(&path_str);
        if backup.existed {
            let bytes = backup.content.unwrap_or_default();
            atomic_write_bytes(&path, &bytes)?;
        } else if path.exists() {
            fs::remove_file(&path)
                .map_err(|e| format!("failed removing file {}: {e}", path.display()))?;
        }
        restored_paths.push(path_str);
    }

    save_cli_backup_store(&app, &store)?;
    Ok(restored_paths)
}

#[tauri::command]
pub async fn detect_cli_tools() -> Result<HashMap<String, bool>, String> {
    let mut status = HashMap::from([
        ("claude-code".to_string(), false),
        ("codex-cli".to_string(), false),
        ("gemini-cli".to_string(), false),
        ("opencode".to_string(), false),
    ]);

    let Some(home_dir) = resolve_home_dir() else {
        return Ok(status);
    };

    let checks = [
        ("claude-code", home_dir.join(".claude")),
        ("codex-cli", home_dir.join(".codex")),
        ("gemini-cli", home_dir.join(".gemini")),
        ("opencode", home_dir.join(".config").join("opencode")),
    ];

    for (tool, path) in checks {
        status.insert(tool.to_string(), path.is_dir());
    }

    Ok(status)
}
