# Feature Map

This is the current feature inventory for r1, organized by what each
capability lets a user **do**. Status sections at the end of each group
classify items as Done / In Progress / Scoped / Scoping / Potential-On
Horizon. The eight cortex / lanes / multi-surface specs live in the Scoped
sections, with a forward-link to the spec file in `specs/`.

## What r1 lets you do

A user-facing readout, before any feature table:

- **Run a coding task end-to-end** — type a free-text task or load a
  `plan.json`; r1 plans, executes, verifies, and commits without a hand on
  the wheel.
- **Watch the agent think in parallel** — a half-dozen specialist Lobes
  (memory recall, plan update, rule check, clarifying questions, memory
  curator, WAL keeper) run concurrently with the main thread, sharing full
  context, surfacing findings as Notes.
- **Steer mid-turn without losing the partial work** — type something while
  the agent is streaming; an LLM-driven Router decides whether to interrupt
  (drop-partial), steer (soft note), queue a separate mission, or just chat.
- **Switch between workspaces from one UI** — a single `r1d` daemon hosts N
  concurrent sessions, each bound to its own working directory. Switch
  projects without spawning processes; sessions persist across reconnects
  and across daemon restarts via journaled events.
- **Use the surface that fits the moment** — Bubble Tea TUI, web chat in
  Cursor 3 "Glass" style, or a Tauri 2 desktop app. Same protocol, same
  state, same lanes.
- **Drive r1 from another agent** — every UI action has a documented
  idempotent schema-validated MCP tool. Your Claude or Codex agent can
  start a session, send messages, kill a runaway lane, publish a Note to
  the Workspace, snapshot the TUI, and read back lane events without any
  human in the loop.
- **Trust the harness** — verification descent refuses "done" without
  evidence; honeypot gates abort end-of-turn on canary leaks; protected-file
  and scope checks block merges; a content-addressed ledger records every
  decision; signed skill packs verify before runtime registration.

## Mission Runtime

The original thesis: one strong implementer per task, verification descent
that doesn't believe self-reports, adversarial review across model families,
content-addressed evidence.

| Feature | Benefit | Status | Reference |
|---|---|---|---|
| Plan/execute/verify/review workflow | Keeps every agent output tied to explicit verification gates | Done | `app/`, `workflow/`, `verify/` |
| Adversarial cross-model reviewer | Reviewer dissent blocks merge; Claude implements → Codex reviews (or vice versa) | Done | `critic/`, `convergence/`, `model.CrossModelReviewer()` |
| Verification descent ladder | Anti-deception contract + forced self-check + ghost-write detector + per-file repair cap (3) | Done | `verify/`, `taskstate/`, hooks |
| Honeypot pre-end-turn gate | Canary, markdown-image exfil, chat-template-token leak, destructive-without-consent | Done | `agentloop/`, `hooks/` |
| Speculative parallel execution | 4 strategies in parallel, pick the winner; gated by `--specexec` | Done | `specexec/` |
| GRPW priority + file-scope conflict | Tasks with most downstream work dispatch first; conflicts respected | Done | `scheduler/` |
| Failure fingerprint dedup + retry intelligence | 10 failure classes; same-error-twice escalation; clean-worktree per retry | Done | `failure/`, `errtaxonomy/` |
| Soft-pass AC after 2× `ac_bug` verdicts | When reviewers keep blaming the AC, escalate rather than spin | Done | `convergence/` |

## Governance & Evidence

