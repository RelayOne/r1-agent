# How It Works

This is the operator and developer walkthrough for r1: what happens from
the moment you double-click the desktop app or run `r1 chat` in a
terminal, through the agent's parallel cognition, through a mid-turn
redirect, through a daemon restart, and back to "your prompt converged
without you babysitting it."

## Audience

- Operators running r1 day-to-day.
- Engineers onboarding to the runtime.
- Reviewers validating the cortex / lanes / multi-surface story.
- Anyone trying to understand "what is r1 actually doing right now."

This doc covers two narratives:

1. **Today, on `main`** — the governed mission runtime, the Tauri R1D-1..R1D-12
   desktop, the plan/execute/verify loop, the deterministic skill substrate.
2. **The cortex / lanes / multi-surface scope** — a session walkthrough
   threaded across cortex spawn, six Lobes running concurrently, lanes
   rendering across surfaces, mid-turn user input handled by the Router,
   and a daemon restart with seamless replay.

## Today, on `main`

### Journey 1 — Run a single mission

The core loop has four phases:

1. **PLAN** — Claude (read-only, MCP disabled, repomap injected) drafts a
   plan against the codebase. Output: a `plan.json` listing tasks, deps,
   ROI tags, allowed file scopes, acceptance criteria.
2. **EXECUTE** — Claude or Codex runs against the repo (sandbox on,
   verification descent + honeypot gate on each end-of-turn).
3. **VERIFY** — Build + test + lint + scope check + protected-file check
   + AST-aware critic (secrets, injection, debug prints).
4. **REVIEW** — Cross-model gate (Claude implements → Codex reviews, or
   vice versa). Reviewer dissent blocks merge.

After REVIEW: `git merge-tree --write-tree` validates the merge with zero
side effects, `mergeMu sync.Mutex` serializes the merge to main, the
worktree is cleaned up (`--force` + `os.RemoveAll` fallback + `worktree
prune`), the attempt + session state + learned patterns + ledger node are
saved.

### Journey 2 — Multi-task plan with parallel agents

```bash
r1 build --plan stoke-plan.json --workers 4
```

For each dispatchable task (parallel, file-scope conflicts respected):

1. Resolve provider via `model.Resolve()`: Claude → Codex → OpenRouter →
   direct API → lint-only.
2. Acquire pool worker (least loaded, circuit breaker, OAuth poller).
3. Create git worktree + install enforcer hooks (PreToolUse +
   PostToolUse).
4. Write `r1.session.json` signature; heartbeat every 30s.
5. Run PLAN → EXECUTE → VERIFY → REVIEW → MERGE.
6. On failure: classify (10 classes), extract specifics, discard
   worktree, create fresh, inject retry brief + diff summary. Max 3
   attempts. Same error twice → escalate (`failure.Compute()`
   fingerprint dedup).

Throughout: structured events emit to `.stoke/bus/events.log` (NDJSON,
hash-chained); a `BuildReport` is generated at `.stoke/reports/latest.json`;
event-driven reminders fire during tool use (context >60%, error 3×,
test-write, turn-drift, etc.).

### Journey 3 — Build / install a deterministic skill

```bash
r1 skills pack init my-deploy-skill     # scaffold a pack
r1 skills pack info my-deploy-skill     # inspect
r1 skills pack publish my-deploy-skill  # publish to local registry
r1 skills pack sign my-deploy-skill     # signed bundle
r1 skills pack install my-deploy-skill  # activate
r1 skills pack update my-deploy-skill   # refresh
```

`r1 skills pack serve` exposes the published pack library as a small
read-only HTTP registry: `/healthz`, `/v1/packs`, `/v1/packs/<pack>`,
`/v1/packs/<pack>/archive.tar.gz`. Installed signed packs are verified at
runtime; runtime registration refuses unsigned or invalid-signature packs.

### Journey 4 — The Tauri 2 desktop today

The R1D-1..R1D-12 phases shipped on `main`. The desktop already has:
session view, SOW tree, descent ladder, descent evidence, skill catalog,
ledger viewer, memory bus viewer, settings, MCP servers panel,
observability dashboard, multi-session view, signing + auto-update infra,
store-submission readiness. It currently spawns a per-session `r1
--one-shot` subprocess.

