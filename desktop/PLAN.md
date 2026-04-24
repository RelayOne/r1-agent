# R1 Desktop — Implementation Plan

> Lifted verbatim from `plans/work-orders/work-r1-desktop-app.md` §7 (phases)
> and §8 (effort estimate), with dates pinned at scoping time.
>
> **Filed:** 2026-04-23. **Status:** SCOPED. No item VERIFIED — forward-scoping only.
>
> Phase prefix: `R1D-*` (R1 Desktop). Each phase specifies deliverables +
> AC + estimated duration.

---

## Milestone pinning (planning dates)

Assumes work starts **2026-05-01** (one sprint after scoping), single
engineer, no blocked dependencies. Re-baseline on R1D-1 kickoff.

| Milestone | Target date | Gate |
|---|---|---|
| R1D-1 complete | 2026-05-08 | `cargo tauri dev` runs end-to-end with r1 subprocess |
| R1D-2 complete | 2026-05-15 | Session view renders a real r1 run with tool calls |
| R1D-3 complete | 2026-05-29 | SOW tree + T1..T8 verification descent live |
| R1D-4 complete | 2026-06-05 | Studio-pack install from GUI works |
| R1D-5 complete | 2026-06-12 | Ledger browser + verify-chain + crypto-shred |
| R1D-6 complete | 2026-06-19 | Memory bus viewer across 5 scopes |
| R1D-7 complete | 2026-06-26 | Settings + vault + provider selection |
| R1D-8 complete | 2026-07-03 | MCP servers panel |
| R1D-9 complete | 2026-07-10 | Observability dashboard |
| R1D-10 complete | 2026-07-24 | Multi-session + approval queue + scheduling |
| R1D-11 complete | 2026-08-07 | Polish + signing + notarization + auto-update |
| R1D-12 complete | 2026-08-14 | Store submissions live |
| **v1 GA** | **2026-08-14** | All 12 phases shipped; 5 concurrent sessions stable on 16 GB MBP |

If two engineers split UI / runtime tracks, GA pulls in to **2026-06-26** (8
weeks from kickoff).

---

## R1D-1 — Tauri scaffold + r1 subprocess IPC (1 week)

- [ ] R1D-1.1: `cargo tauri init` in `/home/eric/repos/stoke/desktop/` with
      React+TS+Vite+Tailwind+shadcn/ui template.
- [ ] R1D-1.2: Rust sidecar — subprocess launcher that spawns `r1 --one-shot`
      (or `r1 serve` if a long-lived server mode exists; confirm before start),
      parses stdout JSON events.
- [ ] R1D-1.3: Tauri event forwarding from Rust to WebView — one-way stream of
      parsed events.
- [ ] R1D-1.4: Tauri `invoke` commands for the 4 MVP RPC verbs: `session.create`,
      `session.send`, `session.cancel`, `skill.list`.
- [ ] R1D-1.5: WebView: single-page IPC-test surface with prompt input, reply
      display pane, and session-start button.
- [ ] **AC:** `cargo tauri dev` launches the Tauri window on macOS, Windows,
      and Linux; typing a prompt spawns an r1 subprocess; subprocess stdout
      event stream parses without error; reply display renders streamed
      response in under 500 ms per event; cancel button SIGTERMs the
      subprocess cleanly.

## R1D-2 — Session view + basic chat (1 week)

- [ ] R1D-2.1: Session view component with chat transcript + composer.
- [ ] R1D-2.2: Tool-use rendering — inline collapsible blocks per tool call.
- [ ] R1D-2.3: Markdown rendering via react-markdown with syntax-highlighted
      code blocks.
- [ ] R1D-2.4: Multi-session sidebar — switch between concurrent sessions.
- [ ] R1D-2.5: Cancel, pause, resume controls.
- [ ] **AC:** End-to-end: create a session, send a prompt, receive a streamed
      reply with tool calls, cancel mid-run. 2 concurrent sessions switch
      cleanly.

## R1D-3 — SOW tree + verification descent panel (1-2 weeks)

- [ ] R1D-3.1: SOW tree sidebar — reads from `plan/` via new RPC
      `plan.get(session_id)`.
- [ ] R1D-3.2: Dependency-graph visualization (react-flow or d3-hierarchy)
      for complex SOWs.