| Feature | Benefit | Status | Reference |
|---|---|---|---|
| Content-addressed ledger | Append-only Merkle-chained graph; 16 node-type prefixes; no updates, no deletes | Done | `ledger/`, `ledger/nodes/`, `ledger/loops/` |
| Durable event bus | WAL-backed pub/sub with hooks, delayed events, parent-hash causality | Done | `bus/` |
| STOKE envelope v1.0 | Wire-format common across CLI/TUI/web/MCP — `stoke_version`, `instance_id`, `trace_parent`, optional `ledger_node_id` | Done | `docs/stoke-protocol.md` |
| Supervisor rules engine | 30 deterministic rules across 10 categories (consensus, drift, hierarchy, research, skill, snapshot, SDM, cross-team, trust, lifecycle); 3 per-tier manifests | Done | `supervisor/`, `supervisor/rules/*` |
| Consensus loop tracker | 7-state machine (PRD → SOW → ticket → PR → landed) | Done | `ledger/loops/` |
| Snapshot protection | Pre-merge baseline manifest; restore-on-failure | Done | `snapshot/` |
| Bridge adapters (v1 → v2) | Cost / verify / wisdom / audit emit bus events + write ledger nodes | Done | `bridge/` |
| Ledger redaction with two-level Merkle | Content tier wipes preserve chain integrity forever | Scoped | `specs/ledger-redaction.md` |

## Cortex — Parallel Cognition (Scoped)

The new capability the v1 cortex / lanes / multi-surface scope adds. The
substrate (spec 1) and six v1 Lobes (spec 2) are separated so the empty
stage compiles and ships before any specific Lobe wires up.

| Feature | Benefit | Status | Spec |
|---|---|---|---|
| `internal/cortex/` package — Workspace + Lobe + Round + Spotlight + Router | The shared GWT-style stage on which Lobes publish Notes | Scoped | `specs/cortex-core.md` |
| Drop-partial interrupt protocol | Interrupt cancels per-turn ctx, drains SSE, never persists partial assistant message | Scoped | `specs/cortex-core.md` §"Drop-partial interrupt protocol" |
| 30s ping-based idle watchdog | Auto-cancel on connection stalls during streaming | Scoped | `specs/cortex-core.md` |
| Cache pre-warm pump | `max_tokens=1` warming request on Start + every 4 minutes (5-min TTL minus margin) | Scoped | `specs/cortex-core.md` §"Cache pre-warm pump" |
| LobeSemaphore + per-turn token budget | 5 concurrent LLM Lobes default, hard cap 8, 30%-of-main-output cap | Scoped | `specs/cortex-core.md` §"Budget controller" |
| Workspace.Replay from durable WAL | Daemon restart preserves Notes; idempotent | Scoped | `specs/cortex-core.md` §"Workspace persistence" |
| Router (Haiku 4.5) — 4 tools | `interrupt`, `steer`, `queue_mission`, `just_chat` decide mid-turn user-input handling | Scoped | `specs/cortex-core.md` §"The Router" |
| MidturnCheckFn + PreEndTurnCheckFn composition | Cortex hooks compose with operator hooks; critical-Note short-circuit | Scoped | `specs/cortex-core.md` §"Integration points" |
| MemoryRecallLobe (deterministic) | TF-IDF over memory + wisdom corpora; surfaces top-3 relevant entries per round | Scoped | `specs/cortex-concerns.md` §1 |
| WALKeeperLobe (deterministic) | Drains every hub event to durable WAL with backpressure shedding | Scoped | `specs/cortex-concerns.md` §2 |
| RuleCheckLobe (deterministic) | Converts supervisor-rule fires into Notes; trust/dissent → critical | Scoped | `specs/cortex-concerns.md` §3 |
| PlanUpdateLobe (Haiku 4.5) | Auto-applies edits; queues adds and removes for user confirm | Scoped | `specs/cortex-concerns.md` §4 |
| ClarifyingQLobe (Haiku 4.5) | Up to 3 clarifying questions per turn; surfaces at idle | Scoped | `specs/cortex-concerns.md` §5 |
| MemoryCuratorLobe (Haiku 4.5) | Auto-writes only `fact` category; queues other categories; honors `private` tag; `~/.r1/cortex/curator-audit.jsonl` | Scoped | `specs/cortex-concerns.md` §6 |
| Per-Lobe enable + escalation flags in `~/.r1/config.yaml` | Operator opt-out per Lobe; Sonnet escalation gated | Scoped | `specs/cortex-concerns.md` §"Privacy & Opt-Out" |

