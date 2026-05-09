# Rust Stub & Gap Audit — `desktop/src-tauri/`

Scanned: `desktop/src-tauri/src/*.rs` (11 files, 4109 LOC) + `desktop/src-tauri/tests/*.rs` (2 files).
Skipped: none requested (no `target/`/`vendor/` present in scope).
Cross-reference: `desktop/src/**/*.{ts,tsx}` for `invoke()` / `invokeStub()` call sites; `specs/desktop-cortex-augmentation.md`.

Severity rubric: HIGH = production crash path, broken UI verb, or unwired user-visible feature. MED = `#[allow(dead_code)]` on a real symbol that should be wired or removed; helper that always returns `None`; spec-mandated but stubbed body. LOW = test-only nits or innocuous defensive defaults.

---

## Findings

| File:Line | Severity | Category | Issue | Effort |
|---|---|---|---|---|
| desktop/src/components/discovery-wizard.tsx:54 | HIGH | unwired-tauri-cmd | Frontend calls `invoke<string>("daemon_install_command", {})` but **no `#[tauri::command] fn daemon_install_command` exists** in `src-tauri/src/*.rs`. `discovery::install_command_for_host_os()` is the intended Rust source but isn't exposed via `invoke_handler`. Wizard fails closed at runtime. | S |
| src-tauri/src/ipc.rs:629-642 | HIGH | spec-stub | `session_lanes_subscribe` advertises `tauri::ipc::Channel<LaneEvent>` per spec §8 but signature only takes `params: SessionLanesSubscribeParams`. Channel passed by `desktop/src/lib/laneSubscription.ts:62-69` (`on_event: ch`) is silently dropped. Subscription RPC succeeds but no `LaneEvent` ever flows to the WebView. | M |
| src-tauri/src/ipc.rs:857-869 | HIGH | spec-stub | `app_open_folder_picker` returns `Ok(AppOpenFolderPickerResult { path: None })` unconditionally — a noop body acknowledged in code: `"body stays a noop returning None until item 28 wires the wizard's folder picker"`. Folder picker is a published verb in spec §6.1 and registered handler list. | S |
| src-tauri/src/menu.rs:38 | HIGH | dead-allow | `#![allow(dead_code, unused_mut)]` covers the **entire native-menu module**. `apply_menu()` (line 320) never called from `main.rs` → no native menu bar, all 22 `M_*` accelerators (⌘N, ⌘O, ⌘W, ⌘\, etc.) inert. Spec §9 unmet. | L |
| src-tauri/src/transport.rs:12 | HIGH | dead-allow | `#![allow(dead_code)]` covers entire WS transport (`TransportHandle`, `BackoffPolicy`, `LifecycleEvent`, `jitter`, `urlencode`, `build_connect_url`). Never constructed from `main.rs`. Reconnect/replay (spec §16, R9 mitigation) entirely absent. Comment cites issue #145. | L |
| src-tauri/src/lanes.rs:7 | HIGH | dead-allow | `#![allow(dead_code)]` covers `LaneSubscription`, `LaneBuffer`, `LaneSink`, `LanesState::register/get/count`, the `unsubscribe` host-side fast-path, and the entire backpressure-ring (R3 mitigation). Only `LanesState::new()` wired into `main.rs:57`. Lane streaming pipeline non-functional. | L |
| src-tauri/src/discovery.rs:20 | HIGH | dead-allow | `#![allow(dead_code)]` masks `read_daemon_json`, `probe_external`, `spawn_sidecar`, `parse_ws_host_port`, `parse_listening_line`, `install_command_for_host_os`. Only `discover_or_spawn` is consumed (from `main.rs:110`). `install_command_for_host_os` should be exposed as a Tauri command (the wizard wants it). | L |
| src-tauri/src/discovery_state.rs:7 | MED | dead-allow | `#![allow(dead_code)]` covers `connect_url()` + `backoff_policy()` accessors of `DiscoveryState`. Never read from anywhere but tests. Either wire into transport.rs (which is itself unwired) or delete. | S |
| src-tauri/src/popout.rs:7 | MED | dead-allow | `#![allow(dead_code)]` covers `PopoutEntry.session_id`, `.lane_id`, `count()`, `list()`, `contains()`, `remove()`. Only `insert()` is reachable through `open_or_focus_lane_popout`. Remaining accessors are intended for `menu::refresh_pop_outs_submenu` (which is itself dead — see menu.rs). | S |
| src-tauri/src/ipc.rs:888-892 | MED | stub-helper | `futures_or_sync_any_session_id_sync()` is a documented stub: "We can't easily call async from a sync context here." Returns `None` unconditionally. Used by `cost_get_current` (line 311) and `cost_get_history` (line 332) as a fallback after `params.session_id` is missing — meaning when no `session_id` is supplied, the verb hits the `not_found` branch even when sessions exist. | M |
| src-tauri/src/ipc.rs:865 | MED | TODO-comment | Inline comment "body stays a noop returning `None` until item 28 wires the wizard's folder picker, after which it'll delegate to the dialog plugin's Rust API." Self-acknowledged unfinished. | S |
| src-tauri/src/discovery.rs:14-19 | MED | TODO-comment | "this module is built and exported but not yet called from main.rs — current shipping design spawns one r1 subprocess per session via SubprocessManager. The multi-session-daemon path is scoped for a future revision of spec 7." `discover_or_spawn` IS now called (main.rs:110) but the comment isn't updated and the `#![allow(dead_code)]` remains. | S |
| src-tauri/src/transport.rs:5-11 | MED | TODO-comment | "built but not yet wired from main.rs. Current shipping design uses a per-session WS forwarder in lanes.rs that connects directly. transport.rs implements the future shared-WS-pool path." Both transport.rs AND the lanes.rs forwarder are dead — the per-session forwarder mentioned doesn't exist either. | M |
| src-tauri/src/lanes.rs:3-6 | MED | TODO-comment | "only `LanesState::new()` is wired into main.rs — the rest of this file (LaneEvent fields, LaneRingBuffer, LaneForwarder, lane_forward command) remains pending menu/IPC wire-up." | M |
| src-tauri/src/menu.rs:24-32 | MED | TODO-comment | "the menu module is declared in main.rs but its public surface (build_menu + the M_* event-id constants) is not yet wired into Tauri's setup hook." Declared (`mod menu;`) but never invoked — entire native-menu deliverable absent. | M |
| src-tauri/src/popout.rs:1-7 | MED | TODO-comment | "PopoutRegistry::new() is wired into main.rs but its session_id / lane_id fields and count() method only fire from menu.rs's pending refresh_pop_outs_submenu hook." Pending the (dead) menu module. | S |
| src-tauri/src/discovery_state.rs:1-6 | MED | TODO-comment | "connect_url() and backoff_policy() are accessor helpers for the discovered-daemon path; pending wire-up into a Tauri command surface alongside discovery.rs." Pending forever. | S |
| src-tauri/src/subprocess.rs:56 | LOW | dead-allow | `#[allow(dead_code)]` on `RpcResponse::jsonrpc` field. Field exists for parse-shape conformance but never read. Acceptable. | S |
| src-tauri/src/subprocess.rs:97 | LOW | dead-allow | `#[allow(dead_code)]` on `Session::session_id` field. Stored at construction but never read after; `_child_handle` already uses `_` prefix to signal intentional retention. | S |
| src-tauri/src/errors.rs:22 | LOW | dead-allow | `#[allow(dead_code)]` on `IpcError::not_implemented`. Used only by tests (`ipc::tests::ipc_error_not_implemented_code`, `errors::tests::not_implemented_carries_method_name`). Production code never returns it — could be removed without effect. | S |
| src-tauri/src/main.rs:69 | LOW | expect-prod | `.expect("error while running R1 Desktop application")` on `tauri::Builder::run`. Standard Tauri boilerplate; failure here is unrecoverable. Acceptable. | S |
| src-tauri/src/subprocess.rs:180 | LOW | expect-prod | `.expect("stdout pipe always present when Stdio::piped()")` — true invariant for `Stdio::piped()`; cannot fail in practice. | S |
| src-tauri/src/subprocess.rs:184 | LOW | expect-prod | `.expect("stdin pipe always present when Stdio::piped()")` — same invariant. | S |
| src-tauri/src/discovery_state.rs:75 | LOW | expect-prod | `.expect("DiscoveryState poisoned")` on `Mutex::lock()`. Lock is held for sub-microsecond writes and a poisoned mutex means an earlier panic — propagating is correct. | S |
| src-tauri/src/discovery_state.rs:83 | LOW | expect-prod | Same lock poison guard — `set_handle`. | S |
| src-tauri/src/discovery_state.rs:91 | LOW | expect-prod | Same lock poison guard — `set_error`. | S |
| src-tauri/src/discovery_state.rs:100 | LOW | expect-prod | Same lock poison guard — `snapshot` (read path). | S |
| src-tauri/src/discovery_state.rs:121 | LOW | expect-prod | Same lock poison guard — `connect_url`. | S |
| src-tauri/src/discovery.rs:163 | LOW | unwrap_or | `rest.split_once('/').unwrap_or((rest, ""))` — total-fn default; safe. | S |
| src-tauri/src/subprocess.rs:237-481 | LOW | unwrap_or | Pervasive `.unwrap_or_default()` / `.unwrap_or(Value::Null)` / `.unwrap_or_else(\|\| "internal".into())` on `Option`-typed fields of `RpcResponse` event payloads. All defensive; correct. | S |
| src-tauri/src/ipc.rs:198,256,317,338,393,408,602,638,687,716,746,800 | LOW | unwrap_or | `serde_json::to_value(&params).unwrap_or(serde_json::json!({}))` — fallback to empty object on serialize failure. Cannot fail for the serde-derived params here, but defensive default is fine. | S |
| src-tauri/src/discovery.rs:300,307,327,359,360,369 | LOW | expect-test | `.expect()` calls inside `#[cfg(test)] mod tests` (line 294+). Test-only — acceptable. | S |
| src-tauri/src/discovery_state.rs:242 | LOW | expect-test | Inside test mod (line 163+). | S |
| src-tauri/src/transport.rs:394,406,408,416 | LOW | expect-test | Inside test mod (line 289+). | S |
| src-tauri/src/lanes.rs:419,444,445,454,457,459,461,471,472,474,476,493,501,503,505,515,516,517,524,591 | LOW | expect-test | Inside test mod (line 374+). | S |
| src-tauri/src/lanes.rs:528 | LOW | panic-test | `panic!("unexpected lane_id: {other}")` inside test mod. | S |
| src-tauri/src/ipc.rs:928,943,958,960,973,975 | LOW | expect-test | Inside test mod (line 898+). | S |
| desktop/src-tauri/tests/lanes_test.rs:92,136,170 | LOW | panic-test | `panic!()` inside integration test. | S |