- [ ] R1D-3.3: Verification descent panel with T1..T8 grid.
- [ ] R1D-3.4: Evidence drill-down per tier.
- [ ] R1D-3.5: Failure-classification UI wired to `failure/` package.
- [ ] **AC:** A mission with 3 ACs and 8-deep SOW renders completely;
      clicking any T-cell shows evidence; retry-failed button triggers
      re-execution.

## R1D-4 — Skill catalog + Studio pack integration (1 week)

- [ ] R1D-4.1: Skill catalog browser with faceted filters.
- [ ] R1D-4.2: Manifest detail renderer (7 required fields).
- [ ] R1D-4.3: Skills marketplace with install/uninstall.
- [ ] R1D-4.4: Bundled install of the Actium-Studio pack (from
      work-r1-actium-studio-skills.md) one-click.
- [ ] R1D-4.5: "Test this skill" modal with input-form generation from JSON
      schema.
- [ ] **AC:** User installs the Actium Studio pack from the GUI, finds
      `studio.scaffold_site`, runs a test invocation against a staging
      Studio instance.

## R1D-5 — Ledger browser + crypto-shred controls (1 week)

- [ ] R1D-5.1: Ledger browser with session list + node timeline.
- [ ] R1D-5.2: Node-detail drawer rendering all 22 node types from
      `ledger/nodes/`.
- [ ] R1D-5.3: Verify-chain button + result visualization.
- [ ] R1D-5.4: Crypto-shred action with double-confirm modal and meta-ledger
      write.
- [ ] R1D-5.5: Export-session NDJSON.
- [ ] **AC:** Verify-chain passes on a clean session; intentionally corrupt
      a node on disk → verify-chain fails with exact offset; crypto-shred a
      node → verify-chain still passes but content is gone.

## R1D-6 — Memory bus viewer (1 week)

- [ ] R1D-6.1: 5-scope tab layout (Session / Worker / AllSessions / Global /
      Always).
- [ ] R1D-6.2: Key/value table with sort + filter per scope.
- [ ] R1D-6.3: Row drill-down with write/read history.
- [ ] R1D-6.4: Scope export to JSON + import with conflict resolution.
- [ ] R1D-6.5: Delete action.
- [ ] **AC:** All 5 scopes populated by a running session; reads and writes
      reflect immediately; delete + re-read shows gone; export + import
      round-trip preserves state.

## R1D-7 — Settings + vault + provider selection (1 week)

- [ ] R1D-7.1: Settings layout with 7 sub-sections (Profile / Providers /
      Credentials / Data / Privacy / Updates / Advanced).
- [ ] R1D-7.2: Providers section — test-connection per provider, default
      selector.
- [ ] R1D-7.3: Credentials vault with OS keychain integration (macOS:
      Keychain Services, Windows: Windows Credential Manager, Linux: Secret
      Service API).
- [ ] R1D-7.4: Data-dir picker + `.stoke/` → `.r1/` migration tool.
- [ ] R1D-7.5: Privacy toggles + diagnostic-bundle export.
- [ ] **AC:** Configure RelayGate provider, save credential, test connection,
      provider becomes selectable in session composer. Migration tool moves
      a `.stoke/` session to `.r1/` without data loss.

## R1D-8 — MCP servers panel (1 week)

- [ ] R1D-8.1: MCP servers panel — list, add, remove.
- [ ] R1D-8.2: Connection test (initialize handshake + tool listing).
- [ ] R1D-8.3: Per-tool test invocation form.
- [ ] R1D-8.4: Session-scoped vs. global server toggle.
- [ ] **AC:** Add an external MCP server (stdio, sse, or http), test
      connection, invoke one tool successfully, disconnect, confirm cleanup.

## R1D-9 — Observability dashboard (1 week)

- [ ] R1D-9.1: KPI cards + chart grid using recharts or visx.
- [ ] R1D-9.2: Per-provider latency histogram, per-skill invocation count,
      error-rate timeline.
- [ ] R1D-9.3: Time-range picker + export CSV.
- [ ] R1D-9.4: Cost-reconciliation check against RelayGate B6 when
      configured (mismatch → yellow banner).
- [ ] **AC:** 7-day chart loads in <1 s on a workspace with 1k sessions; CSV
      export opens in Excel and matches on-screen numbers.

## R1D-10 — Multi-session parallelism + approval queue + scheduling (2 weeks)

- [ ] R1D-10.1: Support N concurrent sessions (target: stable at 5 on 16 GB
      RAM).