## Lanes — Cross-Surface Wire Format (Scoped)

| Feature | Benefit | Status | Spec |
|---|---|---|---|
| Six event types (`lane.created`/`status`/`delta`/`cost`/`note`/`killed`) | Exhaustive wire surface — adding a seventh is a version bump | Scoped | `specs/lanes-protocol.md` §4 |
| Lane state machine (`pending → running → blocked → done\|errored\|cancelled`) | Cross-surface vocabulary; illegal transitions rejected `-32099` | Scoped | `specs/lanes-protocol.md` §3 |
| `pinned` orthogonal flag | Surfaces render pinned lanes above unpinned; persists across reconnect | Scoped | `specs/lanes-protocol.md` §3.2 |
| JSON-RPC 2.0 over WS / NDJSON over stdout / HTTP+SSE fallback | One protocol; three transports | Scoped | `specs/lanes-protocol.md` §5 |
| Per-session monotonic `seq` | Replay via `Last-Event-ID` (SSE) or `since_seq` (JSON-RPC) | Scoped | `specs/lanes-protocol.md` §6 |
| WS subprotocol `r1.lanes.v1` | Version handshake; CSWSH defense | Scoped | `specs/lanes-protocol.md` §5.4 |
| Five MCP tools (`r1.lanes.list / subscribe / get / kill / pin`) | Every lane action is agent-driveable | Scoped | `specs/lanes-protocol.md` §7 |
| Backwards-compat with `desktop/IPC-CONTRACT.md` | Existing 11 verbs untouched; `session.delta` co-emits with `lane.delta` for one minor release | Scoped | `specs/lanes-protocol.md` §9 |

## Surfaces — TUI, Web, Desktop (Scoped)

### TUI (`internal/tui/lanes/` — Bubble Tea v2)

| Feature | Benefit | Status | Spec |
|---|---|---|---|
| Adaptive lane columns vs vertical stack | Wide terminal → up to 4 columns (`width / 32`); narrow → vertical | Scoped | `specs/tui-lanes.md` §"Layout Algorithm" |
| Focus mode (65/35 main+peers) | `enter` zooms a lane; peers stack on the right | Scoped | `specs/tui-lanes.md` §"Layout" |
| Per-lane render-string cache + diff-only repaint | 200 Hz upstream → ≤5 Hz model receives | Scoped | `specs/tui-lanes.md` §"Render-Cache Contract" |
| Single fan-in `chan laneTickMsg` + `waitForLaneTick` | Canonical realtime example; producer goroutine coalesces 200-300 ms | Scoped | `specs/tui-lanes.md` §"waitForLaneTick" |
| Status vocabulary (D-S1 glyphs + AdaptiveColor) | `pending(·)/running(▸)/blocked(⏸)/done(✓)/errored(✗)/cancelled(⊘)` | Scoped | `specs/tui-lanes.md` §"Styles" |
| Keybindings — jump, cycle, focus, kill, kill-all, help | `1`–`9`, `tab`, `enter`, `esc`, `x`, `K`, `?` | Scoped | `specs/tui-lanes.md` §"Keybinding Map" |
| Local IPC + WS transports | Embedded mode dials `~/.r1/r1d.sock`; remote dials WS | Scoped | `specs/tui-lanes.md` §"Subscription Wiring" |
| `NO_COLOR` / `TERM=dumb` graceful | Glyph alone disambiguates status | Scoped | `specs/tui-lanes.md` §"Testing" |

### Web (`web/` — React 18 + Vite 6 + Tailwind 3 + shadcn/ui)