---

## Summary counts

- **`unimplemented!()` / `todo!()` in non-test code:** 0
- **`panic!()` in non-test code:** 0 (the lone production-side `panic!` in lanes.rs:528 is inside `mod tests`)
- **`.unwrap()` in non-test code:** **0** (only `.unwrap_or*()` total-fn defaults, all safe)
- **`.expect()` in non-test code:** **8** total — 1 (Tauri startup), 2 (Stdio::piped pipes — invariants), 5 (Mutex lock poison guards). All structurally unavoidable; none on user input or network I/O.
- **`#[allow(dead_code)]` (or `#![allow(dead_code)]`) symbols:** 9 occurrences. Module-wide on `discovery.rs`, `transport.rs`, `lanes.rs`, `popout.rs`, `discovery_state.rs`, `menu.rs`. Per-symbol on `errors::IpcError::not_implemented`, `subprocess::RpcResponse.jsonrpc`, `subprocess::Session.session_id`.
- **Unwired Tauri commands (frontend invokes a name with no Rust handler):** **1** — `daemon_install_command` (called from `discovery-wizard.tsx:54`). All 25 commands in `register_handlers!` block ARE registered, but only 4 of them are actually invoked from production frontend code (`session_lanes_kill`, `session_lanes_subscribe`, `session_lanes_unsubscribe`, `session_set_workdir`); the rest are reachable only via the `invokeStub` shim which short-circuits and never reaches Rust. The other 21 Tauri commands are defined but the WebView side has not migrated off the stub layer — that's a frontend gap, not a Rust stub, but it does mean the Rust verbs are functionally unreachable in shipping code today.
- **Spec 7 (desktop-cortex-augmentation) deliverables missing from src-tauri/:**
  - Native menu wiring (item 25): `menu::apply_menu` never called from `main.rs::setup_discovery` or anywhere else.
  - Reconnecting WS transport (item 16): `transport::TransportHandle` never constructed; backoff/jitter/Last-Event-ID code present but unreachable.
  - Lane forwarder over `tauri::ipc::Channel<LaneEvent>` (item 17): `LanesState` registered with `manage()` but `register()`/`get()` never called; subscribe verb takes no Channel.
  - `app_open_folder_picker` body stub (item 28): always returns `path: None`.
  - `daemon_install_command` Tauri command (referenced from wizard, item 28): not declared.
  - `refresh_pop_outs_submenu` (part of item 25): defined but never called (depends on dead menu).

