// SPDX-License-Identifier: MIT
//
// R1 Desktop library target — exposes internal modules so
// `desktop/src-tauri/tests/*.rs` integration tests can pin contract
// surfaces without relying on a Tauri runtime.
//
// The binary entry stays in `src/main.rs`; this file is a thin
// re-export so Cargo builds both targets from the same module tree.
// `cargo build` builds both bin + lib; `cargo test` compiles and
// runs the unit tests in each module plus the integration tests in
// `tests/`.

pub mod discovery;
pub mod errors;
pub mod lanes;
pub mod popout;
pub mod transport;

// IPC + subprocess + menu carry tauri::AppHandle types that aren't
// constructible outside a real Tauri runtime, so they're not
// exposed via the lib surface.