The cortex / lanes scope flips this default: `r1 serve` becomes the
primary process, the bundled binary becomes a sidecar fallback.

## The cortex / lanes / multi-surface narrative

A representative session, end-to-end, across all the moving parts. Names
match the specs in `specs/` and the package map in CLAUDE.md.

### Frame 1 — Eric opens the web UI

Eric opens `http://127.0.0.1:7777/` in Chrome. The page loads from
`internal/server/static/dist/` (an embedded SPA bundle, picked up
automatically by `internal/server/embed.go`'s `//go:embed static`).

The web app:

1. Calls `GET /api/daemons` to list known r1d daemons.
2. Discovers the daemon at `127.0.0.1:7777` via the discovery file
   (`~/.r1/daemon.json`) mirrored over HTTP.
3. Calls `POST /auth/ws-ticket` to mint a short-lived (~30s) WS
   subprotocol token.
4. Opens `new WebSocket('ws://127.0.0.1:7777/ws', ['r1.bearer', token])`.
   The daemon validates the subprotocol, the `Origin`, the `Host`, and
   the token. Handshake completes.
5. Calls `GET /api/sessions` — empty list.

The user clicks "New Session". A shadcn `Dialog` opens with three fields:
model (Sonnet 4.6 default), workdir (FSA `showDirectoryPicker()` if
available; manual path entry with autocomplete from
`r1d.listAllowedRoots()` otherwise), system-prompt preset.

Eric picks `/home/eric/repos/foo`. The web app:

1. Persists the FSA handle in IndexedDB (FSA handles can't serialize to
   localStorage).
2. Calls `POST /api/sessions {workdir, model, systemPromptPreset}`.

The daemon validates the workdir (absolute, exists, writable, not under
`~/.r1/`), generates `session_id = "sess_" + uuid.New()`, allocates a
`*Session` struct with `SessionRoot = "/home/eric/repos/foo"`, calls
`cortex.NewWorkspace(SessionRoot)`, opens
`/home/eric/repos/foo/.r1/sessions/sess_xxx/journal.ndjson`, spawns
`go session.run(ctx)`, emits `session.started` to the bus, returns
`{session_id, started_at}`.

### Frame 2 — The cortex spawns

Inside `session.run(ctx)`, the agentloop loop is constructed with a
`Cortex *cortex.Cortex` field on `agentloop.Config`. The cortex bundles
six Lobes:

| Lobe | Kind | Trigger |
|---|---|---|
| MemoryRecallLobe | Deterministic | Every turn boundary; reindex on `cortex.workspace.memory_added` |
| WALKeeperLobe | Deterministic | Every hub event (filter returns true unconditionally) |
| RuleCheckLobe | Deterministic | Every `supervisor.rule.fired` bus event |
| PlanUpdateLobe | LLM (Haiku 4.5) | Every 3rd assistant turn or on action-verb input |
| ClarifyingQLobe | LLM (Haiku 4.5) | After every user turn |
| MemoryCuratorLobe | LLM (Haiku 4.5) | Every 5 assistant turns or on `task.completed` |

`Cortex.Start(ctx)`:

1. Fires the synchronous initial cache pre-warm (`max_tokens=1`,
   `system+tools` byte-identical to the main thread, `model =
   claude-haiku-4-5`).
2. Launches the pre-warm ticker goroutine (4-min interval; 5-min TTL
   minus margin).
3. Launches each `LobeRunner.Start(ctx)`. Emits `cortex.lobe.started` for
   each. Three deterministic Lobes start unrestricted; three LLM Lobes
   acquire the `LobeSemaphore` (capacity 5, hard cap 8).

The agentloop is composed with the cortex hooks:

- `MidturnCheckFn`: cortex's `MidturnNote` runs first, operator hook
  second, joined with `\n\n`.
- `PreEndTurnCheckFn`: cortex's `PreEndTurnGate` short-circuits on any
  unresolved `SevCritical` Note.
- `OnUserInputMidTurn`: the chat REPL / web client wires this. The agent
  loop itself does not.

The session emits `session.started` to the bus; the journal writes its
genesis record. The web UI sees the `session.started` envelope, navigates
to `/sessions/sess_xxx`, and re-subscribes via WS with `since_seq: 0` —
the daemon emits the synthetic `session.bound` at `seq=0`, then a
`lane.created` for the main lane.

### Frame 3 — Eric types a message

Eric types: "Add a JWT auth middleware to handlers/auth.go using
github.com/golang-jwt/jwt/v5."

The web app's `<Composer>` validates with zod, calls
`{type:"chat", session_id, content}` over the WS. The daemon appends a
`user.input` record to the journal, emits a `session.delta` event with
`{role:"user", content}`, and the agentloop kicks off a new turn.

The agentloop sends a Messages-API request to Sonnet 4.6 with the system
prompt + tools + history + the new user message. Cache breakpoints align
with the cortex's pre-warm pump, so input tokens hit the cache at 10%
cost.

In parallel — same instant — the cortex's six Lobes all run their per-turn
checks:

**MemoryRecallLobe** (deterministic, free):

1. Builds a TF-IDF query from the last 1000 chars of message history.
2. Calls `mem.Recall(query, 5)` and `tfidf.Index.Search` over the wisdom
   corpus.
3. Dedups by `Entry.ID` / `Learning.TaskID`.
4. Publishes top-3 dedup'd matches as `Note{Severity: SevInfo, Tags:
   ["memory"]}`.

The Workspace's `Publish`:

1. Acquires `Lock`.
2. Validates the Note (non-empty `LobeID`, `Title` ≤80 runes, known
   `Severity`).
3. Assigns `ID = "note-<seq>"`, `EmittedAt = time.Now().UTC()`, `Round =
   currentRound`.
4. Appends to in-memory `notes` slice.
5. Calls `persist.writeNote(durable, n)` — JSON-marshals the Note, calls
   `bus.Bus.Publish(bus.Event{Type: "cortex.note.published", Payload:
   jsonBytes})`. **Durable before lock release.**
6. Releases `Lock`.
7. Emits `hub.Event{Type:"cortex.note.published", Custom:{"note": n}}`.
8. Calls every registered subscriber (sync, contract: <1ms each).

The web UI's lanes sidebar now shows a new lane: "MemoryRecallLobe" with
status `running`, then `done`, with three pinned Notes summarized as
"3 prior decisions referenced this Workspace shape." The TUI (if
attached) shows the same — adaptive layout depending on terminal width.

**RuleCheckLobe** (deterministic, free):

Subscribed to `bus.Pattern{TypePrefix: "supervisor.rule.fired"}`. Nothing
fires this turn — no Note emitted.

**WALKeeperLobe** (deterministic, free):

Drains every `hub.Event` into the durable WAL with structured framing
(`bus.Event{Type:"cortex.hub.<original>"}`). The WAL absorbs all of it;
backpressure stays well under the 1k-event threshold.

**PlanUpdateLobe** (Haiku 4.5):

It's the first turn — no `plan.json` to update, but the action-verb scan
("Add a JWT auth middleware") triggers it anyway. `LobePromptBuilder`
constructs a Messages-API request with:

- The verbatim PlanUpdate system prompt (`const planUpdateSystemPrompt =
  ...`), cached 1h via `cache_control: {"type":"ephemeral", "ttl":"1h"}`.
- Tools sorted alphabetically (cache stability).
- Empty plan + the user message in the un-cached block.
- `MaxTokens: 800`, `Model: claude-haiku-4-5`.

The cache pre-warm pump (fired on cortex Start and every 4 min) means
this Lobe call hits the cache for the system prompt block. Output is
structured JSON; PlanUpdateLobe parses it, decides confidence is too low
(no existing plan to update), publishes nothing.

**ClarifyingQLobe** (Haiku 4.5):

System prompt instructs it to detect "actionable ambiguity." The user
message is fully specified — file path, library, task type. The model
emits no `queue_clarifying_question` tool call and replies
`"no_ambiguity"`. No Note published.

**MemoryCuratorLobe** (Haiku 4.5):

It's only the first turn — no curation candidate exists. No Note
published.

### Frame 4 — The main thread streams a tool call

Sonnet 4.6 begins streaming. First a thinking block, then a `tool_use`
for `read_file({path: "handlers/auth.go"})`. The agentloop dispatches the
tool. Because the read takes <2s, it's NOT promoted to its own lane —
short tool calls stay inside the main lane's `lane.delta` stream.

The web UI's `<MessageBubble>` renders the thinking block in a dim
collapsible `ReasoningCard`; the tool call renders as a `ToolCard` with a
Streamdown-rendered JSON input. The lanes sidebar shows the main lane in
`running` status with the `▸` glyph.

The 200 Hz `lane.delta` events are coalesced to ≤10 Hz on the wire (per
D-S2). The web UI applies a `requestAnimationFrame` batch — diff-only
repaint. The TUI's render cache invalidates only the main lane's row,
not the whole panel.

### Frame 5 — Mid-turn redirect

Halfway through generating the new file, Eric realizes the project has
an existing auth library. He types: "actually use lib/auth/jwt.go" and
hits Enter.

The web `<Composer>` is enabled even during streaming (it sends a chat
envelope, not a synthetic-user message). The WS receives the new chat:
the daemon classifies it as mid-turn and calls
`Cortex.OnUserInputMidTurn(ctx, "actually use lib/auth/jwt.go",
turnCancel)`.

`OnUserInputMidTurn` invokes the **Router** (`cortex.Router`):

1. Builds a Messages-API request with the verbatim `DefaultRouterSystemPrompt`,
   the 4 tool defs (`interrupt`, `steer`, `queue_mission`, `just_chat`),
   the last-10-message snapshot, the new user input, and a one-line
   summary of `Workspace.Snapshot()`.
2. `model: "claude-haiku-4-5"`, `max_tokens: 1024`, `temperature: 0`,
   cached system + tools.
3. Sends. p99 ≤ 2 s.

The model returns exactly one tool call: `interrupt`, with `reason:
"user retracts library choice"` and `new_direction: "use lib/auth/jwt.go
for JWT helpers"`.

