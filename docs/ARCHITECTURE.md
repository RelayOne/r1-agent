# Architecture

This is the trunk architecture view for r1. It covers the existing planes
that ship today plus the **Cortex layer**, the **r1d daemon**, the
**lanes-protocol**, and the **three surfaces** (TUI, web, desktop) defined
by the eight specs in `specs/`.

## Audience

- Engineers maintaining the runtime.
- Reviewers checking whether docs match the current code and the scoped
  cortex / lanes / multi-surface work.
- External integrators building MCP clients, IDE plugins, or alternate
  surfaces against the lanes-protocol.
- Stakeholders who need the system shape without reading every package.

## High-level topology

```
                 ┌─────────────────────────────────────────────────────┐
                 │  r1d daemon (per-user singleton, on-demand spawn)   │
                 │                                                     │
   CLI ─unix-sock┤  +───────────+    +───────────────────────────+     │
                 │  │ IPC mux   │    │ SessionHub (sync.Map)     │     │
   TUI ─unix-sock┤  │ • sock    │    │  ┌─────┐ ┌─────┐ ┌─────┐  │     │
   Web ─WS─loopback│ │ • npipe  │────┤  │Sess │ │Sess │ │Sess │  │     │
   Desk ─WS─loopback│ │ • WS+HTTP │   │  │  A  │ │  B  │ │  C  │  │     │
   MCP ─WS/stdio │  │ Bearer   │    │  └──┬──┘ └──┬──┘ └──┬──┘  │     │
                 │  +───────────+    │     │       │       │     │     │
                 │                   │  agentloop.Loop per session│     │
                 │  Discovery ~/.r1/ │  ├───────────────────┐   │     │
                 │  • daemon.json    │  │ Cortex            │   │     │
                 │  • daemon.lock    │  │  Workspace        │   │     │
                 │  Token rotated    │  │  ┌─────┐ ┌─────┐  │   │     │
                 │  on every start   │  │  │Lobe1│…│LobeN│  │   │     │
                 │                   │  │  └─────┘ └─────┘  │   │     │
                 │                   │  │  Round / Spotlight│   │     │
                 │                   │  │  Router (Haiku 4.5)│  │     │
                 │                   │  └───────────────────┘   │     │
                 │                   │  journal.ndjson per sess │     │
                 │                   └───────────────────────────┘     │
                 │                                                     │
                 │            internal/bus/ WAL — shared, scoped       │
                 │            by Scope{TaskID: sessionID}              │
                 └─────────────────────────────────────────────────────┘
                                          │
                                          ▼
                              os/exec.Cmd with cmd.Dir = SessionRoot
                              (worktrees, tools, build, test, lint)
```

## Five system planes

r1 has five architectural planes that compose:

1. **Mission execution** — planning, executing, verifying, reviewing,
   committing.
2. **Governance and evidence** — ledger, WAL, receipts, honesty, cost.
3. **Deterministic skills** — compile, manufacture, register, select, run.
4. **Cortex (parallel cognition)** — Workspace, Lobes, Round, Spotlight,
   Router, Notes.
5. **Surfaces (multi-instance UI)** — TUI, web, desktop, MCP, all over the
   lanes-protocol.

Planes 1–3 are on `main`. Planes 4 and 5 are scoped by the eight specs
in `specs/`.

## Plane 1 — Execution Core

The execution core remains the orchestrator packages:

- `app/`, `workflow/`, `mission/` — top-level lifecycle.
- `engine/`, `agentloop/` — Claude Code / Codex CLI runners; native
  agentic Messages-API loop; streaming; 3-tier timeouts.
- `verify/`, `critic/`, `convergence/` — build/test/lint pipeline,
  AST-aware critic, adversarial self-audit.
- `scheduler/`, `plan/`, `taskstate/` — GRPW priority, file-scope conflict,
  resume, anti-deception phase transitions.
- `worktree/` — per-task worktrees with `BaseCommit`, serialized merges.
- `failure/`, `errtaxonomy/`, `checkpoint/` — 10 failure classes,
  fingerprint dedup, retry escalation.

The thesis is unchanged: one strong implementer per task, explicit
verification, adversarial review across model families, never trust a
self-report.

## Plane 2 — Evidence Core

The evidence plane gives r1 its governance posture:

- **Content-addressed ledger** (`ledger/`) — append-only Merkle-chained
  graph. 16 node-type prefixes. No updates, no deletes. Filesystem +
  SQLite backends via one interface.
- **Durable bus** (`bus/`) — WAL-backed pub/sub with hooks, delayed
  events, and parent-hash causality chains. ULID-indexed. Every event
  carries a STOKE protocol envelope.
- **Supervisor** (`supervisor/`) — deterministic rules engine. 30 rules
  across 10 categories (consensus, drift, hierarchy, research, skill,
  snapshot, SDM, cross-team, trust, lifecycle); 3 per-tier manifests
  (mission, branch, session).
- **Consensus loops** (`ledger/loops/`) — 7-state machine (PRD → SOW →
  ticket → PR → landed).