| Feature | Benefit | Status | Spec |
|---|---|---|---|
| Three-column Cursor 3 "Glass" layout | Session list left, chat center, lanes sidebar right | Scoped | `specs/web-chat-ui.md` §"Component Catalog" |
| Multi-instance daemon switcher | `Cmd+1..9` switch; one zustand store per daemon | Scoped | `specs/web-chat-ui.md` §"Routing" |
| Streaming markdown via `vercel/streamdown` | Graceful partial-Markdown; Shiki, KaTeX, Mermaid, rehype-harden | Scoped | `specs/web-chat-ui.md` §"Stack" |
| `@ai-sdk/elements` cards | Tool, Reasoning, Plan, CodeBlock cards default-collapse on `output-available` | Scoped | `specs/web-chat-ui.md` |
| AI SDK 6 `useChat` hook | Maps directly to lane envelope; transport custom-wired to WS | Scoped | `specs/web-chat-ui.md` |
| Tile mode (1×2 / 1×3 / 2×2) | Pin 2-4 lanes into the center pane for parallel watching | Scoped | `specs/web-chat-ui.md` §"TileGrid" |
| WS subprotocol-token auth + Last-Event-ID replay | Reconnect with backoff + jitter; 4401 auto-mints fresh ticket | Scoped | `specs/web-chat-ui.md` §"WebSocket Reconnect" |
| `ResilientSocket` with state machine | `idle→connecting→open→reconnecting→closed`; 30s ping/30s pong watchdog | Scoped | `specs/web-chat-ui.md` |
| Workdir picker (FSA + IndexedDB persistence + manual fallback) | Browsers without FSA fall back to typed path with allowed-roots autocomplete | Scoped | `specs/web-chat-ui.md` §"WorkdirPickerDialog" |
| CSP locked to loopback | `connect-src 'self' ws://127.0.0.1:* http://127.0.0.1:*` | Scoped | `specs/web-chat-ui.md` §"Build Pipeline" |
| `data-testid` lint + axe-core e2e | Every interactive element accessible to agents and screen readers | Scoped | `specs/web-chat-ui.md` §"Accessibility" |

### Desktop (`desktop/` — Tauri 2 augmentation)