The Router emits `hub.Event{Type:"cortex.router.decided",
Custom:{kind:"interrupt", latency_ms: 1812}}`. The web UI shows a brief
toast: "Steering — interrupting current turn." The lanes sidebar shows
the Router's lane `done`.

`OnUserInputMidTurn` enacts the interrupt:

1. Calls `turnCancel()`.
2. The agentloop's `RunTurnWithInterrupt` helper sees `turnCtx.Done()`,
   drains both `respCh` and `doneCh` (mandatory — SSE reader leak
   otherwise).
3. Drops the partial assistant message entirely (Anthropic gives no
   recovery handle for incomplete `tool_use` blocks; persisting would
   400 the next API call).
4. Appends a synthetic user message to the committed history:
   `<system-interrupt source="user" severity="info">user retracts
   library choice</system-interrupt> New direction: use lib/auth/jwt.go
   for JWT helpers`.
5. Returns `(messages, StopInterrupted)`.

The agentloop kicks off a fresh turn. The new committed history ends
with the synthetic user message; the API request is `user`-terminated
and valid. The new assistant turn knows the previous direction was
retracted and the new library is `lib/auth/jwt.go`.

### Frame 6 — The new turn, with PlanUpdate firing

Sonnet 4.6 thinks, then emits a `tool_use` for
`read_file({path: "lib/auth/jwt.go"})`. The cortex's Lobes all run again:

**MemoryRecallLobe** finds nothing new (same query window).

**PlanUpdateLobe** triggers because the user retracted a direction
(action-verb scan). `LobePromptBuilder` builds the request again — same
system prompt (cache hit, ~10% input cost), same tools, the new history.
The model returns:

```json
{
  "additions": [],
  "removals": [],
  "edits": [
    {"id": "task-1", "field": "title", "new": "Add JWT middleware using lib/auth/jwt.go"}
  ],
  "confidence": 0.85,
  "rationale": "User explicitly switched library."
}
```

PlanUpdateLobe parses, confidence ≥0.6, auto-applies the edit via
`plan.Save(...)`, publishes a Note: `LobeID: "PlanUpdateLobe", Severity:
SevInfo, Tags: ["plan"], Title: "Plan updated", Body: "task-1 retitled to
'Add JWT middleware using lib/auth/jwt.go'"`.

The web UI shows a `<PlanCard>` updating live in the chat pane (`@ai-sdk/elements`
`Plan` element); the lanes sidebar shows a new `done` lane for
PlanUpdateLobe.

### Frame 7 — Pin a lane to tile mode

Eric clicks the pin icon on the PlanUpdateLobe lane in the right
sidebar. The web UI emits an `r1.lanes.pin` MCP-equivalent over the
`R1dClient.pinLane()` method. The daemon flips `Lane.Pinned = true`;
surfaces don't get a separate event (per spec 3, surfaces re-fetch
cheaply via `lanes.list`).

