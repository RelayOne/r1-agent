# Decisions Log

## 2026-05-02 — Cortex / Lanes / Multi-Surface Scope

### D-2026-05-02-01 — Spec split = 8 specs
Foundation (cortex-core → cortex-concerns + lanes-protocol → tui-lanes + r1d-server → web-chat-ui + desktop-cortex-augmentation → agentic-test-harness). Each independently buildable with declared deps.
**Build order:** 1 → (2,3) → (4,5) → (6,7) → 8.

### D-2026-05-02-02 — Web UI stack = React + Vite + Tailwind
Matches `desktop/` toolchain. Components reusable across web and Tauri.

### D-2026-05-02-03 — r1d transport = WebSocket + HTTP/JSON + SSE fallback
WS as primary streaming. HTTP for control/CRUD. SSE fallback when WS blocked.

### D-2026-05-02-04 — Merge model = agent-decides
On user input mid-turn, a Router LLM (Haiku 4.5) with 4 tools (interrupt / steer / queue_mission / just_chat) decides handling. Plus baseline soft-note injection at turn boundaries via existing `MidturnCheckFn`. Critical-tagged Notes refuse `end_turn` via `PreEndTurnCheckFn`.

### D-2026-05-02-05 — All 6 concerns ship in v1
Memory-recall + WAL-keeper (deterministic), Rule-check (deterministic), Plan-update (Haiku LLM), Clarifying-Q drafter + Memory-curator (Haiku LLM).

### D-2026-05-02-06 — Budget = smart-not-cheap
Tunable knobs. Defaults: 5 concurrent LLM Lobes, Haiku floor with Sonnet escalation on rule-check failure or operator-tagged critical. CostTracker enforces a per-turn 30%-of-main budget for Lobes. Correctness wins ties.

### Recommended cortex/lanes/daemon/agentic decisions accepted (research-driven defaults)

**Cortex foundation:**
- D-C1: Cortex package = `internal/cortex/` exposing `Workspace`, `Lobe`, `Note`, `Round`, `Spotlight` (RT-PARALLEL-COGNITION; GWT vocabulary).
- D-C2: Lobe naming chosen — avoids collision with existing `internal/concern/` (stance-context projection).
- D-C3: Workspace persistence = write-through to bus/ WAL so daemon restart preserves Notes.
- D-C4: Drop-partial interrupt pattern — never persist partial assistant message; drain SSE on cancel; 30s ping watchdog (RT-CANCEL-INTERRUPT).
- D-C5: Pre-warm cache before fan-out — `max_tokens=0` warming request on Loop start, refresh every 4 min (RT-CONCURRENT-CLAUDE-API).
- D-C6: Tier 4 budget — 5 LLM Lobes + unlimited deterministic. Cap=8 hard ceiling.
- D-C7: Lobe model floor = Haiku 4.5 with 1-hour cache extension; Sonnet escalation allowed on tagged-critical paths.

**Lanes & surfaces:**
- D-S1: Status vocabulary shared cross-surface — pending(·)/running(▸)/blocked(⏸)/done(✓)/errored(✗)/cancelled(⊘). Always paired with color.
- D-S2: Render coalescing = 5–10 Hz. Per-lane render-string cache. Diff-only repaint (RT-TUI-LANES anti-pattern avoidance).
- D-S3: TUI = Bubble Tea v2 + bubblelayout + Bubbles v2 + lipgloss v2 AdaptiveColor.
- D-S4: Web UI pattern = Cursor 3 "Glass" — right-sidebar agents window, "Agent Tabs" tile mode in main pane.
- D-S5: Web markdown = `vercel/streamdown`; AI cards = `@ai-sdk/elements`; chat hook = AI SDK 6 `useChat`.
- D-S6: WS auth = token via `Sec-WebSocket-Protocol` subprotocol, plus strict Origin/Host allowlist.
- D-S7: Desktop = augment existing R1D phases. tauri-plugin-store for per-session workdir; tauri-plugin-websocket for daemon connection; one `tauri::ipc::Channel<LaneEvent>` per session at 10 Hz.

**r1d daemon:**
- D-D1: Daemon model = per-user singleton on-demand (Watchman pattern). `r1 serve --install` opt-in for always-on.
- D-D2: One process, N sessions as goroutines. Each session carries `SessionRoot` threaded via `cmd.Dir`.
- D-D3: IPC = unix socket / Windows named pipe for CLI; loopback HTTP+WS for browsers. JSON-RPC 2.0 envelope, monotonic seq, replay-on-reconnect.
- D-D4: os.Chdir audit + CI lint required before turning on multi-session (RT-R1D-DAEMON central risk).
- D-D5: Single-instance via `gofrs/flock` on `~/.r1/daemon.lock` plus socket exclusivity.

**Agentic:**
- D-A1: MCP-primary, single wire protocol for all programmatic surfaces.
- D-A2: Goal-shaped tools (`r1.lanes.kill`), not RPC-shaped.
- D-A3: Web testing via Playwright MCP. TUI via teatest+JSON-RPC shim. Component contracts via Storybook MCP.
- D-A4: Test DSL = Gherkin-flavored markdown (`*.agent.feature.md`).
- D-A5: Governing principle: every UI action has an idempotent schema-validated MCP equivalent. Enforce via CI lint.