- **Snapshot** (`snapshot/`) — pre-merge baseline manifest; restore on
  failure.
- **Bridge** (`bridge/`) — adapters wire v1 cost/verify/wisdom/audit into
  the v2 event bus and ledger.

This is why every runtime feature keeps adding audit and metrics hooks
instead of only new prompts.

## Plane 3 — Deterministic Skills

The skills lane spans more than compilation:

- Manufacturing and manifest enforcement (`skillmfr/`).
- Registry and selection (`skill/`, `skillselect/`).
- Seeded repo/user pack libraries.
- Signed pack authoring and verification.
- Runtime registration and verification hooks.
- Pack lifecycle (`init`, `info`, `install`, `list`, `publish`, `search`,
  `sign`, `verify`, `update`, `serve`).

Pack distribution is a real subsystem now — `r1 skills pack serve` exposes
`/healthz`, `/v1/packs`, `/v1/packs/<pack>`, `/v1/packs/<pack>/archive.tar.gz`.

## Plane 4 — Cortex Layer (scoped)

The Cortex is r1's parallel-cognition substrate. New `internal/cortex/`
package, defined by `specs/cortex-core.md` (foundation) and
`specs/cortex-concerns.md` (six v1 Lobes).

### Component diagram

```
  ┌──────────────────────────────────────────────────────────────────┐
  │                    Cortex (one per session)                      │
  │                                                                  │
  │   ┌────────────────────────────────────────────────────────┐    │
  │   │                  Workspace                             │    │
  │   │  • RWMutex-protected []Note                           │    │
  │   │  • write-through to internal/bus/ WAL (durable)       │    │
  │   │  • emits hub.Event{cortex.note.published}             │    │
  │   │  • Snapshot / UnresolvedCritical / Drain / Replay     │    │
  │   └─────────────┬──────────────────────────────────────────┘    │
  │                 │                                                │
  │   ┌─────────────┴──────────────┐  ┌──────────────────────────┐  │
  │   │     Spotlight              │  │     Round (barrier)      │  │
  │   │  Single highest-priority   │  │  Open(N) → Done × N →    │  │
  │   │  unresolved Note            │  │  Wait(deadline) → Close  │  │
  │   └────────────────────────────┘  └──────────────────────────┘  │
  │                                                                  │
  │   ┌────────────────────────────────────────────────────────┐    │
  │   │                     Lobes (N concurrent)               │    │
  │   │  ┌───────────────┐  ┌──────────────┐  ┌─────────────┐ │    │
  │   │  │MemoryRecall   │  │WALKeeper     │  │RuleCheck    │ │    │
  │   │  │(deterministic)│  │(deterministic)│ │(deterministic)│   │
  │   │  └───────────────┘  └──────────────┘  └─────────────┘ │    │
  │   │  ┌───────────────┐  ┌──────────────┐  ┌─────────────┐ │    │
  │   │  │PlanUpdate     │  │ClarifyingQ   │  │MemoryCurator│ │    │
  │   │  │(Haiku 4.5)    │  │(Haiku 4.5)   │  │(Haiku 4.5)  │ │    │
  │   │  └───────────────┘  └──────────────┘  └─────────────┘ │    │
  │   │  Each: goroutine + panic-recover + ctx-cancel           │    │
  │   │  LLM Lobes acquire LobeSemaphore (cap=5; hard cap 8)    │    │
  │   └────────────────────────────────────────────────────────┘    │
  │                                                                  │
  │   ┌────────────────────────────────────────────────────────┐    │
  │   │                Router (Haiku 4.5)                      │    │
  │   │  4 tools: interrupt, steer, queue_mission, just_chat   │    │
  │   │  Called by REPL/web on mid-turn user input             │    │
  │   │  p99 ≤ 2 s ; temperature=0 ; cached prompt+tools        │    │
  │   └────────────────────────────────────────────────────────┘    │
  │                                                                  │
  │   ┌────────────────────────────────────────────────────────┐    │
  │   │           Pre-warm pump + Budget controller            │    │
  │   │  max_tokens=1 warming on Start, every 4 min            │    │
  │   │  BudgetTracker: Lobe output ≤ 30% main output / round  │    │
  │   └────────────────────────────────────────────────────────┘    │
  └───────────────────────┬──────────────────────────────────────────┘
                          │
                          ▼
       agentloop.Loop hooks (composed)
       ─ MidturnCheckFn → drains workspace into supervisor note
       ─ PreEndTurnCheckFn → critical Note refuses end_turn
       ─ NEW: OnUserInputMidTurn → Router decides 1 of 4 tools
```

### Integration points with `agentloop.Loop`

Three integration points; all backwards-compatible (cortex absent →
behavior unchanged):

1. **`Cortex` field on `agentloop.Config`** — wires through a
   `agentloop.CortexHook` interface (avoids import cycle; `*cortex.Cortex`
   satisfies it structurally).
2. **`MidturnCheckFn` composition** — Cortex runs first, operator hook
   second, joined with `\n\n`.
