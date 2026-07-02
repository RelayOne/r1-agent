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
mod discovery_state;
mod errors;
mod ipc;
mod lanes;
mod menu;
mod popout;
mod subprocess;
mod transport;

use tauri::{Emitter, Manager};

use discovery_state::DiscoveryState;
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
        // Spec §6.1 — open lane pop-out windows are tracked here so
        // app.popout_lane is idempotent (focus existing) and the
        // menu's "Lane Pop-Outs" submenu can enumerate them.
        .manage(popout::PopoutRegistry::new())
        // Spec §5 (issue #145) — host-side daemon-discovery state.
        // The discover_or_spawn task is fired from setup() below; this
        // managed state holds the result so the React banner can read
        // it via `app.discovery_status`.
        .manage(DiscoveryState::new())
        .setup(|app| {
            // Spec desktop-cortex-augmentation §9 — install the native
            // menu bar so every accelerator (⌘N, ⌘O, ⌘P, ⌘W, ⌘\,
            // ⌘1, ⌘2, kill-lane, pause/resume/cancel) routes to a
            // `menu://<id>` Tauri event the WebView listens for. Wired
            // here per audit/scan-rust-stubs.md item #4.
            if let Err(err) = menu::apply_menu(app.handle()) {
                eprintln!("[r1-desktop] menu::apply_menu failed: {err}");
            }
            setup_discovery(app)
        })
        .run(tauri::generate_context!())
        .expect("error while running R1 Desktop application");
}

// ---------------------------------------------------------------------------
// setup() — fires discover_or_spawn at startup
// ---------------------------------------------------------------------------
//
// Spec desktop-cortex-augmentation §5 says the desktop should attempt
// to attach to an externally-running r1 daemon (via ~/.r1/daemon.json
// + a TCP probe) at startup, falling back to the bundled sidecar on
// NotFound or Refused.
//
// Implementation:
//   1. Mark the DiscoveryState pending so the React banner can render
//      "Connecting…" immediately on first paint.
//   2. Spawn the discover_or_spawn future on Tauri's async runtime.
//      We must not block setup() since Tauri waits for it before
//      showing the window.
//   3. On completion, publish the result (success or error) into the
//      managed state. If discovery succeeds we DON'T tear down the
//      per-session SubprocessManager — it stays as the fallback path
//      until ipc.rs routing is migrated to the shared transport
//      (separate work item, also tracked in #145).
//
// The closure returns Ok(()) eagerly so window creation isn't blocked
// by the network I/O. Failures inside the spawned task surface as the
// `error` field of `DaemonStatusSnapshot`.
fn setup_discovery(app: &mut tauri::App) -> Result<(), Box<dyn std::error::Error>> {
    // Mark pending synchronously so the first daemon_status query
    // returns `pending: true` even before the spawned task runs.
    {
        let state: tauri::State<'_, DiscoveryState> = app.state();
        state.mark_pending();
    }

    // Move only an AppHandle into the task. Tauri's State<T> wraps the
    // managed value in a thread-safe Arc internally, so re-fetching it
    // via `app_handle.state()` inside the task gives us the SAME
    // DiscoveryState instance the IPC verbs see.
    let app_handle = app.handle().clone();
    tauri::async_runtime::spawn(async move {
        match crate::discovery::discover_or_spawn(&app_handle).await {
            Ok(handle) => {
                // Payload shape matches DaemonUpPayload in
                // desktop/src/panels/daemon-status.ts. Emitted AFTER
                // set_handle so a listener that reacts by querying
                // `app_discovery_status` sees the connected snapshot
                // (audit/complete-systems-2026-07-01.md A053 — nothing
                // emitted daemon.up/daemon.down before, so the
                // title-bar pill stayed "Offline (starting)" forever).
                let payload = serde_json::json!({
                    "url": handle.url.clone(),
                    "mode": match handle.mode {
                        crate::discovery::TransportMode::External => "external",
                        crate::discovery::TransportMode::Sidecar => "sidecar",
                    },
                    "at": chrono::Utc::now().to_rfc3339(),
                });
                let s: tauri::State<'_, DiscoveryState> = app_handle.state();
                s.set_handle(handle);
                if let Err(err) = app_handle.emit("daemon.up", payload) {
                    eprintln!("[r1-desktop] daemon.up emit failed: {err}");
                }
            }
            Err(err) => {
                let s: tauri::State<'_, DiscoveryState> = app_handle.state();
                s.set_error(format!("{err}"));
                // will_retry:false — discovery does not self-retry;
                // the user retries via the wizard / Settings → Daemon.
                let payload = serde_json::json!({
                    "reason": format!("{err}"),
                    "at": chrono::Utc::now().to_rfc3339(),
                    "will_retry": false,
                });
                if let Err(emit_err) = app_handle.emit("daemon.down", payload) {
                    eprintln!("[r1-desktop] daemon.down emit failed: {emit_err}");
                }
            }
        }
    });

    Ok(())
}
