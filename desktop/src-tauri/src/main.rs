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

mod ipc;
mod subprocess;

use subprocess::SubprocessManager;

fn main() {
    ipc::register_handlers()
        .manage(SubprocessManager::new())
        .run(tauri::generate_context!())
        .expect("error while running R1 Desktop application");
}