---

## 2026-04-20 — Stoke Full Agent Scoping (historical)

### D-2026-04-20-01 — `r1 run` command shape: both free-text AND SOW
**Context:** CloudSwarm subprocesses `stoke run --output stream-json [flags] TASK_SPEC` but this subcommand doesn't exist in Stoke today (RT-CLOUDSWARM-MAP §8). Existing `stoke ship --sow path.md` is SOW-only.
**Decision:** `stoke run` supports **both** modes:
- `stoke run "free text task"` → routes to chat-intent classifier → dispatches to appropriate executor
- `stoke run --sow path.md` → routes to existing SOW executor
**Owners:** spec-2 (CloudSwarm Protocol), spec-3 (Executor Foundation — task router)
**Implications:** `cmd/stoke/run_cmd.go` is new. Internally calls into existing `sow_native.go` for SOW path and chat intent classifier for free-text.

### D-2026-04-20-02 — `STOKE_DESCENT` stays opt-in through Q2
**Context:** H-91 verification descent engine just shipped (commit 8611d48); still stabilizing. Spec-1 adds anti-deception + per-file cap + bootstrap-per-cycle hardening.
**Decision:** `STOKE_DESCENT=1` remains opt-in. Re-evaluate after ladder is regression-free for 2 weeks post-hardening.
**Owners:** spec-1 (Descent Hardening).
**Flip condition:** 14 consecutive days of ladder runs with 0 regressions vs baseline + stakeholder sign-off.

### D-2026-04-20-03 — Deploy: Fly.io only in spec-6; Vercel + Cloudflare deferred
**Context:** RT-10 evaluated all three. Fly.io has widest language support, explicit rollback, NDJSON match with Stoke's engine/stream model.
**Decision:** Spec-6 ships Fly.io only. Follow-on specs add Vercel (common for Next.js) and Cloudflare (Pages folding into Workers — in flux).
**Owners:** spec-6 (Deploy Executor).

### D-2026-04-20-04 — Memory backend: full stack (SQLite+FTS5+sqlite-vec+embeddings) in v1
**Context:** RT-08 proposed two paths. Operator chose the full stack.
**Decision:** Spec-7 ships:
- SQLite + FTS5 (BM25) for text search
- sqlite-vec for embedding similarity
- Three-way embedding fallback: remote API (OpenAI 3-small with Matryoshka → 512d) → local llama.cpp daemon (nomic-embed-text-v2) → BM25-only
- Scope hierarchy (global/repo/task) + auto-retrieval at planner/worker/delegation injection points
**Owners:** spec-7 (Operator UX + Memory).
**Implications:** Spec-7 is larger than originally scoped (~5-7 days of work vs 2-3). Embedding fallback = 3 implementations. Splitting off "Memory Enhancement" into its own spec-7a is a fallback if scope bloats.

## Recommended decisions accepted as-is (not opinionated by operator)

- **D-C1**: Event emitter extends `internal/streamjson/` (do NOT create `internal/events/`) — evidence RT-STOKE-SURFACE §14.
- **D-C2**: Event log = SQLite+WAL single `events` table — evidence RT-05.
- **D-C3**: Bus has zero publishers today; all specs that emit events publish to bus AND streamjson.
- **D-C4**: Anti-deception contract injected for ALL workers, no opt-out — evidence RT-06 MASK 8-12% lift.
- **D-C5**: Forced self-check uses `agentloop.Config.PreEndTurnCheckFn` (not supervisor rule) — evidence RT-STOKE-SURFACE §2 §13.
- **D-2**: Per-file repair cap = 3 (Cursor parity) — evidence RT-11 verbatim.
- **D-7**: Only `hitl_required` is protocol-critical for CloudSwarm; other events are dashboard freeform — evidence RT-CLOUDSWARM-MAP §2-3.
- **D-9**: Policy hook DEFERRED (CloudSwarm doesn't gate Stoke with Cedar; skill-level only) — evidence RT-CLOUDSWARM-MAP §4.
- **D-12**: Existing SOW flow wrapped as `CodeExecutor`, not rewritten.
- **D-13**: `AcceptanceCriterion.VerifyFunc` added backward-compatibly (Command wins if both set).
- **D-15**: Browser = `github.com/go-rod/rod` pinned to a 2026 main SHA.
- **D-17**: Research = Opus 4.7 lead + Sonnet 4.5 subagents; parallelism cap 5 default.
- **D-19**: A2A = `github.com/a2aproject/a2a-go` v2.2.0 (official SDK).
- **D-20**: Delegation extends existing `internal/delegation/scoping.go`; HMAC real-verifier from day 1.
- **D-22**: TrustPlane payment follows `a2a-x402` extension URI pattern.
- **D-27**: `stoke plan` is a separate command producing `plan.json`; `stoke ship` remains the combined path.
- **D-28**: `Operator` interface with terminal + NDJSON impls (latter uses `hitl_required` for Ask).
- **D-29**: Intent Gate = verb-scan first, Haiku on ambiguity; DIAGNOSE masks write tools at `harness/tools` auth.
- **D-31**: Live meta-reasoner gated by `STOKE_META_LIVE=1`.