---

## Top 10 by impact (items a user could realistically hit)

1. **discovery-wizard.tsx:54 → missing `daemon_install_command`.** First-launch wizard panics on the missing-handler error the moment the user clicks "Show install command". HIGH user impact.
2. **`session_lanes_subscribe` drops the Channel.** Frontend opens a lane subscription, gets a `subscription_id`, but no `LaneEvent` ever arrives — the lanes sidebar (`desktop/src/panels/lane-rail.ts:205`) sits empty even when the session is producing lanes. Spec §8 acceptance criterion "render at ≥ 5 Hz" cannot be met.
3. **`app_open_folder_picker` returns `None` unconditionally.** Any UI that round-trips folder selection through Rust gets `null`. Spec §7 workdir-per-session flow falls back to `null` and the daemon never receives `cmd.Dir`.
4. **No native menu.** `menu::apply_menu` never invoked, so ⌘N / ⌘O / ⌘P / ⌘W / ⌘\ / ⌘1 / ⌘2 / kill-lane / pause / resume / cancel — every accelerator the spec promises — does nothing. Spec §9 acceptance criterion "drive every menu item with keyboard accelerator; assert the corresponding `invoke`/event fires" must fail.
5. **No WS reconnect / replay.** `transport::TransportHandle` is dead. On daemon disconnect, the desktop has no backoff schedule and no `Last-Event-ID` thread, so spec §11.3 "daemon-discovery.spec.ts: kill child PID — assert banner red, retry succeeds" cannot pass. Lane events lost on disconnect.
6. **`cost_get_current` / `cost_get_history` fail when caller omits `session_id`.** `futures_or_sync_any_session_id_sync` returns `None` always, so the optional-fallback path always hits `not_found` even when sessions exist. UI surfaces `not_found: no active session for cost query` to the user spuriously.
7. **No lane backpressure ring.** `LaneBuffer` / overflow `DeltaGap` markers / R3 mitigation are all dead. Once channel forwarding is wired, an inactive WebView tab will OOM the host (the very risk §12 R3 calls out).
8. **No `lane.status_changed` ordering guarantee.** R7 mitigation lives in `LaneSubscription::ingest` (lanes.rs:244); without that path being on the live datapath, a status flip can render before pending deltas — visible "done" lane that's still streaming.
9. **`PopoutRegistry` count/list dead.** `app_popout_lane` opens windows fine, but the menu's "Lane Pop-Outs" submenu (which would enumerate them) is part of the dead menu module. User loses the only UI that lists open pop-outs.
10. **`#![allow(dead_code)]` blanket on 6 modules masks ongoing rot.** Future contributors reading `lanes.rs` / `transport.rs` / `menu.rs` see plausible code with green CI; the modules silently drift from spec because nothing forces a wire-up. Should be downgraded to per-symbol `#[allow]` (or removed) so each unused symbol is a visible TODO.
