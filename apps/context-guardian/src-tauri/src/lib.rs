mod gateway;
mod memory;
mod storage;

use gateway::{ContextUsage, GatewayIdentity, GatewayResult, KeychainStore, SecretStore};
use std::io::Write;
use std::path::PathBuf;
use memory::{ProjectList, ProjectMemory, RevisionSummary};
use storage::{Checkpoint, ListResult, Settings, Store};
use tauri::{Manager, State};

/// Overrides the data directory (used by tests and the smoke run so they never
/// touch the user's real checkpoints).
const DATA_DIR_ENV: &str = "CONTEXT_GUARDIAN_DATA_DIR";
/// Overrides the Keychain service name (the smoke run uses a throwaway one).
const KEYCHAIN_SERVICE_ENV: &str = "CONTEXT_GUARDIAN_KEYCHAIN_SERVICE";
/// When set, the frontend runs its self-check and the app writes the report
/// to this path and exits.
const SMOKE_REPORT_ENV: &str = "CONTEXT_GUARDIAN_SMOKE_REPORT";
/// Which self-check to run: "1" (mock workflow), "2" (persistence after a
/// restart) or "gateway" (real gateway workflow driven by scripts/smoke-gateway.mjs).
const SMOKE_PHASE_ENV: &str = "CONTEXT_GUARDIAN_SMOKE_PHASE";

struct GatewayState {
    client: reqwest::Client,
    secrets: Box<dyn SecretStore>,
}

#[tauri::command]
fn list_checkpoints(store: State<'_, Store>) -> Result<ListResult, String> {
    store.list()
}

#[tauri::command]
fn get_checkpoint(store: State<'_, Store>, id: String) -> Result<Option<Checkpoint>, String> {
    store.get(&id)
}

/// Serializes `value` and refuses it if it contains the stored gateway API key.
fn guard_secret<T: serde::Serialize>(gateway: &GatewayState, value: &T) -> Result<(), String> {
    let payload = serde_json::to_string(value).map_err(|e| e.to_string())?;
    gateway::refuse_if_contains_secret(gateway.secrets.as_ref(), &payload)
}

#[tauri::command]
fn save_checkpoint(
    store: State<'_, Store>,
    gateway: State<'_, GatewayState>,
    checkpoint: Checkpoint,
) -> Result<Checkpoint, String> {
    guard_secret(&gateway, &checkpoint)?;
    store.save(&checkpoint)
}

#[tauri::command]
fn delete_checkpoint(store: State<'_, Store>, id: String) -> Result<bool, String> {
    store.delete(&id)
}

#[tauri::command]
fn load_settings(store: State<'_, Store>) -> Result<Option<Settings>, String> {
    store.load_settings()
}

#[tauri::command]
fn save_settings(store: State<'_, Store>, settings: Settings) -> Result<(), String> {
    if !settings.gateway_url.trim().is_empty() {
        gateway::normalize_base_url(&settings.gateway_url)?;
    }
    store.save_settings(&settings)
}

/// Lets the app check text it is about to copy (a continuation prompt) for the
/// stored gateway API key. The key itself is never returned.
#[tauri::command]
fn check_text_for_secret(gateway: State<'_, GatewayState>, text: String) -> Result<(), String> {
    gateway::refuse_if_contains_secret(gateway.secrets.as_ref(), &text)
        .map_err(|_| "refused: the text contains the stored gateway API key".to_string())
}

#[tauri::command]
fn list_projects(store: State<'_, Store>) -> Result<ProjectList, String> {
    store.list_projects()
}

#[tauri::command]
fn get_project_memory(store: State<'_, Store>, project_id: String) -> Result<Option<ProjectMemory>, String> {
    store.get_memory(&project_id)
}

#[tauri::command]
fn create_project(
    store: State<'_, Store>,
    gateway: State<'_, GatewayState>,
    memory: ProjectMemory,
) -> Result<ProjectMemory, String> {
    guard_secret(&gateway, &memory)?;
    store.create_project(&memory)
}

/// Saves a memory update the user approved in the preview. `expected_revision`
/// is the revision the preview was computed from.
#[tauri::command]
fn save_project_memory(
    store: State<'_, Store>,
    gateway: State<'_, GatewayState>,
    memory: ProjectMemory,
    expected_revision: u64,
) -> Result<ProjectMemory, String> {
    guard_secret(&gateway, &memory)?;
    store.save_memory(&memory, expected_revision)
}

#[tauri::command]
fn list_memory_revisions(store: State<'_, Store>, project_id: String) -> Result<Vec<RevisionSummary>, String> {
    store.list_memory_revisions(&project_id)
}

#[tauri::command]
fn get_memory_revision(store: State<'_, Store>, project_id: String, revision: u64) -> Result<ProjectMemory, String> {
    store.get_memory_revision(&project_id, revision)
}

#[tauri::command]
fn restore_memory_revision(
    store: State<'_, Store>,
    project_id: String,
    revision: u64,
    expected_revision: Option<u64>,
    updated_at: String,
) -> Result<ProjectMemory, String> {
    store.restore_memory_revision(&project_id, revision, expected_revision, &updated_at)
}

