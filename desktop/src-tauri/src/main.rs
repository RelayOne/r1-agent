// SPDX-License-Identifier: MIT
//
// R1 Desktop — Tauri application entry point (R1D-1.1).
//
// Tier model (desktop/IPC-CONTRACT.md §1 / desktop/docs/architecture.md §1):
//   Tier 1: WebView (React + TypeScript)    — panels in desktop/src/
//   Tier 2: Rust host (this binary)         — subprocess lifecycle + IPC dispatch
//   Tier 3: r1 Go subprocess(es)            — one per active session
//
// Responsibilities of this binary:
//   • Boot the Tauri window and serve the WebView frontend.
//   • Manage `SubprocessManager` as a Tauri managed-state singleton.
//   • Register all `invoke_handler` commands from `ipc` module.
//   • Keep no r1-specific logic here — all of it lives in `ipc` and `subprocess`.

// Prevents an extra console window from appearing on Windows in release builds.
#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

mod discovery;
mod ipc;
mod lanes;
mod subprocess;
mod transport;

use subprocess::SubprocessManager;

fn main() {
    let mut builder = ipc::register_handlers()
        // Six Tauri 2 plugins from spec desktop-cortex-augmentation §2.
        // Order matches the dependency block in desktop/Cargo.toml.
        .plugin(tauri_plugin_websocket::init())
        .plugin(tauri_plugin_store::Builder::default().build())
        .plugin(tauri_plugin_dialog::init())
        .plugin(tauri_plugin_shell::init())
        .plugin(tauri_plugin_fs::init());

    // tauri-plugin-autostart wants the args the binary should be relaunched
    // with at login. Empty list = launch with no extra flags. The plugin
    // itself is a no-op until the user toggles "Start at login" in
    // Settings → Auto-start (item 27/29).
    builder = builder.plugin(tauri_plugin_autostart::init(
        tauri_plugin_autostart::MacosLauncher::LaunchAgent,
        Some(vec![]),
    ));

    builder
        .manage(SubprocessManager::new())
        // Spec desktop-cortex-augmentation §8 — host-side
        // subscription registry consulted by session.lanes.unsubscribe.
        .manage(lanes::LanesState::new())
        .run(tauri::generate_context!())
        .expect("error while running R1 Desktop application");
}