3. **`PreEndTurnCheckFn` composition** — Cortex critical-Note gate
   short-circuits before any operator-defined gate.

A fourth integration point lives in the chat REPL / web client (not
agentloop): on stdin or WS user input mid-turn, the REPL calls
`Cortex.OnUserInputMidTurn(ctx, userInput, turnCancel)`, which invokes the
Router and enacts the chosen tool's effect. The Router's choice can cancel
the per-turn context (`interrupt`), publish a soft Note (`steer`), enqueue
a mission (`queue_mission`), or no-op the loop (`just_chat`).

### Drop-partial interrupt protocol

On `Interrupt`, the cortex runtime:

1. Calls `cancelTurn()` on the per-turn context.
2. Drains both the response chan (`for range respCh {}`) and the done chan
   (`<-doneCh`) — both must complete before return, else the SSE reader
   leaks.
3. Discards the partial assistant message accumulator entirely (Anthropic
   gives no recovery handle for incomplete `tool_use` blocks; persisting
   would 400 the next API call).
4. Appends a synthetic user message describing the interrupt to the
   committed history.
5. Returns the new history, which is `user`-terminated and valid for the
   next API call.

A 30s ping-based idle watchdog auto-cancels on connection stalls.

### Six v1 Lobes

Three deterministic, three Haiku 4.5:

- **MemoryRecallLobe** (deterministic) — TF-IDF over `memory.Store` and
  `wisdom.Store`; rebuilds on `cortex.workspace.memory_added` events;
  surfaces top-3 dedup'd matches per round as `info` Notes.
- **WALKeeperLobe** (deterministic) — `hub.Subscriber` whose `Filter`
  returns true for every event type; forwards each into `bus.Bus.Publish`
  with structured framing (`cortex.hub.<original>`); backpressure-shed
  drops `info` events when the WAL backlog exceeds 1k.
- **RuleCheckLobe** (deterministic) — subscribes to
  `bus.Pattern{TypePrefix: "supervisor.rule.fired"}`; converts each fire
  to a Note; `trust.*` and `consensus.dissent.*` map to `critical` and
  refuse `end_turn` until acknowledged.
- **PlanUpdateLobe** (Haiku 4.5) — every 3rd assistant turn boundary or on
  action-verb input; emits structured JSON deltas; auto-applies edits;
  queues additions and removals as a single `user-confirm` Note.
- **ClarifyingQLobe** (Haiku 4.5) — runs after every user turn; emits up
  to 3 `queue_clarifying_question` tool calls; caps outstanding clarify
  Notes at 3.
- **MemoryCuratorLobe** (Haiku 4.5) — every 5 turns or on
  `task.completed`; auto-writes only `Category ∈ {fact}` (operator-
  configurable); queues other categories as `memory-confirm` Notes;
  privacy filter drops `private`-tagged source messages; appends every
  auto-write to `~/.r1/cortex/curator-audit.jsonl`.

Per-Lobe enable + escalation flags live in `~/.r1/config.yaml`. Per-turn
budget caps Lobe collective output at 30% of main output. Sonnet
escalation is opt-in per Lobe and only fires on `SevCritical` Note in the
same round or `escalate_on_failure: true` after a prior error.

## Plane 5 — Surfaces & lanes-protocol

### Lane state machine and event types

A **lane** is the per-surface visible thread of activity inside a session.

```
                       ┌──── pin ────┐ (orthogonal — does not change state)
                       │             │
                       ▼             ▼

  [created]──►( pending )──►( running )──►( done )       (terminal)
                  │             │  ▲
                  │             │  │
                  │             │  └── unblock ──┐
                  │             │                │
                  │             ▼                │
                  │         ( blocked )──────────┘
                  │             │
                  ├──cancel─────┤
                  │             │
                  ▼             ▼
              ( cancelled ) ( errored )
                                ▲
                                │
                  any state ────┘  (on unrecoverable error)
```

Six event types are exhaustive. Adding a seventh is a wire-version bump.

| Event | When | Purpose |
|---|---|---|
| `lane.created` | Once per lane, before any other event | `kind` ∈ {main, lobe, tool, mission_task, router}; `parent_lane_id`; `label` |
| `lane.status` | State transition | `status`, `prev_status`, `reason`, `reason_code` |
| `lane.delta` | Streaming content | One block per event (text, thinking, tool_use_*, tool_result, note_ref) |
| `lane.cost` | ≤1 Hz per lane | tokens_in/out, cached_tokens, usd, cumulative_usd |
| `lane.note` | Mirror of cortex Note publish | Lightweight pointer (note_id, severity, kind, summary) |
| `lane.killed` | One-shot kill signal | Followed by `lane.status` to terminal |

### Wire envelopes

- **JSON-RPC 2.0 over WS** for browsers + desktop. `$/event`
  notifications carry `{sub, seq, event, session_id, lane_id, event_id, at,
  data}`. WS subprotocol token: `r1.lanes.v1` (CSWSH defense).
