# Desktop e2e truth — CI-2 resolution (2026-06-12)

Lane: R1-DSKE2E. Branch: `fix/desktop-e2e-truth` (off `origin/dev` @ 0f46fd1b).
Goal: make the `desktop-augmentation` e2e gate truthful so promotion PR #318
(dev → staging) can go green. Tracking node: PROGRAM-DAG-2026-05-15 §CI-2.

## Findings (evidence)

1. **Every e2e leg fails identically.** PR #318 run 27435675239: all 12 specs
   on all 3 OSes fail with `TypeError: fetch failed … connect ECONNREFUSED
   127.0.0.1:4444` at `desktop/tests/e2e/helpers/tauri-driver-session.ts:113`.
   Failing since 2026-05-16 (promotion blocker, with the Cloud Build deploy
   step being the other — separate lane).

2. **The harness does not speak WebDriver.** `tauri-driver-session.ts`
   spawns the app binary directly, then POSTs custom verbs
   (`/click`, `/waitForEvent`, `/testState`) to tauri-driver's port.
   tauri-driver implements the W3C WebDriver protocol (`POST /session`, …);
   these endpoints do not exist. The CI step's tauri-driver process is never
   actually used as a driver — it isn't even listening by test time.

3. **The app has no test-mode surface.** Grep of `desktop/src` +
   `desktop/src-tauri/src` for `R1_E2E`, `R1_FAKE_EXTERNAL_DAEMON`,
   `test://`, `test.windows.list`, `primary-window.closed`,
   `test.drive-lanes.*`, `session.workdir.*`, `dialog.open.ready` → zero
   hits. None of the driver-only `data-role` verbs the specs click
   (`r1-daemon-pill`, `new-session`, `pick-workdir`, `clear-workdir`,
   `popout-lane`, `close-primary-window`, `close-popout`, `drive-lanes`,
   `trace-lanes`, `overflow-lane`) exist in the rendered UI.

4. **tauri-driver does not support macOS.** Even a fully implemented
   test surface routed through real tauri-driver could never green the
   `e2e (macos-latest)` leg via that driver.

## Decision: Option B (truthful skip), not Option A (implement hooks)

Option A sizing: app-side test command server + event bus, fake external
daemon, sidecar banner states wired to it, ~10 driver-only UI verbs,
sessions.json semantics matching the specs, 10 Hz × 4-lane × 30 s event
generator with overflow ring + gap markers, window enumeration, dialog
auto-response — across Rust host + WebView TS, gated behind a non-production
flag. Estimated well over 300 lines (closer to 1000+), and still cannot fix
the macOS leg (finding 4). Exceeds the mission's A/B threshold.

Option B: skip every spec with a reason naming the exact missing surface;
never fabricate hook responses. What remains REAL in the e2e job:
release-profile `cargo build` of the Tauri app on all 3 OSes (the only place
the release profile is compiled in CI) and compilation/collection of the
Playwright harness with a deterministic 12/12-skipped split.

## Changes

- `desktop/tests/e2e/*.spec.ts` (4 files): file-level
  `test.skip(true, "<missing surface> not implemented … (CI-2)")`.
- `desktop/tests/e2e/helpers/tauri-driver-session.ts`: header corrected —
  documents that the verb endpoints are unimplemented and the suite is
  skipped until a real test-mode surface lands.
- `.github/workflows/desktop-augmentation.yml`: e2e job drops the unused
  tauri-driver install/start glue and Playwright browser download (no spec
  uses a browser fixture); step renamed truthfully to release build; run
  step is a plain `npx playwright test`.

## Un-skip path (tracking)

Re-enable per spec file only when the app ships a test-mode IPC surface
(gated so production builds exclude it) providing, at minimum:
banner `testState`, `session.workdir.set/cleared` + dialog stubbing,
`popout.opened/closed` + `test.windows.list` + `primary-window.closed`,
and `test.drive-lanes.*`/`test.lane-trace.dump`/`test.overflow-lane.*`.
That work is PROGRAM-DAG-2026-05-15 Layer B (R1-DSK-1…11) territory; the
harness must also be rewritten to a real driver protocol, and the macOS
leg needs a non-tauri-driver strategy.