- [ ] R1D-10.2: Approval queue UI + system tray badge + native notifications.
- [ ] R1D-10.3: Schedule tab with CRUD on recurring tasks.
- [ ] R1D-10.4: OS-integration: macOS launchd, Windows Task Scheduler,
      Linux systemd user unit (each optional).
- [ ] R1D-10.5: Headless-mode schedule execution (no UI required).
- [ ] **AC:** 5 concurrent sessions run for 30 min without crash; an
      autonomous session hits an approval gate and the badge appears; a
      scheduled task fires on three consecutive days when the app is closed.

## R1D-11 — Polish + signing + notarization + auto-update (2 weeks)

- [ ] R1D-11.1: UI polish pass — loading states, empty states, error states,
      keyboard shortcuts, accessibility audit.
- [ ] R1D-11.2: macOS signing + notarization pipeline in CI.
- [ ] R1D-11.3: Windows EV-signing pipeline in CI.
- [ ] R1D-11.4: Linux GPG + APT/RPM repo.
- [ ] R1D-11.5: Auto-update with signed manifest + rollback.
- [ ] R1D-11.6: First-launch onboarding: data-dir pick, provider selection,
      optional demo session.
- [ ] **AC:** Download the signed artifact, install, first launch onboarding
      completes, app auto-detects an update and applies it; rollback works
      when a bad update is forced.

## R1D-12 — Release + store submissions (1 week)

- [ ] R1D-12.1: Homebrew cask PR (macOS).
- [ ] R1D-12.2: Scoop manifest (Windows).
- [ ] R1D-12.3: Flathub submission (Linux).
- [ ] R1D-12.4: Landing page copy for `r1.dev/desktop`.
- [ ] R1D-12.5: Release notes + changelog.
- [ ] **AC:** `brew install --cask r1` works on macOS; `scoop install r1`
      works on Windows; Flathub listing live.

---

## Effort estimate

**Total: ~13-15 weeks for one senior engineer to MVP**, or **~6-8 weeks with
two engineers** splitting UI (engineer A: R1D-2, R1D-4, R1D-5, R1D-6, R1D-7,
R1D-8) and runtime (engineer B: R1D-1, R1D-3, R1D-9, R1D-10, R1D-11, R1D-12)
tracks.

Critical path: `R1D-1 → R1D-2 → (R1D-3 ‖ R1D-4) → R1D-5 + R1D-6 → R1D-7 +
R1D-8 → R1D-9 → R1D-10 → R1D-11 → R1D-12`.

## Dependencies

- **work-r1-rename.md S1 (dual-accept foundations)** must be committed
  before R1D-7.4 (the `.stoke/` → `.r1/` migration tool).
- **work-r1-actium-studio-skills.md R1S-1..R1S-4** must be committed before
  R1D-4.4 (bundled pack install).
- R1's `cmd/stoke/ctl_cmd.go` IPC surface must be stable — it is the
  ground-truth for R1D-1.4 RPC verbs.

## Skills required

- Rust (Tauri sidecar) — intermediate.
- TypeScript + React — strong.
- Platform signing + distribution (macOS + Windows + Linux) — one team
  member with prior experience.
- OS-integration hooks (launchd, Task Scheduler, systemd, keychains) — prior
  experience or 2 weeks self-ramp.

## Completion criteria (copied from work-r1-desktop-app.md §11)

- [ ] All 12 phases (R1D-1..R1D-12) committed.
- [ ] macOS, Windows, Linux x64 artifacts signed, notarized where required,
      downloadable from `r1.dev/desktop`.
- [ ] Homebrew + Scoop + Flathub listings live.
- [ ] Auto-update verified end-to-end on all three platforms (apply +
      rollback).
- [ ] 5 concurrent sessions stable on a 16 GB MBP for a 30 min stress run.
- [ ] Ledger verify-chain, crypto-shred, and export-import round-trip
      passing on each platform.
- [ ] Docs published at `r1.dev/docs/desktop/`.
- [ ] Zero regression on R1's CLI CI gate (`go build ./cmd/r1 && go test
      ./... && go vet ./...`) — desktop scaffold lives in a subdir and does
      not block Go builds.
- [ ] A fresh operator, starting from a signed download, completes
      onboarding + first session in under 10 minutes.

When every box above is checked, this work order's status changes to
VERIFIED with a commit hash per `/home/eric/repos/CLAUDE.md` §Rules.