The PlanCard was already in the chat pane via the `useChat` part stream.
Pinning the **lane** moves it into the **TileGrid**. Eric pins one more
lane (the MemoryRecall results). The center pane switches from `<MessageLog>+<Composer>`
to `<TileGrid>` in 1×2 layout. Each tile is a `<LaneTile>` rendering
that lane's live activity.

### Frame 8 — A long tool call gets promoted to its own lane

Sonnet 4.6 issues a `bash({cmd: "go test ./..."})` to verify the new
middleware. The agentloop sees a tool call has been running for >2 s,
promotes it to its own lane via `Workspace.NewToolLane(ctx, parent,
"bash")`, emits `lane.created{kind: "tool", parent_lane_id: <main>,
label: "bash: go test ./..."}`.

The web UI's lanes sidebar shows the new tool lane with status
`running`. Eric watches its `lane.delta` events stream the test output.
A test fails. The agentloop calls a critic, which classifies the failure
(`failure.Compute`), extracts specifics, decides "instructions issue —
the new file imports `jwt/v5` but the project pins `jwt/v4`."

**RuleCheckLobe** doesn't fire (no supervisor rule matches). **The main
thread** receives the critic's diagnosis as a tool result, retracts the
import, regenerates, retests. Pass.

### Frame 9 — End-of-turn gate

Sonnet 4.6 wants to emit `end_turn`. The agentloop's `PreEndTurnCheckFn`
runs. It calls `cortex.PreEndTurnGate(messages)`:

1. Calls `Workspace.UnresolvedCritical()`.
2. Filters Notes with `Severity == SevCritical` AND no later Note has
   `Resolves == n.ID`.
3. Returns "" (no unresolved critical Notes).

Operator hook (if any) runs after. Both return "". The honeypot gate
runs: no canary leak, no markdown-image exfil, no chat-template-token
leak, no destructive shell. End-of-turn is allowed.

The agentloop emits `session.delta{role: "assistant"}` for the final
content, then `session.ended` is NOT emitted (the session continues —
only mission completion or kill ends a session).

The MemoryCuratorLobe doesn't fire (it triggers every 5 turns; we're at
turn 2). When it does fire, it'll see "user retracted jwt/v5 → jwt/v4
because project pins v4" — that's a `gotcha` category candidate. Per the
default `auto_curate_categories: [fact]`, it queues this as a
`memory-confirm` Note rather than auto-writing. Eric sees a notification
in the chat: "Remember this? 'project pins jwt/v4 — never use v5.'" He
clicks Confirm; the Lobe calls `mem.Save()` and appends an entry to
`~/.r1/cortex/curator-audit.jsonl`.

### Frame 10 — Daemon restart mid-session

Three turns later, Eric runs `r1 update`, which downloads a new binary
to `~/.r1/bin/r1` via atomic rename. He then runs `r1 serve restart`.

The old daemon receives `daemon.shutdown {grace_s: 30}`, broadcasts
`session.paused` to every active subscriber, fsyncs every journal,
exits 0. The web UI sees the WS close (code 1000), enters `reconnecting`
state, and starts exponential backoff.