#[tauri::command]
fn storage_location(store: State<'_, Store>) -> String {
    store.root().display().to_string()
}

/// Reports only whether a key exists; the key itself never leaves Rust.
#[tauri::command]
fn gateway_has_api_key(gateway: State<'_, GatewayState>) -> Result<bool, String> {
    Ok(gateway.secrets.get()?.is_some())
}

#[tauri::command]
fn gateway_set_api_key(gateway: State<'_, GatewayState>, api_key: String) -> Result<(), String> {
    let key = gateway::validate_api_key(&api_key)?;
    gateway.secrets.set(&key)
}

#[tauri::command]
fn gateway_clear_api_key(gateway: State<'_, GatewayState>) -> Result<(), String> {
    gateway.secrets.clear()
}

#[tauri::command]
async fn gateway_fetch_usage(
    gateway: State<'_, GatewayState>,
    base_url: String,
    session_id: String,
) -> Result<GatewayResult<ContextUsage>, String> {
    Ok(gateway::fetch_context_usage(&gateway.client, gateway.secrets.as_ref(), &base_url, &session_id).await)
}

#[tauri::command]
async fn gateway_test_connection(
    gateway: State<'_, GatewayState>,
    base_url: String,
) -> Result<GatewayResult<GatewayIdentity>, String> {
    Ok(gateway::fetch_identity(&gateway.client, gateway.secrets.as_ref(), &base_url).await)
}

#[tauri::command]
fn smoke_mode() -> Option<String> {
    std::env::var_os(SMOKE_REPORT_ENV)?;
    Some(std::env::var(SMOKE_PHASE_ENV).unwrap_or_else(|_| "1".to_string()))
}

/// Lets the in-app self-check tell the external smoke driver which step it
/// has reached (appends a line to `<report>.signals`).
#[tauri::command]
fn smoke_signal(name: String) -> Result<(), String> {
    let path = std::env::var_os(SMOKE_REPORT_ENV).ok_or("smoke mode is not enabled")?;
    let mut signals = PathBuf::from(path).into_os_string();
    signals.push(".signals");
    let mut file = std::fs::OpenOptions::new()
        .create(true)
        .append(true)
        .open(PathBuf::from(signals))
        .map_err(|e| e.to_string())?;
    writeln!(file, "{name}").map_err(|e| e.to_string())
}

/// Receives the frontend's self-check results, adds what only the native side
/// can see (window flags), writes the report and exits.
#[tauri::command]
fn smoke_report(app: tauri::AppHandle, report: serde_json::Value) -> Result<(), String> {
    let path = std::env::var_os(SMOKE_REPORT_ENV).ok_or("smoke mode is not enabled")?;
    let window = app.get_webview_window("main").ok_or("main window missing")?;
    let native = serde_json::json!({
        "alwaysOnTop": window.is_always_on_top().map_err(|e| e.to_string())?,
        "decorated": window.is_decorated().map_err(|e| e.to_string())?,
        "visible": window.is_visible().map_err(|e| e.to_string())?,
        "innerSize": window.inner_size().map(|s| [s.width, s.height]).map_err(|e| e.to_string())?,
        "scaleFactor": window.scale_factor().map_err(|e| e.to_string())?,
    });
    let full = serde_json::json!({ "frontend": report, "native": native });
    std::fs::write(&path, serde_json::to_vec_pretty(&full).map_err(|e| e.to_string())?)
        .map_err(|e| e.to_string())?;
    let passed = full["frontend"]["passed"].as_bool().unwrap_or(false);
    app.exit(if passed { 0 } else { 1 });
    Ok(())
}

#[cfg_attr(mobile, tauri::mobile_entry_point)]
pub fn run() {
    tauri::Builder::default()
        .plugin(tauri_plugin_clipboard_manager::init())
        .setup(|app| {
            let root = match std::env::var_os(DATA_DIR_ENV) {
                Some(dir) => PathBuf::from(dir),
                None => app.path().app_data_dir()?,
            };
            app.manage(Store::new(root));
            let service = std::env::var(KEYCHAIN_SERVICE_ENV)
                .unwrap_or_else(|_| gateway::DEFAULT_KEYCHAIN_SERVICE.to_string());
            app.manage(GatewayState {
                client: gateway::build_client(gateway::REQUEST_TIMEOUT),
                secrets: Box::new(KeychainStore::new(service)),
            });
            Ok(())
        })
        .invoke_handler(tauri::generate_handler![
            list_checkpoints,
            get_checkpoint,
            save_checkpoint,
            delete_checkpoint,
            load_settings,
            save_settings,
            storage_location,
            check_text_for_secret,
            list_projects,
            get_project_memory,
            create_project,
            save_project_memory,
            list_memory_revisions,
            get_memory_revision,
            restore_memory_revision,
            gateway_has_api_key,
            gateway_set_api_key,
            gateway_clear_api_key,
            gateway_fetch_usage,
            gateway_test_connection,
            smoke_mode,
            smoke_signal,
            smoke_report
        ])
        .run(tauri::generate_context!())
        .expect("error while running SUEZ Context Guardian");
}
