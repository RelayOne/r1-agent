# RT-05: Stateless Harness + Session Log Pattern

**Date:** 2026-04-20
**Target package:** `internal/eventlog/` (Stoke)
**Status:** Research complete — recommendation at end.

## 1. Anthropic Managed Agents architecture (April 2026)

Anthropic's "Scaling Managed Agents: Decoupling the brain from the hands"
(2026-04-08) introduces a three-way split:

- **Session** — a durable, append-only event log that lives outside every
  component and outlives them. "Single source of truth for everything that
  happens."
- **Harness** — stateless orchestrator. Calls the Claude API, routes tool
  calls, writes events back into the session. Any harness instance can pick
  up any session ID and continue from where the previous one died.
- **Sandbox** — disposable container. Provisioned only when the brain calls
  a tool that needs one; sessions that finish via pure reasoning or MCP
  never wait for a container. This is why their p50 TTFT dropped ~60% and
  p95 >90%.

### Event types (from the Managed Agents API)

Five documented event types in the session log:

| type               | payload                                             |
|--------------------|-----------------------------------------------------|
| `user_message`     | Initial task + mid-session human input              |
| `assistant_message`| Claude response incl. thinking blocks               |
| `tool_call`        | `{name, input}` issued by Claude                    |
| `tool_result`      | String (or structured) return from the sandbox      |
| `context_reset`    | Compaction checkpoint: summary + cut-point          |

### Documented SDK surface

```
getSession(id)       → Event[]
emitEvent(id, event) → void
getEvents(opts)      → Event[]          // positional slice, rewind
wake(sessionId)      → HarnessHandle
provision({...})     → SandboxHandle
execute(name, input) → string
```

`getEvents` is deliberately positional so the brain can "rewind a few events
before a specific moment to see the lead-up" — replay is range-scan, not
fold-the-whole-history.

### Replay / context reconstruction

On harness crash: `wake(sessionId)` spawns a fresh harness, which calls
`getSession(id)`, reconstructs the Claude context window from the event list
(respecting `context_reset` markers so it only rehydrates since the last
compaction), and continues. Nothing in the harness survives a crash because
nothing in the harness *needs* to. Compaction is first-class: the harness
writes a `context_reset` event whose payload is the summary, and future
reconstructions stop at that marker.

## 2. Prior art

**Temporal** (durable execution). Event History + deterministic replay.
Workers are evicted from cache and rehydrated by replaying history; a
Command sequence mismatch during replay (non-determinism) halts the
workflow. Network / clock / random calls must go through Temporal's APIs so
they are recorded once and read from history on replay. Same shape as
Managed Agents: history is authoritative, worker is fungible.

**Erlang/OTP.** `gen_server` crashes lose in-memory state by design;
recovery is via a supervision tree restart *plus* external state (ETS,
Mnesia, disk, a log). The same split Anthropic arrived at: supervisor
restarts the process, durable store restores the state. The lesson is that
"stateless worker + durable state" is well-trodden 40-year territory.