The new daemon spawns. It scans `~/.r1/sessions-index.json`, finds Eric's
session at `sess_xxx`, opens
`/home/eric/repos/foo/.r1/sessions/sess_xxx/journal.ndjson`, replays
each NDJSON record into a fresh `*Session`:

1. `session.created` → allocate the session struct, set `SessionRoot`,
   `model`, `budget_usd`.
2. `user.input` and `session.delta` records → reconstruct message history.
3. `session.tool_started` / `session.tool_completed` → infer lane state.
4. `lane.delta` records → reconstruct each lane's last-known state.
5. The cortex's `Workspace.Replay(ctx)` reads `cortex.note.published`
   events from the WAL and rebuilds the in-memory Notes slice in publish
   order. The `drainedUpTo` cursor advances to `len(notes)` so the next
   turn's Drain returns nothing — resumed sessions don't re-inject stale
   notes.

The new daemon starts the WS listener, broadcasts `daemon.reloaded {at,
version: <new-sha>}`. The web UI's `ResilientSocket` reconnects (the
backoff curve: 250 ms × 2^n, capped 8 s, with jitter). On reconnect, it
sends `{type: "subscribe", session_id: "sess_xxx", lastEventId: <last
seq seen>}`. The daemon replays from the journal — assistant deltas,
lane events, cost ticks — in monotonic seq order. The chat pane fills in
the messages it missed; the lanes sidebar shows pinned lanes restored to
their pre-restart state.

Total reconnect time: ~3-5 s. No state lost. No duplicate messages.

### Frame 11 — Eric switches to the desktop app

Eric opens the Tauri 2 desktop app on the same machine. `tauri::Builder::setup`
calls `discover_or_spawn(app)`:

1. `read_daemon_json()` reads `~/.r1/daemon.json` (mode 0600, owned by
   Eric).
2. `probe_external()` tries TCP connect to `ws://127.0.0.1:7777` with 1s
   timeout. Succeeds (the new daemon).
3. Returns `DaemonHandle{mode: External, url, token}`.

The desktop's title-bar `<DaemonStatus>` renders a green dot: "Connected
(external)." The session list shows `sess_xxx`. Eric clicks it. The
desktop subscribes to lane events via
`session.lanes.subscribe({session_id, channel: tauri::ipc::Channel<LaneEvent>})`.
A per-session forwarder task `select!`s the daemon's WS frames into the
channel; the WebView receives `LaneEvent` enums (Delta / Status / Spawned
/ Killed) and renders via `<LaneSidebar>` from the shared
`packages/web-components/` package.

The exact same `LaneCard` and `LaneSidebar` components render in the web
app — that's why they live in a workspace package. Atomic refactors land
in one PR, both surfaces update together.