| Feature | Benefit | Status | Spec |
|---|---|---|---|
| Daemon discovery + sidecar fallback | Probes `~/.r1/daemon.json`; spawns bundled `r1` via `ShellExt::sidecar` on failure | Scoped | `specs/desktop-cortex-augmentation.md` §5 |
| `<DaemonStatus>` banner | Green=external, blue=sidecar, yellow=reconnecting, red=offline | Scoped | `specs/desktop-cortex-augmentation.md` §5 |
| Per-session workdir via `tauri-plugin-store` | Persists across restart; the Go side binds it to `cmd.Dir` | Scoped | `specs/desktop-cortex-augmentation.md` §7 |
| `tauri::ipc::Channel<LaneEvent>` per session | Multiplexes all lanes; 10 Hz, ring-buffer drops `lane.delta` on overflow | Scoped | `specs/desktop-cortex-augmentation.md` §8 |
| Pop-out lane via `Cmd+\` | Opens a `WebviewWindow` rendering only that lane; survives primary close | Scoped | `specs/desktop-cortex-augmentation.md` §9 |
| Native menu bar (File/Edit/View/Session/Tools/Window/Help) | Cmd+N / Cmd+O / Cmd+P / Cmd+1 / Cmd+2 / Cmd+\ | Scoped | `specs/desktop-cortex-augmentation.md` §9 |
| `tauri-plugin-autostart` (login items / registry / .desktop) | Settings → "Start at login" toggle | Scoped | `specs/desktop-cortex-augmentation.md` §10 |
| `r1 serve --install` (kardianos/service) | Daemon auto-start independent of UI auto-start | Scoped | `specs/desktop-cortex-augmentation.md` §10 |
| Shared `packages/web-components/` workspace package | `LaneCard`, `LaneSidebar`, `LaneDetail`, `PoppedLaneApp` consumed by both web and desktop | Scoped | `specs/desktop-cortex-augmentation.md` §4 |

## r1d Daemon (Scoped — spec 5)

| Feature | Benefit | Status | Spec |
|---|---|---|---|
| Per-user singleton, on-demand | Watchman pattern; `r1 chat` forks `r1 serve` if the IPC endpoint is missing | Scoped | `specs/r1d-server.md` §1 |
| N concurrent sessions as goroutines | Each session carries `SessionRoot string` threaded via `cmd.Dir` | Scoped | `specs/r1d-server.md` §8 |
| Single-instance enforcement | `gofrs/flock` on `~/.r1/daemon.lock` plus socket exclusivity | Scoped | `specs/r1d-server.md` §11 |
| Discovery file `~/.r1/daemon.json` | Mode 0600; pid + sock + port + token; rotate-on-start | Scoped | `specs/r1d-server.md` §7 |
| Unix socket / Windows named pipe | Peer-cred check (no token needed); 0600 socket; 0700 parent dir | Scoped | `specs/r1d-server.md` §7.3 |
| Loopback HTTP+WS listener | Random ephemeral port; Origin pin + Host pin + WS subprotocol token | Scoped | `specs/r1d-server.md` §7 |
| 256-bit Bearer token | `Authorization: Bearer` (HTTP) or `Sec-WebSocket-Protocol: r1.bearer, <t>` (WS) | Scoped | `specs/r1d-server.md` §7.1 |
| Per-session `journal.ndjson` | Append-only; fsync on terminal events; replay on daemon restart | Scoped | `specs/r1d-server.md` §9 |
| Hot-upgrade — restart-required, transparent | Replay each session's journal; emit `daemon.reloaded` to reconnecting clients | Scoped | `specs/r1d-server.md` §11 |
| `r1 serve --install` (`kardianos/service`) | launchd / systemd-user / Windows SCM unit | Scoped | `specs/r1d-server.md` §12 |
| `os.Chdir` audit + CI lint | Hard gate before multi-session is enabled; per-session sentinel panics on mismatch | Scoped | `specs/r1d-server.md` §10 |
| Backwards-compat aliases | `r1 daemon ...` and `r1 agent-serve ...` keep working | Scoped | `specs/r1d-server.md` §14 |

## Agentic Test Harness — Every UI Action Is a Tool (Scoped — spec 8)

| Feature | Benefit | Status | Spec |
|---|---|---|---|
| `r1.session.*` tools (`start / send / cancel / list / get / resume`) | Whole-session control reachable to external agents | Scoped | `specs/agentic-test-harness.md` §4.1 |
| `r1.lanes.*` tools (`list / subscribe / get / kill / pin`) | Lane lifecycle and streaming | Scoped | `specs/agentic-test-harness.md` §4.2 |
| `r1.cortex.*` tools (`notes / publish / lobes_list / lobe_pause / lobe_resume`) | Read the Workspace; publish Notes; pause Lobes | Scoped | `specs/agentic-test-harness.md` §4.3 |
| `r1.mission.*`, `r1.worktree.*`, `r1.bus.tail`, `r1.verify.*` | Whole-runtime agent surface | Scoped | `specs/agentic-test-harness.md` §4.4-4.6 |
| `r1.tui.*` tools (`press_key / snapshot / get_model`) via `teatest_shim.go` | TUI testable without a real terminal emulator | Scoped | `specs/agentic-test-harness.md` §4.7 |
| Web tested via Playwright MCP | DOM/a11y-snapshot 12-17pp more reliable than vision-driven Computer Use | Scoped | `specs/agentic-test-harness.md` §3 |
| Storybook MCP for component contracts | Every component story has role + accessible-name + state metadata | Scoped | `specs/agentic-test-harness.md` §3 |
| Gherkin-flavored markdown (`*.agent.feature.md`) | Agent-readable scenarios; runner dispatches each step through MCP | Scoped | `specs/agentic-test-harness.md` §3 |
| `tools/lint-view-without-api/` CI lint | Fails the build when an interactive component lacks an MCP counterpart | Scoped | `specs/agentic-test-harness.md` §5 |
| `docs/AGENTIC-API.md` | Contract for external agents | Scoped | `specs/agentic-test-harness.md` §3 |

## Deterministic Skills

| Feature | Benefit | Status | Reference |
|---|---|---|---|
| Skill manufacture (4-workflow pipeline + confidence ladder) | Reusable workflows become governed artifacts | Done | `internal/skillmfr/` |
| Registry and selection | Runtime behavior maps to explicit skill assets | Done | `skill/`, `skillselect/` |
| Pack lifecycle (`init`, `info`, `install`, `list`, `publish`, `search`, `update`) | Pack inspection, activation, discovery, and refresh operational | Done | `cmd/r1/skills_pack_cmd.go` |
| `sign` and `verify` | Integrity controls for pack distribution | Done | `cmd/r1/skills_pack_cmd.go` |
| HTTP registry — `r1 skills pack serve` | Stable read-only endpoints (`/healthz`, `/v1/packs`, archives) | Done | `cmd/r1/skills_pack_server.go` |
| Runtime signed-pack verification | Refuses registration when signature is missing or invalid | Done | April 30 main-branch commit |

## Runtime Helper Surfaces

| Feature | Benefit | Status | Reference |
|---|---|---|---|
| Ledger audit runtime | Lets deterministic flows query ledger-backed audit evidence | Done | `cmd/stoke-mcp/backends.go` |
| Skill execution audit runtime | Runtime execution behavior inspectable | Done | `cmd/stoke-mcp/backends.go` |
| Metrics collection runtime | Runtime metrics snapshots exposed to deterministic flows | Done | `cmd/stoke-mcp/metrics_runtime.go` |
| Timeout / cancellation hooks | Bounded, cancellation-aware deterministic runtime calls | Done | `cmd/stoke-mcp/backends.go` |
| Oneshot runtime cost metadata | Runtime cost visible to callers and operators | Done | April 30 main-branch commit |

## Code Analysis & Generation

| Feature | Benefit | Status | Reference |
|---|---|---|---|
| Go AST analysis + extraction | Function/class indexing and structured-content parsing | Done | `goast/`, `extract/` |
| Repomap with PageRank | Token-budgeted "what's important" injection into prompts | Done | `repomap/` |
| Symbol index (`symindex/`) | Fast function/class lookup | Done | `symindex/` |
| Dependency graph (`depgraph/`) | Import-graph-aware test selection | Done | `depgraph/`, `testselect/` |
| TF-IDF semantic search | BM25 retrieval over codebase | Done | `tfidf/` |
| Vector / embedding search (`vecindex/`) | sqlite-vec backed similarity | Done | `vecindex/` |
| Cascading `str_replace` algorithm | Exact → whitespace → ellipsis → fuzzy match | Done | `tools/` |
| Patch apply (`patchapply/`) | Unified-diff parsing with fuzzy match | Done | `patchapply/` |
| Auto-fix loop (`autofix/`) | Iterative lint-and-fix until quiet | Done | `autofix/` |
| Conflict resolution (`conflictres/`) | Semantic merge-conflict resolution | Done | `conflictres/` |

## File / Workspace / Worktree

| Feature | Benefit | Status | Reference |
|---|---|---|---|
| Atomic multi-file edits | Transactional semantics across many files | Done | `atomicfs/` |
| Git worktree per task | `git merge-tree --write-tree` validation; `mergeMu` serializes merges | Done | `worktree/` |
| BaseCommit captured at worktree creation | `diff BaseCommit..HEAD` for retry summaries | Done | `worktree/` |
| Conversation branching | Multiple solution paths in parallel | Done | `branch/` |
| Hash-anchored line verification | Detects concurrent edits | Done | `hashline/` |

## LLM Integration

| Feature | Benefit | Status | Reference |
|---|---|---|---|
| 5-provider model resolver | Claude → Codex → OpenRouter → direct API → lint-only fallback chain | Done | `model/`, `provider/` |
| Subscription pool with circuit breaker | Per-pool OAuth poller; `closed → open → half-open` with cooldown | Done | `subscriptions/`, `pools/` |
| MCP server connectivity | GitHub, Linear, Slack, Postgres, custom; stdio / http / sse / streamable-http | Done | `mcp/` |
| Cache-aligned prompt construction | Stable cache breakpoints across main + Lobe + warming requests | Done | `promptcache/`, `agentloop.BuildCachedSystemPrompt` |
| Adaptive context bin-packing | Three-tier budget; progressive compaction; reminders | Done | `context/`, `ctxpack/`, `microcompact/` |
| Real-time cost tracking + budget alerts | `CostTracker.OverBudget()` checked before each execute attempt | Done | `costtrack/` |

## Permissions & Security

| Feature | Benefit | Status | Reference |
|---|---|---|---|
| 11-layer policy engine | `--tools`, MCP isolation, `--disallowedTools`, `--allowedTools`, settings.json, worktree isolation, sandbox, `--max-turns`, enforcer hooks, verify pipeline, git ownership | Done | `policy/`, `hooks/` |
| Mode-1 auth isolation | API-key env vars stripped; `apiKeyHelper: null`; per-pool `CLAUDE_CONFIG_DIR` | Done | `config/`, `subscriptions/` |
| MCP triple isolation (plan + verify) | `--strict-mcp-config` + empty config + `--disallowedTools mcp__*` | Done | `mcp/`, `policy/` |
| Sandbox `failIfUnavailable: true` | Fail-closed | Done | `config/` |
| Process group isolation | `Setpgid: true` + `killProcessGroup` (SIGTERM → SIGKILL) | Done | `engine/` |
| Prompt-injection hardening (`promptguard`) | 4 ingest paths scanned: skills, failure analysis, feasibility gate, convergence judge | Done | `promptguard/` |
| Tool-output sanitization | 200KB cap, head+tail truncation, chat-template-token scrub with ZWSP, `[STOKE NOTE: untrusted DATA]` prefix | Done | `agentloop.executeTools` |
| 58-sample red-team corpus | OWASP LLM01, CL4R1T4S, Rehberger SpAIware, Willison; 60% per-category detection floor | Done | `redteam/` |
| Honeypot pre-end-turn gate | Canary, markdown-image exfil, chat-template-token leak, destructive-without-consent | Done | `agentloop/` |
| 18 deterministic security rules | Secrets, eval, injection, exec — no LLM calls | Done | `scan/` |

## Knowledge & Learning

| Feature | Benefit | Status | Reference |
|---|---|---|---|
| Persistent memory (`memory/`) | SQLite + FTS5 + sqlite-vec; episodic/semantic/procedural | Done | `memory/` |
| Wisdom store (`wisdom/`) | Cross-task gotchas, decisions, `FindByPattern` | Done | `wisdom/` |
| Persistent indexed research | FTS5-backed research storage | Done | `research/` |
| Flow tracking | Intent inferred from action sequences | Done | `flowtrack/` |
| Replay (session recording) | Post-mortem debugging | Done | `replay/` |

## Config, Session, Infrastructure

| Feature | Benefit | Status | Reference |
|---|---|---|---|
| YAML policy parser + auto-detect | `stoke.policy.yaml`; `verificationExplicit` distinguishes "all false" from omitted | Done | `config/` |
| Session store interface | JSON + SQLite (WAL); attempts, state, learned patterns | Done | `session/` |
| Three-tier message dispatch queue | Critical / observability / low-priority lanes | Done | `dispatch/` |
| Structured leveled logging | Task / Attempt / Cost helpers | Done | `logging/` |
| Thread-safe metrics + telemetry | Per-package counters; performance metrics | Done | `metrics/`, `telemetry/` |
| NDJSON 6-event-type streamjson | Drain-on-EOF; 3-tier timeouts | Done | `streamjson/` |

## UI & Interfaces (today, before spec 4 / 6 / 7)

| Feature | Benefit | Status | Reference |
|---|---|---|---|
| Headless runner + Bubble Tea TUI (Dashboard / Focus / Detail) | Today's interactive surface (pre-lanes panel) | Done | `tui/`, `tui/interactive.go`, `tui/runner.go` |
| Mission API HTTP endpoints | Programmatic access | Done | `server/` |
| Per-machine dashboard `r1-server` (port 3948) | Discovers running r1 instances; live event stream; 3D ledger visualizer | Done | `cmd/r1-server/` |
| Interactive REPL | `internal/repl/` | Done | `repl/` |
| 17-persona audit | Multi-perspective review (security, performance, a11y, DX, …) | Done | `audit/` |

---

## Status Summary

### Done
- Mission runtime (plan/execute/verify/review) with cross-model reviewer.
- Verification descent ladder + honeypot gate + protected-file/scope checks.
- Content-addressed ledger, durable WAL bus, supervisor rules engine.
- Deterministic skill substrate + signed pack distribution + HTTP registry.
- 5-provider model resolver, subscription pool with circuit breaker.
- Wave 2 R1-parity: browser tools, Manus operator, multi-language LSP, VS
  Code + JetBrains plugins, multi-CI parity, Tauri R1D-1..R1D-12 phases.
- Prompt-injection hardening, red-team corpus, MCP server connectivity.
- 132 internal Go packages; race-clean across the whole repo.

### In Progress
- Hardening of Manus-style autonomous operator (per-mission toggle).
- LSP feature coverage beyond hover/definition/diagnostics.
- Headless desktop GUI for CI screenshot tests.
- Race-clean regression sweep across `internal/`.

### Scoped
- **cortex-core** (`specs/cortex-core.md`) — Workspace, Lobe, Round,
  Spotlight, Router, drop-partial, pre-warm, budget controller, persistence.
- **cortex-concerns** (`specs/cortex-concerns.md`) — six v1 Lobes
  (memory-recall, WAL-keeper, rule-check, plan-update, clarifying-Q,
  memory-curator) plus per-Lobe enable + escalation flags.
- **lanes-protocol** (`specs/lanes-protocol.md`) — six event types,
  JSON-RPC 2.0 envelope, NDJSON / WS / SSE framings, replay semantics, five
  MCP tools.
- **tui-lanes** (`specs/tui-lanes.md`) — Bubble Tea v2 lanes panel with
  adaptive layout, focus mode, render cache, transports.
- **r1d-server** (`specs/r1d-server.md`) — `r1 serve` per-user singleton
  daemon; multi-session goroutines; `os.Chdir` audit + lint; journal replay.
- **web-chat-ui** (`specs/web-chat-ui.md`) — React 18 + Vite 6 + Tailwind 3;
  Cursor 3 "Glass"; tile mode; ResilientSocket; FSA workdir picker.
- **desktop-cortex-augmentation** (`specs/desktop-cortex-augmentation.md`) —
  Tauri 2 augmentation; daemon discovery + sidecar fallback;
  per-session workdir; `Channel<LaneEvent>`; pop-out windows; native menu;
  auto-start.
- **agentic-test-harness** (`specs/agentic-test-harness.md`) — consolidated
  `r1_server.go` MCP catalog; teatest_shim; Playwright MCP; Storybook MCP;
  view-without-api lint; Gherkin DSL.
- IDE plugin marketplace publishing (VS Code Marketplace, JetBrains
  Marketplace) — code in-tree, publishing pipeline pending.
- Ledger redaction with two-level Merkle commitment
  (`specs/ledger-redaction.md`).

### Scoping
- Cross-machine session migration (current daemon is one-host).
- Per-tool throttling policy in `.stoke/`.
- Encryption-at-rest for journals (`specs/encryption-at-rest.md`).
- Broader outward-facing superiority reporting against peer runtimes.

### Potential — On Horizon
- BitBucket Pipelines adapter parity with GitLab CI / GitHub Actions.
- Native MCP server bundle for popular IDEs without a separate install.
- Browser tool sandboxed under a remote browser (vs current local browser).
- Cross-product deterministic skill exchange + marketplace dynamics.
- Cloud daemon support beyond loopback / per-host singleton.
- Multi-tenant per-host (multiple uids on a shared box).
- Tracing / OpenTelemetry export of lane events.