**Event sourcing / CQRS.** The log is the system of record; projections
(e.g. a SQLite read-model) are rebuildable from the log. If you delete the
cache, nothing is lost — you rebuild from the JSONL (see
paperclipai/paperclip RFC #801 and SoftwareMill's event-sourcing guide).
This is the pattern for Stoke because it preserves a human-readable audit
trail alongside indexed query.

**Git.** Content-addressed append-only object store. Stoke already uses
this shape (`contentid/`, `ledger/`) so the primitives exist.

## 3. Storage format trade-offs

| Option              | Replay speed | Concurrent writers | Inspect | Crash recovery |
|---------------------|--------------|--------------------|---------|-----------------|
| Single JSONL file   | Linear scan  | `O_APPEND` atomic up to `PIPE_BUF` (4 KiB) per write, then races | `jq`, `tail -f` | Last partial line; trivial to repair (seek to last `\n`) |
| SQLite (WAL)        | Indexed      | **Single writer** serialized (WAL is itself append-only); unlimited readers | `sqlite3` CLI | Strong — WAL has atomic commit |
| Per-session numbered JSON files | Random-access | One writer per dir | `ls`, `cat` | Fine-grained but fsync-per-event is expensive |

Key facts confirmed:

- SQLite WAL permits unlimited readers + one writer; writes are fully
  serialized via the WAL file. For event-log workloads this is *not* a
  bottleneck — our write rate is << 100/s.
- JSONL `write(2)` with `O_APPEND` is atomic only up to `PIPE_BUF`. Beyond
  that (e.g. long tool results) two concurrent appenders can interleave
  bytes. If we pick JSONL we must serialise writes inside the process or
  per file.
- Hybrid pattern (JSONL source-of-truth + SQLite read cache) is popular
  (sqliteforum.com event-sourcing thread, paperclip RFC). Simpler operation
  than a pure-SQLite store because you can always `rm cache.db && rebuild`.

## 4. Critical design questions

**Resumability minimum.** To fully resume, log: every model prompt (inputs
+ system + tool schema), every `tool_call`, every `tool_result`, every
`assistant_message`, every compaction marker, every external side-effect we
care about (verify tier results, worktree merge SHAs, mission
state-transitions). Enough that re-running the harness from `t=0` only
re-reads history until the last event, then issues exactly one new API
call.

**Tool call / tool result atomicity.** They are *not* a single
transaction — the sandbox could die between them. Two-step protocol:

1. Write `tool_call` with a client-generated `call_id` before dispatch.
2. Write `tool_result` (same `call_id`) on return.

On wake: scan for `tool_call` with no matching `tool_result`. Two recovery
strategies — (a) re-dispatch if the tool declares itself idempotent
(`GET` / pure compute / git read); (b) emit a synthetic
`tool_result{status:"unknown", error:"harness_crashed"}` and let the brain
decide. Stoke should prefer (b) for any worktree-mutating tool.

**Compaction.** Mirror Anthropic: emit `context_reset{summary, cut_before}`
events. Reconstruction walks the log backwards until it hits the most
recent `context_reset`, then forward. Older events stay on disk for audit
but are not rehydrated into the window.

**Branching / speculative execution.** Stoke already has `specexec/`
(4 parallel strategies, pick winner) and `branch/` (conversation
branching). Need `branch_id` on every event; a `branch.fork{parent, id}`
event opens a branch, `branch.merge{winner_id}` closes them. Replay is
parameterised by branch_id — you walk only events with a matching id or
an ancestor id. Losers remain on disk for post-mortem.

## 5. Concurrency

Recommendation: **one SQLite DB, one `events` table, `session_id` column**.
Reasons:

- Cross-session foreign keys are trivial (e.g. a child mission references
  its parent's `session_id`). With file-per-session we'd need path
  conventions.
- Stoke already mixes `session.SessionStore` JSON + SQLite (design decision
  19 in `CLAUDE.md`); adding another per-session file is fragmentation.
- Single-writer WAL is fine because Stoke serialises through an
  in-process event bus anyway (`bus/`).

For cross-process safety (e.g. the CLI and a running mission server), use
WAL + `busy_timeout=5000` + retries. SkyPilot's "Abusing SQLite" post
documents this working well past our load.

## 6. Replay correctness

**Non-determinism.** LLM calls, network fetch, `time.Now()`, `rand` — any
output that enters the log must be recorded on first execution and *read*
from the log on replay. Implementation: every side-effectful function is
wrapped:

```go
func (l *Log) Do(ctx, key string, fn func() (Result, error)) (Result, error) {
    if ev, ok := l.find(key); ok { return ev.Result, nil } // replay hit
    r, err := fn()
    l.emit(Event{Kind: "side_effect", Key: key, Result: r, Err: err})
    return r, err
}
```

`key` is stable — `tool_call:<call_id>` or `llm_turn:<turn_ix>`.
Temporal calls this the "SideEffect" / "Activity" boundary; same idea.

**Corruption.** WAL gives atomic commit — torn writes can't appear in the
table, only the last uncommitted transaction is lost. If we also mirror to
JSONL (optional, behind a flag), the recovery rule is: reject the trailing
line if it doesn't parse; SQLite is authoritative.

## 7. Recommendation for `internal/eventlog/`

### Schema

```go
type Event struct {
    ID        string          // ULID, sortable
    TS        time.Time       // UTC, nanosecond
    SessionID string          // mission / task scope
    BranchID  string          // "main" unless forked
    Type      string          // see table below
    CallID    string          // for tool_call / tool_result pairs
    ParentID  string          // causality chain (optional)
    Data      json.RawMessage // type-specific payload
    Hash      string          // SHA256(prev.Hash || canonical(this)) — tamper seal
}
```

### Storage

**SQLite, WAL mode, single `events` table**, columns mirroring the struct,
indexes on `(session_id, id)` and `(session_id, type)`. Reasons: indexed
slice queries (Anthropic's `getEvents(opts)` needs positional range + type
filter), cross-session joins, atomic commit, already a pattern in Stoke
(`session.SQLStore`). Optional JSONL mirror for `jq`-friendly inspection
gated on `STOKE_EVENTLOG_JSONL=1`.

### Initial event types

| Type                   | Purpose                                      |
|------------------------|----------------------------------------------|
| `session.start`        | Mission / task boot, config snapshot         |
| `task.dispatch`        | Scheduler picks task, GRPW score, worktree  |
| `llm.turn`             | Prompt + response + tokens + cost           |
| `tool.call`            | `{name, input, call_id}`                     |
| `tool.result`          | `{call_id, output, err, duration}`           |
| `verify.tier`          | Build/test/lint results per tier            |
| `worktree.merge`       | `{base_sha, merged_sha}`                     |
| `context.compact`      | Summary + cut marker                         |
| `branch.fork` / `.merge` | specexec + conversation branching          |
| `failure`              | Class + fingerprint (tie to `failure/`)      |
| `session.end`          | Exit reason, totals                          |

### API (mirrors Anthropic's surface)

```go
Emit(ctx, ev Event) error
Get(ctx, sessionID string, opts GetOpts) ([]Event, error) // positional slice
Wake(ctx, sessionID string) (Cursor, error)               // last event seen
Do(ctx, key string, fn func() (Result,error)) (Result, error) // record-once
```

### Replay algorithm

1. `Wake(sessionID)` → scan `context.compact DESC LIMIT 1`; start there or
   at `session.start` if none.
2. Forward scan, rebuild LLM message list and tool-call map.
3. For each `tool.call` without matching `tool.result`: policy table —
   idempotent → retry; side-effectful → synthesise
   `{status:"unknown"}` and continue.
4. Hand Claude the reconstructed window; issue one new API call; `Emit`
   the response.
5. Loop.

### Guard-rails

- **Append-only at the type level.** `Emit` only. No `Update` / `Delete`.
  (Historical repair via a compensating event.)
- **Hash chain** so tampering shows up in CI (`Hash = SHA256(prev || canon)`).
- **Canonical JSON** (sorted keys, fixed number format) for the hash.
- **Cross-link to `ledger/`.** Stoke already has a content-addressed ledger
  for governance state; `eventlog` is the transient per-session stream,
  `ledger/` is the durable graph. `Event.Data` may reference ledger
  `NodeID`s — do not duplicate.

## Sources

- [Scaling Managed Agents — Anthropic Engineering](https://www.anthropic.com/engineering/managed-agents)
- [Claude Managed Agents overview — platform.claude.com](https://platform.claude.com/docs/en/managed-agents/overview)
- [Anthropic Managed Agents Architecture (dev.to writeup)](https://dev.to/_46ea277e677b888e0cd13/anthropic-managed-agents-architecture-decoupling-brain-from-hands-for-scalable-ai-agents-295k)
- [How Anthropic Scales Managed Agents (Ken Huang)](https://kenhuangus.substack.com/p/how-anthropic-scaling-managed-agents)
- [Context Compaction API — Claude docs](https://platform.claude.com/docs/en/build-with-claude/compaction)
- [Temporal: Durable Execution tutorial](https://learn.temporal.io/tutorials/go/background-check/durable-execution/)
- [Temporal Event History walkthrough (Go SDK)](https://docs.temporal.io/encyclopedia/event-history/event-history-go)
- [SQLite Write-Ahead Logging](https://www.sqlite.org/wal.html)
- [How SQLite Scales Read Concurrency — Fly Blog](https://fly.io/blog/sqlite-internals-wal/)
- [Event Sourcing with SQLite — sqliteforum](https://www.sqliteforum.com/p/event-sourcing-with-sqlite)
- [Implementing event sourcing using a relational database — SoftwareMill](https://softwaremill.com/implementing-event-sourcing-using-a-relational-database/)
- [GenServer state recovery after crash — Bounga](https://www.bounga.org/elixir/2020/02/29/genserver-supervision-tree-and-state-recovery-after-crash/)
- [Supervisors — Learn You Some Erlang](https://learnyousomeerlang.com/supervisors)
