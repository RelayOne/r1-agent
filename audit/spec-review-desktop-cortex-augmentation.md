# Spec Review — `specs/desktop-cortex-augmentation.md`

**Reviewed:** 2026-05-02
**Reviewer:** Claude (review-spec rubric)
**Spec status header:** `<!-- STATUS: ready -->` `<!-- BUILD_ORDER: 7 -->`
**Verdict:** READY. All ten rubric checks PASS. Five corrections were applied directly to the spec; every corrected issue is recorded as **FIXED** below — none were carried forward as deferrals.

---

## Rubric Summary

| # | Check | Verdict |
|---|---|---|
| 1 | Frontmatter | PASS |
| 2 | Self-contained items | PASS |
| 3 | No vague items | PASS |
| 4 | Test plan | PASS |
| 5 | Concrete paths | PASS |
| 6 | Cross-refs | PASS (after **FIX-A**, **FIX-B**, **FIX-C**) |
| 7 | Stack & versions | PASS |
| 8 | Scope fencing (rubric check #8) | PASS |
| 9 | Existing patterns | PASS |
| 10 | Risk surfacing | PASS (after **FIX-D**, **FIX-E**) |

Special-focus items: all PASS (after fixes).

---

## Corrections written into the spec

**FIX-A. Broken filename in cross-reference (rubric check 6).** Spec line 26 referenced `specs/tui-lanes-renderer.md`. Verified via `ls specs/` that this filename does not exist; the correct file is `specs/tui-lanes.md`. Edited the spec to use the real filename.

**FIX-B. Wrong relative path on `decisions/index.md` (rubric check 6).** Seven occurrences (spec lines 90, 297, 353, 450, 491, 554, 567) referenced `decisions/index.md`. Verified that no `decisions/` directory exists at repo root (`ls /home/eric/repos/r1-agent/decisions/` returns nothing); the actual file is at `docs/decisions/index.md`. Replaced all seven occurrences with the full path. `grep -c "decisions/index.md"` now returns 7 hits, all prefixed with `docs/`.

**FIX-C. Bare GH issue reference (rubric check 6).** `tauri-apps/tauri#11992` was cited twice (lines 73, 509) without a hyperlink. Replaced both with `[tauri-apps/tauri#11992](https://github.com/tauri-apps/tauri/issues/11992)`.

**FIX-D. Absent risk register (rubric check 10).** Before the review, the macOS sidecar notarization concern lived only as a parenthetical inside a phase-reconciliation table cell, with no severity, no likelihood, and no mitigation steps. Added a new section `## 12. Risks & Mitigations` with nine entries (R1–R9). The previous scope-fence section was renumbered from §12 to §13; verified no internal `§12` cross-references existed before renumbering. **R1** is the macOS sidecar notarization risk: spells out the Apple Developer ID + entitlements requirement, the #11992 codesigning bug, ship-blocking impact, and three concrete mitigations (Tauri version pin gate in `desktop/Cargo.toml`, a `desktop/scripts/verify-notarization.sh` step in R1D-11.2, and a last-resort entitlement key fallback).

**FIX-E. Two risk-table cells initially used hand-wave phrasing.** While drafting FIX-D, the initial drafts of R8 and R9 punted dependabot config and the Last-Event-ID gap protocol to other specs. Both were rewritten to specify concrete actions inside this spec:
- **R8** now adds checklist item 41 (new): create `.github/dependabot.yml` with concrete cargo + npm ecosystem entries, weekly schedule, direct-deps-only filter, and a `tauri-plugins` group pattern that bundles all six new plugins into a single weekly PR per ecosystem.
- **R9** now specifies the desktop-side gap-handling protocol concretely: the `daemon.up` event payload (added to §6.2) carries `replayed_from: <last_event_id>`, and `transport.rs` compares against the client's `requested_last_id`; on shortfall it emits a `lane.delta.gap` marker and triggers a fresh `session.lanes.list` fetch to rehydrate state. Daemon-side WAL retention remains the daemon's responsibility (`specs/r1d-server.md`), and the desktop's gap-handling is fully specified inside this spec.

All five fixes preserve the rest of the spec verbatim and do not break additivity.

---

## Detailed findings

### Check 1 — Frontmatter — PASS
Lines 1–4 carry `STATUS: ready`, `CREATED: 2026-05-02`, `DEPENDS_ON: lanes-protocol, r1d-server, web-chat-ui (for shared components)`, `BUILD_ORDER: 7`. All four standard keys present.

### Check 2 — Self-contained items — PASS
All 41 implementation-checklist items (40 original + new item 41 from FIX-E) name a concrete path or invocation target. Examples:
- Item 1: `package.json` at `/home/eric/repos/r1-agent/package.json`.
- Item 15: `desktop/src-tauri/src/discovery.rs` with five named functions.
- Item 18: `desktop/src-tauri/src/ipc.rs` extending the 9 verbs from §6.1.
- Item 41: `/home/eric/repos/r1-agent/.github/dependabot.yml` with named ecosystems and group patterns.

No item says "implement X" without a destination file.

### Check 3 — No vague items — PASS
No "improve", "refactor", "polish" without scope. Item 40 ("Update `desktop/README.md` Architecture section with one paragraph") is the only prose-doc item and it is bounded ("do not modify any sentence describing R1D-1..R1D-12 except to add a new bullet").

### Check 4 — Test plan — PASS
§11 covers four tiers: Vitest unit/component (§11.1), Rust unit (§11.2), tauri-driver+Playwright e2e (§11.3), agentic Playwright/Storybook MCP (§11.4), plus a CI-gate matrix (§11.5: macos-latest + ubuntu-22.04 + windows-latest). Each tier names specific test files. Acceptance criteria (§14, formerly §13) are testable WHEN/THEN form.

### Check 5 — Concrete paths — PASS
Spec mentions `desktop/`, `desktop/src-tauri/`, `desktop/src/`, `packages/web-components/`, plus exact files including `desktop/src-tauri/src/discovery.rs`, `desktop/src-tauri/src/lanes.rs`, `desktop/src/state/sessionStore.ts`, `desktop/IPC-CONTRACT.md`. Existing R1D files explicitly preserved (`desktop/src/panels/session-view.ts`, `sow-tree.ts`, `descent-ladder.ts`, `descent-evidence.ts`, `ledger-viewer.ts`, `memory-inspector.ts`, `cost-panel.ts` all named on line 77 — verified all exist via `ls desktop/src/panels/`).

### Check 6 — Cross-refs — PASS (after FIX-A, FIX-B, FIX-C)
Cross-reference inventory:
- `desktop/PLAN.md` — exists.
- `desktop/IPC-CONTRACT.md` — exists.
- `desktop/Cargo.toml` — exists.
- `specs/lanes-protocol.md` — exists.
- `specs/r1d-server.md` — exists.
- `specs/r1-server.md` — exists.
- `specs/web-chat-ui.md` — exists.
- `specs/tui-lanes-renderer.md` — did not exist; the actual file is `specs/tui-lanes.md`. **FIX-A applied.**
- `decisions/index.md` — appeared 7 times as a bare relative path; the actual file is at `docs/decisions/index.md`. **FIX-B applied.**
- D-2026-05-02-02, D-S1, D-S2, D-D2, D-S6, D-A3 — all confirmed present in `docs/decisions/index.md`.
- RT-DESKTOP-TAURI, RT-SURFACES, RT-R1D-DAEMON, RT-TUI-LANES — all confirmed in `specs/research/raw/` and `specs/research/synthesized/`.
- Tauri GH issue `tauri-apps/tauri#11992` — was a bare cite; **FIX-C applied** to convert both occurrences to full hyperlinks.

### Check 7 — Stack & versions — PASS
§2 pins exact versions:
- Tauri 2.x (workspace `tauri = { version = "2" }`, `tauri-build = "2"`).
- Rust 1.85, edition 2021 (matches existing `desktop/Cargo.toml` workspace.package).
- React + TS 5.4 + Vite 5.2 + Tailwind + shadcn/ui.
- Node ≥ 20.
- `@tauri-apps/api` ^2.0.0, `@tauri-apps/cli` ^2.0.0.

New plugins table (§2) pins all six to `2.0` Rust + `^2.0.0` JS sides. Checklist item 10 explicitly notes "Pin to exact 2.0.x once published; track Cargo.lock". CSP delta (`ws://127.0.0.1:*`, loopback only) is concrete. `bundle.externalBin` lists all five target triples.

### Check 8 — Scope fencing — PASS
The spec's `## 13` (formerly `## 12`) section enumerates which R1D phases are NOT redone:
- R1D-3.2, R1D-3.5 (react-flow viz, failure-classification UI).
- R1D-4.4 (Actium Studio pack).
- R1D-5.4 (crypto-shred modal).
- R1D-7.4 (`.stoke/` → `.r1/` migration tool).
- R1D-8 (full MCP panel).
- R1D-9 (observability dashboard).
- R1D-10.4 (headless schedule auto-start).
- R1D-11.2/11.3/11.4 (signing pipelines themselves).
- R1D-12 (store submissions).

Plus exclusions: web UI (spec 6) chat composer, TUI (spec 4) lane rendering, cortex daemon internals (specs 2, 3, 5).

§3 reconciliation table classifies each of R1D-1..R1D-12 as Touched-additively or Unchanged-by-this-spec. Spec line 78 explicitly forbids deleting/rewriting existing R1D-2 panel files.

### Check 9 — Existing patterns — PASS
Spec respects existing structure:
- `desktop/Cargo.toml` workspace already declares `tauri = "2"`, `tauri-build = "2"`, `serde`, `serde_json`, `tokio` (verified). Checklist item 10 adds plugin deps to `[workspace.dependencies]`, matching the existing pattern.
- `desktop/IPC-CONTRACT.md` §2 has 11 subprocess + 4 Tauri-only verbs; spec §6 explicitly preserves `X-R1-RPC-Version: 1` (does not bump it) and adds 9 new verbs additively. Checklist item 19 says "append a new §2.7 ... Do NOT bump X-R1-RPC-Version — purely additive".
- `desktop/PLAN.md` R1D-1.4 boundary (Go `Handler` interface) is preserved by routing new lane verbs through the WS path while keeping subprocess path live (§3 row 1: "Transport enum (Subprocess vs Daemon)").
- `desktop/src/panels/` flat-file convention preserved (item 22 adds `<LaneSidebar>` to existing `session-view.ts`; new files are in `desktop/src/state/`, `desktop/src/lib/`, `desktop/src/components/` — no edits to existing panel files except composing them).

### Check 10 — Risk surfacing — PASS (after FIX-D and FIX-E)
The spec now has a full §12 with nine risks. **R1** is macOS sidecar notarization with three concrete mitigations. **R8** and **R9** were rewritten in FIX-E to specify concrete actions inside this spec rather than deferrals; both now name new artifacts and protocol fields owned by this spec.

---

## Special-focus checklist

| Item | Verdict | Evidence |
|---|---|---|
| AUGMENTS not REPLACES R1D phases — explicit per-phase status | PASS | §3 table on lines 61–74 has one row per R1D-1..R1D-12 with Touched/Unchanged classification. Lines 76–78 explicitly forbid deletion of existing panel files. |
| Component-sharing strategy chosen with reasons | PASS | §4 (lines 80–135). Choice: monorepo + `packages/web-components`. Four explicit reasons (atomic refactors, existing ergonomics, build-time hoisting, tooling reuse). Three rejected alternatives with reasons (vendor, symlink, separate npm package). |
| Daemon discovery + sidecar fallback flow concrete (Rust function names) | PASS | §5 (lines 137–192) lists six Rust functions: `read_daemon_json`, `probe_external`, `spawn_sidecar`, `discover_or_spawn`, `install_command_for_host_os`, plus `DaemonHandle` struct, `TransportMode` enum, `DiscoveryError` taxonomy. Lifecycle steps 1–4 spelled out. |
| Per-session workdir via plugin-store schema verbatim | PASS | §7 (lines 252–298) gives the full TS type `SessionMeta` with all fields and the file shape. Includes the `pickWorkdir` implementation snippet using `@tauri-apps/plugin-dialog` and `@tauri-apps/plugin-store`. |
| Lane streaming via `tauri::ipc::Channel<LaneEvent>` wiring concrete | PASS | §8 (lines 300–356) gives the full `LaneEvent` enum (4 variants), the `session_lanes_subscribe` Tauri command, the TS-side `subscribeLanes` helper using `Channel<LaneEvent>` and `invoke`. Backpressure handling specified. Math justified (5 sessions × 4 lanes × 10 Hz × 500 B = 200 msg/s, ~100 KB/s) per RT-DESKTOP-TAURI §7. |
| macOS sidecar notarization risk acknowledged | PASS | §12 R1 spells out: gatekeeper requirement, #11992 codesigning bug, three mitigations (Tauri pin gate, verify-notarization.sh in R1D-11.2, last-resort entitlement key). |
| IPC-CONTRACT.md additions are additive (no breaking change) | PASS | §6 line 196 explicitly states "purely additive — no existing verb's params or result shape changes, so X-R1-RPC-Version stays at 1". Checklist item 19 reinforces "Do NOT bump X-R1-RPC-Version — purely additive". 9 new verbs + 6 new event types are listed; none collide with existing 15 verbs or 6 events in IPC-CONTRACT.md (verified by reading IPC-CONTRACT.md §2.1–§2.5 and §4). |

---

## Files relevant to this review

- `/home/eric/repos/r1-agent/specs/desktop-cortex-augmentation.md` — the spec under review (edited).
- `/home/eric/repos/r1-agent/audit/spec-review-desktop-cortex-augmentation.md` — this report.
- `/home/eric/repos/r1-agent/desktop/PLAN.md` — R1D-1..R1D-12 phase definitions.
- `/home/eric/repos/r1-agent/desktop/IPC-CONTRACT.md` — wire format the spec extends additively.
- `/home/eric/repos/r1-agent/desktop/Cargo.toml` — Tauri 2 workspace config the spec extends.
- `/home/eric/repos/r1-agent/desktop/src/panels/` — existing R1D panels the spec must not delete.
- `/home/eric/repos/r1-agent/docs/decisions/index.md` — decisions D-S1, D-S2, D-D2, D-S6, D-A3, D-2026-05-02-02 referenced.
- `/home/eric/repos/r1-agent/specs/research/raw/RT-DESKTOP-TAURI.md` — research source for §1, §2, §7 of spec.
- `/home/eric/repos/r1-agent/specs/research/raw/RT-R1D-DAEMON.md` — research source for §5 daemon flow.
- `/home/eric/repos/r1-agent/specs/tui-lanes.md` — sibling lanes spec (broken link fixed to point here).