Eric uses `Cmd+\` to pop out the PlanUpdateLobe lane. The desktop
`app.popout_lane` Tauri command builds a `WebviewWindowBuilder` with
label `"lane:sess_xxx:planupdate"`, URL
`index.html?popout=lane&session=sess_xxx&lane=planupdate`, size 480×640.
The new window mounts `<PoppedLaneApp>` from `packages/web-components/`
and subscribes to that single lane. Eric closes the primary window; the
pop-out remains open.

### Frame 12 — Eric drives r1 with another agent

Eric writes a 6-line MCP client that wraps Claude. The client:

1. Connects to the daemon via `r1d.sock` (peer-cred check; no token
   needed).
2. Calls `r1.session.start({workdir: "/home/eric/repos/bar"})` → returns
   `session_id: "sess_yyy"`.
3. Calls `r1.session.send({session_id: "sess_yyy", message: "Run go test
   ./..."})`.
4. Calls `r1.lanes.subscribe({session_id: "sess_yyy"})` to stream lane
   events.
5. On any `lane.note{severity: "critical"}`, calls `r1.lanes.kill` on
   the offending lane and routes the failure to a human.
6. On `session.ended`, summarizes the result.

Every action this agent takes has a corresponding human action in the
web UI. The CI lint at `tools/lint-view-without-api/` would fail the
build if a UI button existed without a matching MCP tool. The web is a
view over the API; never the reverse.

## Architectural underpinnings, surfaced

A few mechanisms that make all this work:

### Cache pre-warm parity

The pre-warm pump and main thread's `buildRequest` produce byte-identical
system blocks AND tool ordering. Use `agentloop.SortToolsDeterministic`
and `agentloop.BuildCachedSystemPrompt`. A 1-byte drift = 0% cache hit +
zero cost savings. The `internal/cortex/prewarm.go` test diffs the two
byte slices to assert parity.

### Workspace persistence ordering

`Workspace.Publish` performs:

1. Append to in-memory slice **under** `Lock()`.
2. Append to durable `bus.Bus` (write-through to WAL) **before**
   releasing `Lock()`. Failure → return error, no in-memory append.
3. Emit `hub.Event{Type:"cortex.note.published"}` **after** lock release.
   Failure → log only; the Note is already durable.

Inverting any of these: emit-without-persist creates ghost events;
persist-without-lock-hold creates inconsistent state on concurrent
publish; lock-during-emit deadlocks if a subscriber calls
`Workspace.Snapshot`.

### Round superstep barrier

`Round.Open(N)` declares N participants. Each Lobe (or its runner) calls
`Done(roundID, lobeID)` once. `Wait(ctx, deadline)` blocks until all N
Done **or** deadline **or** ctx cancelled. Late Lobes are NOT cancelled
(cancelling a partway-LLM-call wastes API spend); their next Note carries
a later `Round` value. Tests assert that a slow Lobe doesn't lose its
Note — it just lands on the next round's Drain.

### Drop-partial drain ordering

On interrupt:

```
cancelTurn()       // (1) tear down stream
for range respCh{} // (2) drain remaining buffered events
<-doneCh           // (3) wait for SSE reader to exit cleanly
<-watchdogDone     // (4) watchdog exits
// (5) Drop partial entirely — never persist incomplete tool_use blocks
// (6) Append synthetic user message
```

Reverse the order — drain before cancel — and the SSE reader keeps
producing events; `respCh` may never close.

### `os.Chdir` audit + per-session sentinel

The cortex / daemon model works **only because** `cmd.Dir` is the
established pattern. One stray `os.Chdir` in any of the 132 internal
packages would silently leak workdir between concurrent sessions. The
`tools/cmd/chdir-lint/` AST walker scans every Go file for `os.Chdir`,
`os.Getwd`, and `filepath.Abs("")` calls without a `// LINT-ALLOW chdir-*:
reason` annotation. CI fails on red. Per-session sentinel runs before
each tool dispatch:

```go
expected := s.SessionRoot
got, err := os.Getwd()
if err != nil || got != expected {
    panic(fmt.Sprintf(
        "FATAL: session %s expected cwd=%s got=%s — process-global chdir leaked.",
        s.ID, expected, got))
}
```

If a stray `os.Chdir` slips past CI, the sentinel panics loudly before
tool dispatch — far better than silent cross-session contamination.

## Status

### Done
- Core mission loop (PLAN → EXECUTE → VERIFY → REVIEW → MERGE) with
  cross-model adversarial reviewer.
- Deterministic pack lifecycle, signed runtime registration, HTTP pack
  registry.
- Wave 2 R1-parity surfaces: browser, LSP, IDE plugins, multi-CI, Tauri
  R1D-1..R1D-12.
- Verification descent + honeypot gate + protected-file checks.
- 5-provider model resolver, subscription pool, prompt-injection
  hardening, red-team corpus.

### In Progress
- Hardening of the Manus-style autonomous operator behind a per-mission
  toggle.
- LSP feature coverage beyond hover/definition/diagnostics.

### Scoped
- The 12-frame narrative above describes the cortex / lanes / multi-surface
  scope. Eight specs in `specs/` define the slice.

### Scoping
- More explicit superiority and runtime-proof loops against peer runtimes.
- Cross-machine session migration.

### Potential — On Horizon
- Broader network effects from reusable deterministic skills.
- Cloud daemon beyond loopback singleton.
- OpenTelemetry export of lane events.
