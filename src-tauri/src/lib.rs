mod commands;

use nyro_core::{Gateway, config::GatewayConfig, logging};
use tauri::{
    Manager,
    menu::{Menu, MenuItem},
    tray::{MouseButton, MouseButtonState, TrayIcon, TrayIconBuilder, TrayIconEvent},
};

#[cfg_attr(mobile, tauri::mobile_entry_point)]
pub fn run() {
    tracing_subscriber::fmt()
        .with_env_filter("nyro=debug,tower_http=debug")
        .init();

    tauri::Builder::default()
        .on_window_event(|window, event| {
            if let tauri::WindowEvent::CloseRequested { api, .. } = event {
                api.prevent_close();
                let _ = window.hide();
            }
        })
        .plugin(tauri_plugin_single_instance::init(|app, _args, _cwd| {
            if let Some(w) = app.get_webview_window("main") {
                let _ = w.show();
                let _ = w.set_focus();
            }
        }))
        .plugin(tauri_plugin_updater::Builder::new().build())
        .plugin(tauri_plugin_opener::init())
        .plugin(tauri_plugin_autostart::init(
            tauri_plugin_autostart::MacosLauncher::LaunchAgent,
            None,
        ))
        .setup(|app| {
            let data_dir = app
                .path()
                .app_data_dir()
                .unwrap_or_else(|_| std::path::PathBuf::from(".nyro"));

            let config = GatewayConfig {
                data_dir,
                ..Default::default()
            };

            let (gateway, log_rx) = tauri::async_runtime::block_on(Gateway::new(config))?;

            let proxy_port = gateway.config.proxy_port;
            let gw_proxy = gateway.clone();
            let storage_for_logs = gateway.storage.clone();

            tauri::async_runtime::spawn(async move {
                if let Err(e) = gw_proxy.start_proxy().await {
                    tracing::error!("proxy server error: {e}");
                }
            });

            tauri::async_runtime::spawn(async move {
                logging::run_collector(log_rx, storage_for_logs).await;
            });

            app.manage(gateway);

            let tray = setup_tray(app, proxy_port)?;
            app.manage(tray);

            Ok(())
        })
        .invoke_handler(tauri::generate_handler![
            commands::get_providers,
            commands::get_provider,
            commands::get_provider_presets,
            commands::create_provider,
            commands::copy_provider,
            commands::update_provider,
            commands::delete_provider,
            commands::test_provider,
            commands::test_provider_models,
            commands::get_provider_models,
            commands::get_model_capabilities,
            commands::init_oauth_session,
            commands::get_oauth_session_status,
            commands::cancel_oauth_session,
            commands::complete_oauth_session,
            commands::create_oauth_provider,
            commands::get_provider_oauth_status,
            commands::reconnect_provider_oauth,
            commands::logout_provider_oauth,
            commands::bind_provider_oauth,
            commands::list_routes,
            commands::create_route,
            commands::update_route,
            commands::delete_route,
            commands::list_api_keys,
            commands::get_api_key,
            commands::create_api_key,
            commands::update_api_key,
            commands::delete_api_key,
            commands::query_logs,
            commands::get_log,
            commands::get_stats_overview,
            commands::get_stats_hourly,
            commands::get_stats_by_model,
            commands::get_stats_by_provider,
            commands::get_setting,
            commands::set_setting,
            commands::get_cache_settings,
            commands::update_cache_settings,
            commands::detect_embedding_dimensions,
            commands::flush_cache,
            commands::delete_cache_key,
            commands::get_cache_stats,
            commands::get_gateway_status,
            commands::export_config,
            commands::import_config,
            commands::detect_cli_tools,
            commands::sync_cli_config,
            commands::restore_cli_config,
        ])
        .build(tauri::generate_context!())
        .expect("error while running tauri application")
        .run(|app, event| {
            #[cfg(target_os = "macos")]
            if let tauri::RunEvent::Reopen {
                has_visible_windows,
                ..
            } = event
            {
                if !has_visible_windows {
                    if let Some(w) = app.get_webview_window("main") {
                        let _ = w.show();
                        let _ = w.set_focus();
                    }
                }
            }

            #[cfg(not(target_os = "macos"))]
            let _ = (app, event);
        });
}

fn setup_tray(app: &tauri::App, proxy_port: u16) -> Result<TrayIcon, Box<dyn std::error::Error>> {
    let show = MenuItem::with_id(app, "show", "Show Dashboard", true, None::<&str>)?;
    let copy_url = MenuItem::with_id(
        app,
        "copy_url",
        format!("Copy Proxy URL (:{proxy_port})"),
        true,
        None::<&str>,
    )?;
    let quit = MenuItem::with_id(app, "quit", "Quit Nyro", true, None::<&str>)?;
    let menu = Menu::with_items(app, &[&show, &copy_url, &quit])?;

    let tray = TrayIconBuilder::new()
        .icon(app.default_window_icon().unwrap().clone())
        .tooltip(&format!("Nyro AI Gateway — :{proxy_port}"))
        .menu(&menu)
        .on_tray_icon_event(|tray, event| {
            if let TrayIconEvent::Click {
                button: MouseButton::Left,
                button_state: MouseButtonState::Up,
                ..
            } = event
            {
                let app = tray.app_handle();
                if let Some(w) = app.get_webview_window("main") {
                    let _ = w.show();
                    let _ = w.set_focus();
                }
            }
        })
        .on_menu_event(move |app, event| match event.id.as_ref() {
            "show" => {
                if let Some(w) = app.get_webview_window("main") {
                    let _ = w.show();
                    let _ = w.set_focus();
                }
            }
            "copy_url" => {
                if let Some(w) = app.get_webview_window("main") {
                    let _ = w.eval(&format!(
                        "navigator.clipboard.writeText('http://127.0.0.1:{proxy_port}')"
                    ));
                }
            }
            "quit" => {
                app.exit(0);
            }
            _ => {}
        })
        .build(app)?;

    Ok(tray)
}