- **NDJSON over stdout** for `streamjson` consumers. `lane.killed`,
  `lane.note(severity=critical)`, `lane.status(errored)` route through
  the critical lane; everything else goes to observability.
- **HTTP+SSE fallback** with `Last-Event-ID` per WHATWG Server-sent events
  spec.

### Per-session monotonic `seq`

Allocated by the per-session goroutine that owns the WAL append (single
writer, no contention). `seq=0` is reserved for the synthetic
`session.bound` event emitted on every fresh subscription so clients have
a known floor. Replay uses `since_seq` (JSON-RPC) or `Last-Event-ID`
(SSE).

### Five MCP tools

`r1.lanes.list`, `r1.lanes.subscribe` (streaming), `r1.lanes.get`,
`r1.lanes.kill` (idempotent + cascade), `r1.lanes.pin`. All live in
`internal/mcp/lanes_server.go`. The cortex tools (`r1.cortex.notes`,
`r1.cortex.publish`) live in `internal/mcp/cortex_server.go` and are
spec 1's responsibility.

## Plane 6 — r1d daemon

Single process per user. N concurrent sessions, each a goroutine. Each
session carries `SessionRoot string` threaded via `cmd.Dir`. The
goroutine-per-session model works only because `cmd.Dir` is the
established pattern (CLAUDE.md design decision #1) — one stray
`os.Chdir` would silently leak workdir between sessions. The
`os.Chdir` audit + CI lint at `tools/cmd/chdir-lint/` is the gate
before multi-session is enabled. A per-session sentinel checks
`os.Getwd() == SessionRoot` before each tool dispatch and panics on
mismatch.

### `r1 serve` topology (TASK-54)

This is the unified subcommand that replaces `r1 daemon` + `r1
agent-serve`. The legacy commands print a one-line deprecation hint
to stderr and forward to `serve` with the appropriate flag prefix.
Detailed ASCII topology lives in `specs/r1d-server.md` §4 — the
condensed shape is:

```
+-------------+         +-------------+         +-------------+
| Browser/Tau |         |  CLI/r1 ctl |         |  TUI lanes  |
+------+------+         +------+------+         +------+------+
       | ws://127.0.0.1:p     | unix sock /            | unix sock
       | + Bearer + Origin    | npipe + peer-cred      | + peer-cred
       v                      v                        v
+----------------------------------------------------------+
|             r1 serve  (one process per user)             |
|                                                          |
|  +---------+   +-------+      +---------------------+   |
|  | IPC mux |   | HTTP+WS|     |   SessionHub        |   |
|  | unix    |   | random |     |  sync.Map<id,*Sess> |   |
|  | npipe   |   | ephem  |     |  + workdir validate |   |
|  +----+----+   +---+----+     +----------+----------+   |
|       |            |                     |              |
|       +-----+------+--------+------------+              |
|             v               v            v              |
|        +---------+    +---------+   +---------+         |
|        |Session A|    |Session B|   |Session C|         |
|        |goroutine|    |goroutine|   |goroutine|         |
|        |SessionR |    |SessionR |   |SessionR |         |
|        |=/path/A |    |=/path/B |   |=/path/C |         |
|        |journal  |    |journal  |   |journal  |         |
|        | .ndjson |    | .ndjson |   | .ndjson |         |
|        +---------+    +---------+   +---------+         |
|                                                          |
|  ~/.r1/daemon.lock (gofrs/flock)                         |
|  ~/.r1/daemon.json (port + 256-bit token, mode 0600)     |
|  ~/.r1/sessions-index.json (replay manifest)             |
+----------------------------------------------------------+
                          |
                          v
                +----------------------+
                | os/exec.Cmd          |
                | cmd.Dir = SessionRoot|
                +----------------------+
```

Key rules embedded in the topology (cross-linked to spec sections):

- **Single-instance**: `~/.r1/daemon.lock` flock acquired before any
  listener binds; second `r1 serve` exits 1 with `daemon already
  running, pid=<N>, sock=<path>` (TASK-50, daemonlock package).
- **Authentication split**: peer-cred on the unix socket / named
  pipe (no token); 256-bit Bearer on the loopback HTTP/WS surface
  (Origin + Host pinned to loopback).
- **Per-session journal**: append-only ndjson at
  `~/.r1/sessions/<id>/journal.ndjson`; terminal kinds fsync, others
  flush. Replay reconstructs sessions on daemon restart and fans
  `daemon.reloaded` to reconnecting clients before any live delta.
- **`cmd.Dir = SessionRoot`** is the load-bearing isolation
  primitive. The `chdir-lint` AST-based scanner at
  `tools/cmd/chdir-lint/` blocks every unannotated `os.Chdir /
  os.Getwd / filepath.Abs("") / os.Open("./...")` call site; the
  per-session sentinel panics on `os.Getwd() != SessionRoot`.

### Listeners

| Surface | Endpoint | Auth |
|---|---|---|
| CLI (`r1 chat`, `r1 ctl`) | `$XDG_RUNTIME_DIR/r1/r1.sock` (Linux/macOS) / `\\.\pipe\r1-<USER>` (Windows) | Peer-cred check (`SO_PEERCRED` / `LOCAL_PEERCRED`); no token |
| Web / Desktop / MCP | `ws://127.0.0.1:<port>` + `http://127.0.0.1:<port>` | 256-bit Bearer (HTTP) or `Sec-WebSocket-Protocol: r1.bearer, <token>` (WS); Origin pin + Host pin |

Port: random ephemeral on first start, written to `~/.r1/daemon.json`
(mode 0600, token rotated on every start). Discovery via `r1 ctl
discover`.

Single-instance enforcement: `gofrs/flock` advisory lock on
`~/.r1/daemon.lock` plus the bind-is-exclusive property of the socket
path / port.

### Session lifecycle

- **Create**: `POST /v1/sessions {workdir, model, ...}` validates
  `workdir` (absolute, exists, writable, not under `~/.r1/`). Generates
  `session_id = "sess_" + uuid.New()`. Calls `cortex.NewWorkspace(SessionRoot)`.
  Opens `<workdir>/.r1/sessions/<id>/journal.ndjson`. Spawns
  `go session.run(ctx)`.
- **Attach**: WS `/v1/sessions/:id/ws`; subscribe with `since_seq?` —
  server replays from journal, then live deltas.
- **Detach**: client closes WS; session goroutine continues; events buffer
  in WAL.
- **Resume**: client reconnects with `since_seq: <last>` → server replays
  from journal, then live.
- **Pause / Resume** (workflow-level): `POST /:id/pause` or `:id/resume`
  flips state; sub-context honors `ctx.Done()`.
- **Kill**: `DELETE /:id` cancels session ctx, drains in-flight tool
  calls (5s grace), fsyncs journal, writes final `session.ended`.

### Hot upgrade

Restart-required, transparent. Operator runs `r1 update` then `r1 serve
restart`. New daemon scans `~/.r1/sessions-index.json`, re-opens each
`journal.ndjson`, rebuilds `*Session` (workspace + Lobe state from
journal events). Broadcasts `daemon.reloaded {at, version}` to
reconnecting clients. Clients reconnect with `Last-Event-ID` /
`since_seq` → server replays from journal.

### `--install` mode

`r1 serve --install` writes a platform-appropriate service unit via
`kardianos/service`:

- macOS: `~/Library/LaunchAgents/dev.relayone.r1.plist`.
- Linux: `~/.config/systemd/user/r1.service`. `loginctl enable-linger
  $USER` for headless boxes.
- Windows: Service Control Manager service `r1.daemon`.

## Surface architectures

### TUI (`internal/tui/lanes/` — Bubble Tea v2)

A new panel inside the existing `internal/tui/`. Coexists with the v1
`internal/tui/renderer/` and `interactive.go` (different Bubble Tea
major versions; module paths differ).

**Key shape:**

- Adaptive layout: side-by-side columns when wide
  (`width >= cols * LANE_MIN_WIDTH (32)`, max 4 columns), vertical stack
  when narrow, focus mode (65/35 main+peers) when `enter` zoomed.
- Single fan-in `chan laneTickMsg` + `waitForLaneTick` `tea.Cmd` re-armed
  on every receive (the canonical realtime example for r1).
- A producer goroutine coalesces upstream events at 200–300 ms windows so
  we never push more than ~5 Hz into Bubble Tea even when upstream is
  firing at 200 Hz.
- Per-lane `renderCache` invalidated only on dirty-flag flip, width
  change, status transition, focus change, or spinner tick (one per
  coalesce window).
- Status vocabulary glyphs (`pending(·) running(▸) blocked(⏸) done(✓)
  errored(✗) cancelled(⊘)`) paired with `compat.AdaptiveColor` (Tokyo
  Night palette). `NO_COLOR` / `TERM=dumb` keep glyphs legible.
- Two transports: `localIPCTransport` dials `~/.r1/r1d.sock`;
  `wsTransport` dials `ws://127.0.0.1:<port>`.

### Web (`web/` — React 18 + Vite 6 + Tailwind 3 + shadcn/ui)

Three-column Cursor 3 "Glass" layout:

```
+──────────+──────────────────────────────────────+──────────────+
│ Sessions │              Chat / Tile pane         │   Lanes      │
│  list    │                                        │  sidebar     │
│ (left)   │  Streamdown markdown                   │  (right)     │
│          │  + tool / reasoning / plan / diff      │              │
│ - sess A │    cards (@ai-sdk/elements)            │  ▸ Lobe 1    │
│ - sess B │  Composer (Cmd/Ctrl+Enter sends)       │  ⏸ Tool 1   │
│ - sess C │  StopButton during stream → interrupt  │  ✓ Lobe 2   │
│          │                                        │  · Lobe 3    │
│ [+ New]  │  Tile mode: 1×2 / 1×3 / 2×2 grid       │  ✗ Lobe 4    │
│          │  pinning 2-4 lanes into the center     │              │
+──────────+──────────────────────────────────────+──────────────+
            ⚡ r1 │ external │ $0.0837 │ 42 turns │ haiku-4.5 │ ?=help
```

**Key shape:**

- React 18 + Vite 6 + Tailwind 3 + shadcn/ui (matches `desktop/`).
- Streaming markdown via `vercel/streamdown` (graceful partial-Markdown,
  Shiki, KaTeX, Mermaid, rehype-harden).
- AI cards via `@ai-sdk/elements` (`Tool`, `Reasoning`, `Plan`,
  `CodeBlock`, `Conversation`, `PromptInput`).
- Streaming hook: AI SDK 6 `useChat` with `message.parts` model — maps
  directly to lanes-protocol envelopes.
- Routing via react-router 7: `/sessions/:id`,
  `/sessions/:id/lanes/:lane_id`, `/settings`.
- State: `zustand` 5; one store **instance per daemon connection**.
- WS: hand-rolled `ResilientSocket` (state machine, exponential backoff
  + jitter, 30s ping watchdog, `Last-Event-ID` replay, 4401 →
  re-mint-ticket → reconnect).
- CSP locked to loopback: `default-src 'self'; connect-src 'self'
  ws://127.0.0.1:* http://127.0.0.1:*; ...`.
- Vite `build.outDir = '../internal/server/static/dist'`. Picked up
  automatically by `internal/server/embed.go` (`//go:embed static`).

### Desktop (`desktop/` — Tauri 2 augmentation)

Existing 12-phase R1D plan untouched. New files only.

**Discovery flow:**

1. `read_daemon_json()` reads `~/.r1/daemon.json`.
2. `probe_external()` tries TCP connect to `ws://127.0.0.1:<port>` with
   1s timeout.
3. On `NotFound | Refused`, `spawn_sidecar()` runs the bundled `r1 serve
   --port=0 --emit-port-stdout` via `ShellExt::sidecar`. Reads the
   chosen port from the child's stdout NDJSON `daemon.listening` event.
4. Wizard offers `r1 serve --install` for always-on operation.

**Per-session workdir:** `tauri-plugin-store` writes `sessions.json`
under the app data dir. Folder picker via `@tauri-apps/plugin-dialog`
`open({directory: true})`. Push to daemon via
`session.set_workdir`; the Go side binds it to `cmd.Dir` for any
subprocess spawned for that session.

**Lane streaming:** one `tauri::ipc::Channel<LaneEvent>` per session.
Per-session forwarder task reads daemon WS, parses lane frames into
serde enums, and `channel.send()`s them with a 1024-event ring buffer
(drops `lane.delta` on overflow; status/spawn/kill never drop).

**Native menu bar** with platform-conditional layout (macOS app menu vs
Linux/Windows Help menu). Pop-out lane via `Cmd+\` opens a
`WebviewWindow` with label `lane:<session>:<lane>`.

**Auto-start** via `tauri-plugin-autostart`. UI auto-start (login items
/ registry / `.desktop` autostart) is independent from daemon
auto-start (`r1 serve --install` via `kardianos/service`).

**Component sharing:** `packages/web-components/` workspace package
houses `LaneCard`, `LaneSidebar`, `LaneDetail`, `PoppedLaneApp` —
consumed by both `web/` and `desktop/` via npm workspace protocol.

## Cross-cutting: MCP-everything

The agentic test harness (spec 8) is a cross-cutting concern, not a new
plane. It consolidates `internal/mcp/r1_server.go` to publish the full
catalog:

- `r1.session.*` — `start`, `send`, `cancel`, `list`, `get`, `resume`.
- `r1.lanes.*` — `list`, `subscribe`, `get`, `kill`, `pin`.
- `r1.cortex.*` — `notes`, `publish`, `lobes_list`, `lobe_pause`,
  `lobe_resume`.
- `r1.mission.*` — `create`, `list`, `cancel`.
- `r1.worktree.*` — `list`, `diff`.
- `r1.bus.tail` — replay events with `since_seq`.
- `r1.verify.*` — `build`, `test`, `lint`.
- `r1.tui.*` — `press_key`, `snapshot`, `get_model` (via
  `internal/tui/teatest_shim.go`).

A CI lint at `tools/lint-view-without-api/` scans React, Bubble Tea, and
Tauri sources for interactive elements; fails the build when no MCP tool
exists for the corresponding action. A Go runner at
`tools/agent-feature-runner/` parses Gherkin-flavored markdown
(`*.agent.feature.md`) and dispatches each step through MCP. Web is
covered by Playwright MCP; component contracts by Storybook MCP. The
contract for external agents lives in `docs/AGENTIC-API.md`.

## Existing surfaces (today, before specs 4 / 6 / 7)

`r1` ships nine binaries on `main` today:

| Binary | Purpose |
|---|---|
| `r1` | Primary orchestrator — 30+ subcommands |
| `stoke-acp` | Agent Client Protocol adapter for editor integrations |
| `stoke-a2a` | Agent-to-Agent peering: signed cards, HMAC tokens, x402 micropayments, saga compensators |
| `stoke-mcp` | MCP codebase tool server (ledger, wisdom, research, skill stores as MCP tools) |
| `stoke-server` | Mission API HTTP server |
| `stoke-gateway` | Managed-cloud gateway: hosted session state, centralized pool management |
| `r1-server` | Per-machine dashboard (port 3948) — discovers running r1 instances, live event stream, 3D ledger visualizer |
| `chat-probe` | Chat-descent gate + sessionctl socket diagnostic |
| `critique-compare` | Bench runner for critic/reviewer prompt tuning |

Once `r1d-server` lands, `r1 serve` becomes the canonical entrypoint for
long-running daemons and consolidates the existing `r1 daemon` and
`r1 agent-serve` into one process; old commands stay as aliases for one
minor version.

## Internal package map (132 packages)

Grouped by plane. The table is canonical with `CLAUDE.md`'s package map.

**Governance v2 (append-only, content-addressed):**
`contentid`, `stokerr`, `ledger`, `ledger/nodes`, `ledger/loops`, `bus`,
`supervisor` (+ 9 rule subpackages), `concern`, `harness`, `snapshot`,
`wizard`, `skillmfr`, `bench`, `bridge`.

**Cortex (scoped — spec 1, 2):**
`cortex` (Workspace, Lobe interface, Round, Spotlight, Router,
drop-partial interrupt, pre-warm pump, BudgetTracker, persist),
`cortex/lobes/llm` (LobePromptBuilder, Escalator, meta_keys),
`cortex/lobes/memoryrecall`, `cortex/lobes/walkeeper`,
`cortex/lobes/rulecheck`, `cortex/lobes/planupdate`,
`cortex/lobes/clarifyq`, `cortex/lobes/memorycurator`.

**Core workflow:**
`agentloop`, `app`, `hub`, `hub/builtin`, `mission`, `workflow`,
`engine`, `orchestrate`, `scheduler`, `plan`, `taskstate`.

**Planning + decomposition:**
`interview`, `intent`, `conversation`, `skillselect`, `chat`, `operator`,
`hire`.

**Code analysis:**
`goast`, `repomap`, `symindex`, `depgraph`, `chunker`, `tfidf`, `vecindex`,
`semdiff`, `diffcomp`, `gitblame`, `depcheck`.

**File + workspace:**
`atomicfs`, `fileutil`, `filewatcher`, `worktree`, `branch`, `hashline`.

**Testing + verification:**
`baseline`, `verify`, `convergence`, `testgen`, `testselect`, `critic`,
`reviewereval`, `smoketest`.

**Error handling:**
`failure`, `errtaxonomy`, `checkpoint`.

**Code generation:**
`patchapply`, `extract`, `autofix`, `conflictres`, `tools`.

**Agent behavior:**
`boulder`, `specexec`, `handoff`, `consolidation`.

**Knowledge + learning:**
`memory`, `wisdom`, `research`, `flowtrack`, `replay`, `sharedmem`,
`stancesign`.

**Executors (multi-task agent):**
`executor`, `router`, `browser`, `deploy`, `websearch`, `delegation`,
`fanout`, `oneshot`.

**LLM integration:**
`apiclient`, `provider`, `modelsource`, `mcp`, `model`, `prompt`,
`prompts`, `promptcache`, `promptguard`, `microcompact`, `ctxpack`,
`tokenest`, `costtrack`, `litellm`.

**Permissions + security:**
`consent`, `rbac`, `hooks`, `hitl`, `scan`, `secrets`, `redact`,
`redteam`, `policy`, `encryption`, `retention`.

**Config + session:**
`config`, `session`, `sessionctl`, `subscriptions`, `pools`, `context`,
`env`, `eventlog`, `runtrack`, `correlation`.

**Infrastructure:**
`agentmsg`, `dispatch`, `logging`, `metrics`, `telemetry`, `notify`,
`stream`, `streamjson`, `jsonutil`, `schemaval`, `validation`, `perflog`,
`topology`, `gateway`, `cloud`, `trustplane`, `a2a`, `agentserve`.

**UI + interfaces:**
`tui`, `tui/lanes` (scoped), `viewport`, `repl`, `server`,
`server/sessionhub` (scoped), `server/ws` (scoped),
`server/ipc` (scoped), `journal` (scoped), `daemonlock` (scoped),
`daemondisco` (scoped), `serviceunit` (scoped), `remote`, `report`,
`progress`, `audit`, `skill`, `plugins`, `preflight`, `taskstats`.

Package count is verified in CI via `make check-pkg-count`. Adding a new
package requires bumping the expected value.

## Key design decisions (canonical with CLAUDE.md)

1. `cmd.Dir` for worktree cwd (Claude Code has no `--cd` flag).
2. `--tools` for hard built-in restriction; `--allowedTools` only
   auto-approves.
3. MCP triple isolation: `--strict-mcp-config` + empty config +
   `--disallowedTools mcp__*`.
4. Sandbox via settings.json per worktree (no `--sandbox` CLI flag).
5. `apiKeyHelper: null` (JSON null via `*string`) suppresses repo
   helpers in Mode 1.
6. `sandbox.failIfUnavailable: true` — fail-closed.
7. Process group isolation: `Setpgid: true` + `killProcessGroup` (SIGTERM
   then SIGKILL).
8. `git merge-tree --write-tree` for zero-side-effect conflict validation.
9. `mergeMu sync.Mutex` serializes all merges to main.
10. GRPW priority: tasks with most downstream work dispatch first.
11. Cross-model review via `model.CrossModelReviewer()`.
12. Retry: compare BEFORE overwriting `lastFailure`. Copy phase, don't
    mutate. Clean worktree per retry.
13. `BaseCommit` captured at worktree creation for `diff
    BaseCommit..HEAD`.
14. Worktree cleanup: `--force` + `os.RemoveAll` fallback + `worktree
    prune`.
15. `model.Resolve()` walks Primary → FallbackChain (Claude → Codex →
    OpenRouter → API → lint-only).
16. Enforcer hooks installed in every worktree via `hooks.Install()`.
17. Event-driven reminders fire during tool use (context >60%, error 3×,
    test write, etc.).
18. ROI filter removes low-value tasks before execution.
19. `session.SessionStore` interface: both JSON (`Store`) and SQLite
    (`SQLStore`) satisfy it.
20. Budget enforcement: `CostTracker.OverBudget()` checked before each
    execute attempt.
21. Failure fingerprint dedup: `failure.Compute()` + `MatchHistory()`
    escalates repeated failures.
22. `verificationExplicit` bool distinguishes "all false" from "omitted"
    in YAML policy parsing.
23. Dependency-aware test selection via `testselect.BuildGraph()`.
24. Ranked repomap injected into execute prompts (token-budgeted via
    `RenderRelevant`).
25. Pre-merge snapshots (`snapshot.Take`) with restore-on-failure for safe
    rollback.
26. Speculative execution (`--specexec`): 4 strategies in parallel, pick
    the winner.
27. Codex/Claude parity: both runners populate `CostUSD`, `DurationMs`,
    `NumTurns`, `Tokens`.
28. V2 bridge adapters: v1 cost/verify/wisdom/audit emit bus events +
    write ledger nodes via the bridge package.

## Decisions specific to the cortex / lanes / multi-surface scope

From `docs/decisions/index.md`:

- **D-2026-05-02-01** — Spec split = 8 specs. Build order: 1 → (2,3) →
  (4,5) → (6,7) → 8.
- **D-2026-05-02-04** — Merge model = agent-decides. Router LLM (Haiku
  4.5) with 4 tools. Critical Notes refuse end_turn.
- **D-C1..C7** — Cortex foundation choices: `internal/cortex/`,
  GWT-not-Blackboard, write-through to bus WAL, drop-partial interrupt,
  pre-warm cache, 5-Lobe budget with hard cap 8, Haiku 4.5 floor with
  Sonnet escalation gated.
- **D-S1..S7** — Surface choices: shared status vocabulary, render
  coalescing 5–10 Hz, Bubble Tea v2, Cursor 3 "Glass", Streamdown +
  AI SDK 6, WS subprotocol auth, Tauri augment-don't-redo.
- **D-D1..D5** — Daemon choices: per-user singleton on-demand, one process
  N goroutines via `cmd.Dir`, JSON-RPC 2.0 over WS / unix-sock with
  monotonic seq, `os.Chdir` audit + lint required, `gofrs/flock` for
  single-instance.
- **D-A1..A5** — Agentic choices: MCP-primary single wire protocol,
  goal-shaped tools, Playwright + teatest + Storybook MCP, Gherkin DSL,
  view-without-API CI lint.

## Status

### Done
- Mission runtime + verification core.
- Evidence + governance plane.
- Deterministic skill + pack-registry foundations.
- Runtime audit / metrics / cancel / timeout extension surfaces.
- Wave 2 R1-parity surfaces.

### In Progress
- Wider product adoption of the deterministic skill lane.
- Race-clean regression sweep across `internal/`.

### Scoped
- Cortex layer (specs 1–2): Workspace, Lobes, Round, Spotlight, Router,
  drop-partial, pre-warm, budget, persist, six v1 Lobes.
- Lanes wire format (spec 3) + 5 MCP tools.
- Three surfaces: TUI lanes panel (spec 4), web chat (spec 6), desktop
  augmentation (spec 7).
- r1d daemon (spec 5): per-user singleton, multi-session goroutines,
  `os.Chdir` audit, journal replay.
- Agentic test harness (spec 8): consolidated MCP catalog, view-without-API
  lint, Playwright + Storybook MCP, Gherkin DSL.

### Scoping
- Cross-machine session migration.
- Per-tool throttling policy in `.stoke/`.
- Encryption-at-rest for journals.
- Broader superiority reporting against peer runtimes.

### Potential — On Horizon
- Cloud daemon beyond loopback singleton.
- Multi-tenant per-host (multiple uids on a shared box).
- OpenTelemetry export of lane events.
- Cross-product distribution and exchange of governed deterministic
  skills.
