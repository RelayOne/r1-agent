# Desktop e2e gate truth — expected-vs-registered IPC-verb table (2.2)

Date: 2026-07-08 · Lane R1-C · branch converge/r1-2026-07-08
Sources: `desktop/src-tauri/src/ipc.rs` (`register_handlers`), `desktop/IPC-CONTRACT.md`,
`desktop/src/**` (TS `invoke()` call sites), `.github/workflows/desktop-augmentation.yml`,
`desktop/tests/e2e/**`.

## Method

1. Enumerated every `#[tauri::command]` registered in `register_handlers()` (ipc.rs).
2. Enumerated every production TS `invoke("verb")` call site under `desktop/src/`
   (excluding `*.test.ts` scaffolds and doc comments).
3. Cross-referenced. The gate is honest iff **every advertised TS invoke verb has a
   real registered Rust handler** (invoked-but-unregistered = the failure mode).

## A. TS invoke verbs (production `desktop/src/`) → registered Rust handler

| TS invoke verb | Call site | Registered handler in ipc.rs? | Real body (not stub)? |
|---|---|---|---|
| `daemon_config_exists` | components/discovery-wizard-mount.tsx:59 | YES | YES (discovery::read_daemon_json) |
| `daemon_status` | components/discovery-wizard-mount.tsx:159 | YES | YES (rpc_call daemon.status) |
| `daemon_reconnect` | components/discovery-wizard-mount.tsx:173 | YES | YES (discover_or_spawn) |
| `daemon_accept_sidecar` | components/discovery-wizard-mount.tsx:193 | YES | YES (DiscoveryState.accept_sidecar) |
| `daemon_install_command` | components/discovery-wizard.tsx:54 | YES | YES (install_command_for_host_os) |
| `session_lanes_subscribe` | lib/laneSubscription.ts:62 | YES | YES (Channel sink + host fallback) |
| `session_lanes_unsubscribe` | lib/laneSubscription.ts:77 | YES | YES (LanesState.unregister / rpc) |
| `session_lanes_kill` | popout.tsx:92 | YES | YES (rpc_call session.lanes.kill) |
| `app_discovery_status` | panels/daemon-status.ts:245 | YES | YES (DiscoveryState.snapshot) |
| `transport_reconnect_status` | panels/daemon-status.ts:305 | YES | YES (Channel + 500ms watcher) |
| `session_set_workdir` | state/sessionStore.ts:254 | YES | YES (rpc_call session.set_workdir) |

Note: `sessionStore.ts:15` `invoke('session.set_workdir')` is inside a **doc comment**
describing the chain, not a call; the real call at :254 uses the underscored
`session_set_workdir`. `r1d-4.test.ts:39` `invoke("skill_list")` is a scaffold-stub
string literal in a test, not a production call.

**Result: 11/11 advertised TS verbs have a real registered Rust handler. Zero
invoked-but-unregistered gaps. The invoke surface is honest.**

## B. Registered handlers not yet invoked from TS (30 registered total)

The remaining 19 registered handlers are real (not `todo!()`/stub) bodies for verbs the
UI will `invoke` as later panels land (session/ledger/memory/cost/descent/daemon panels):
`session_start, session_pause, session_resume, session_send, session_cancel, skill_list,
skill_get, ledger_get_node, ledger_list_events, memory_list_scopes, memory_query,
cost_get_current, cost_get_history, descent_current_tier, descent_tier_history,
session_lanes_list, daemon_shutdown, app_popout_lane, app_open_folder_picker`.
Registered-and-real but not-yet-invoked is honest (a live handler awaiting its caller);
the reverse (invoked-without-handler) is the panic case, and there are none.

Several of these round-trip to the Go daemon which may answer `-32010 not_implemented`
per IPC-CONTRACT.md §6 (the Go `desktopapi.NotImplemented` scaffold). That is a
documented sentinel, not a lie: the wire method is live, the body lands later.

## C. tauri-driver / e2e harness truth

`desktop/tests/e2e/helpers/tauri-driver-session.ts` POSTs invented verbs
(`/click`, `/waitForEvent`, `/testState`) to a `tauri-driver` port. tauri-driver speaks
**W3C WebDriver**, serves none of those endpoints, and has **no macOS support**. The app
also ships **no test-mode surface** (no `R1_E2E` / `R1_FAKE_EXTERNAL_DAEMON` handling in
`desktop/src-tauri`, no `testState` verb, no `[data-role="r1-daemon-pill"]` route).

This was already reconciled truthfully on **2026-06-12** (CI-2 desktop-e2e-truth):
- All four e2e specs (`daemon-discovery`, `lanes-streaming`, `multi-session`,
  `popout-lane`) begin with `test.skip(true, "app has no test-mode driver surface … see
  audit/desktop-e2e-truth-2026-06-12.md")` — deterministic skip-with-reason, never a
  fabricated green.
- `.github/workflows/desktop-augmentation.yml` e2e job was corrected: the tauri-driver
  install/start glue was **removed** (it was never exercised), and the job now verifies
  exactly (a) the Tauri app compiles in the **release** profile on ubuntu-24.04 /
  macos-latest / windows-latest (the only CI leg that builds release), and (b) the
  Playwright harness compiles and collects with a deterministic **all-skipped / 0-failed**
  split. The workflow header states this verbatim.

## D. Verdict

- **IPC invoke surface: HONEST + COMPLETE** — every advertised TS verb maps to a real
  registered Rust handler; no missing `#[tauri::command]` to implement.
- **e2e gate: TRUTHFUL** — the workflow verifies what actually runs (release compile +
  deterministic skipped-with-reason Playwright collection); it does not claim to run a
  driver-backed suite it cannot. Specs are honestly skipped against a documented un-skip
  contract (`audit/desktop-e2e-truth-2026-06-12.md`).
- **No handler implementation required. No workflow change required → NOT BLOCKED.**
  The 2.2 concern (an e2e gate requiring capabilities the app lacks) was already resolved
  by the 2026-06-12 correction; this table re-verifies it against current HEAD.

## E. Residual (for the un-skip contract, not this pass)

Making the e2e specs actually run requires a **real, non-production-gated test-mode
surface**: `R1_E2E` / `R1_FAKE_EXTERNAL_DAEMON` handling in `desktop/src-tauri`, a
`testState` driver verb, a clickable `[data-role="r1-daemon-pill"]`, and a WebDriver
(or alternative automation) transport that supports macOS. That is Layer B work
(PROGRAM-DAG-2026-05-15), out of scope for this convergence pass; recorded so it is not
lost. Fabricating hook responses to force green is explicitly forbidden by the spec skip.
