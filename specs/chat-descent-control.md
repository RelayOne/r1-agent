<!-- STATUS: done -->
<!-- BUILD_COMPLETED: 2026-04-22 -->
<!-- CREATED: 2026-04-20 -->
<!-- DEPENDS_ON: spec-1 (descent), spec-2 (events, HITL), spec-3 (executor), spec-7 (Operator interface) -->
<!-- BUILD_ORDER: 11 -->

# Chat Descent Control — Implementation Spec

## Overview

Two orthogonal surfaces land together because they share the same running-session IPC:

1. **Chat mini-descent.** Per work.md §5.1 verbatim requirement, every code change the agent makes during a `stoke chat` turn must run through the verification descent ladder (spec-1) before the reply is rendered. The chat surface is already FULLY WIRED (RT-STOKE-SURFACE §6 — `internal/chat/session.go`, `dispatcher.go`, `intent.go`), but descent is not invoked on ad-hoc file writes initiated by chat. This spec adds `internal/chat/descent_gate.go`, which detects diffs vs the session-start commit and runs a trimmed descent (`MaxCodeRepairs=1`, multi-analyst off, soft-pass=ask_operator), inlining the verdict into the chat reply via `Operator.Notify` / `Operator.Ask`.
2. **Operator control plane.** A new `internal/sessionctl/` package exposes a Unix-socket JSON-RPC over which the operator CLI verbs (`status`, `approve`, `override`, `budget`, `pause`, `resume`, `inject`, `takeover`) drive a running `stoke run` / `stoke ship` / `stoke chat` process. Every command emits an `operator.*` bus event, persists to the event log (spec-3), and mirrors to streamjson (spec-2). Three previously-distinct approval paths (CloudSwarm HITL stdin, terminal Operator.Ask, external `stoke approve`) converge on one decision channel.

Nothing in spec-1, spec-2, spec-3, or spec-7 is modified; this spec wires them together.

## Stack & Versions

- Go 1.22+
- Unix domain sockets via `net.ListenUnix`; optional HTTP+JSON over `net/http` when `--listen :PORT`
- SQLite WAL event log (`internal/eventlog/` from spec-3)
- Existing bus: `internal/bus/bus.go`
- Chat: `internal/chat/` (session.go, dispatcher.go, intent.go)
- Descent engine: `internal/plan/verification_descent.go` (spec-1)
- Operator interface: `internal/operator/` (spec-7)
- HITL reader: `internal/hitl/` (spec-2)
- `os.Stdout` fd for PTY in takeover mode (stdlib `os/exec` + `golang.org/x/term` — already vendored via bubbletea)

## Existing Patterns to Follow

- Chat session entry: `cmd/stoke/chat.go:398` (`buildChatSession`), `:431` (`chatOnceREPL`), `:485` (`chatOnceShell`); `cmd/stoke/main.go:5279` (`launchREPL`), `:5571` (`launchShell`).
- Dispatcher interface + intent extraction: `internal/chat/dispatcher.go`, `internal/chat/intent.go`.
- Descent entry point: `plan.VerificationDescent(ctx, ac, cfg)` — reuse verbatim.
- `DescentConfig` fields (spec-1): `MaxCodeRepairs`, `MaxRepairsPerFile`, `MultiAnalystEnabled`, `SoftPassPolicy`, `Operator`, `OnLog`.
- Bus publish + eventlog append helper: `eventlog.EmitBus(bus.Publisher, Log, Event)` (spec-3).
- Streamjson mirror: `streamjson.EmitSystem(subtype, data)` (spec-2 — extend, do not fork).
- HITL NDJSON stdin reader: `internal/hitl/reader.go` (spec-2) — reused by sessionctl approve-routing.
- Process-group isolation: `Setpgid: true` + `killProcessGroup` pattern (codebase design decision #7) — takeover SIGSTOPs the agent's PGID and SIGCONTs on release.
- Unix socket safe perms: `os.Chmod(path, 0600)` after `net.Listen("unix", path)`.

## Library Preferences

- JSON RPC: stdlib `encoding/json` with newline-delimited framing (matches CloudSwarm supervisor shape, RT-CLOUDSWARM-MAP §1). No gRPC, no JSON-RPC 2.0 envelope (keep it simple and greppable).
- Auth token (HTTP mode only): `STOKE_CTL_TOKEN` env var; compare with `subtle.ConstantTimeCompare`.
- PTY for takeover: `github.com/creack/pty` if already vendored — otherwise fall back to `os/exec` with `cmd.Stdin = os.Stdin` and raw-mode toggles via `x/term`.
- Process signaling: stdlib `syscall.Kill(-pgid, SIGSTOP)` / `SIGCONT`.
- Socket discovery glob: stdlib `filepath.Glob("/tmp/stoke-*.sock")`.

## Data Models

### `sessionctl.Request`

| Field       | Type                | Constraints                                    | Default |
|-------------|---------------------|------------------------------------------------|---------|
| `Verb`      | string              | one of the verb table below                    | required |
| `RequestID` | string              | ULID, idempotency key                          | minted on client |
| `Payload`   | `json.RawMessage`   | verb-specific, validated against schema        | `{}` |
| `Token`     | string              | required for HTTP mode; empty for Unix socket | `""` |

### `sessionctl.Response`

| Field      | Type               | Constraints                                   | Default |
|------------|--------------------|-----------------------------------------------|---------|
| `RequestID`| string             | echoes request                                | required |
| `OK`       | bool               | true on success                               | required |
| `Data`     | `json.RawMessage`  | verb-specific                                 | `{}` |
| `Error`    | string             | empty when `OK=true`                          | `""` |
| `EventID`  | string             | ULID from `eventlog.Append` when mutating     | `""` |

### Verb table (Unix socket RPC + HTTP `POST /ctl`)

| Verb                | Payload (JSON)                                                     | Response.Data                                        | Effect |
|---------------------|--------------------------------------------------------------------|------------------------------------------------------|--------|
| `status`            | `{}`                                                               | `{state, plan_id, tasks:[...], cost, budget, paused}`| read-only |
| `approve`           | `{approval_id?, decision, reason?}`                                | `{matched_ask_id}`                                   | resolves a pending `Operator.Ask` / HITL ask; emits `operator.approve` |
| `override`          | `{ac_id, reason}`                                                  | `{ac_id}`                                            | force-marks an AC passed; emits `operator.override` |
| `budget_add`        | `{delta_usd, dry_run?}`                                            | `{prev_budget, new_budget}`                          | raises cost cap; emits `operator.budget_change` |
| `pause`             | `{}`                                                               | `{paused_at}`                                        | SIGSTOPs the session's process group; emits `operator.pause` |
| `resume`            | `{}`                                                               | `{resumed_at}`                                       | SIGCONTs; emits `operator.resume` |
| `inject`            | `{text, priority?}`                                                | `{task_id}`                                          | appends a task to the session queue; emits `operator.inject` |
| `takeover_request`  | `{reason?, max_duration_s?}`                                       | `{takeover_id, pty_path}`                            | pauses agent, reserves a PTY; emits `operator.takeover_start` |
| `takeover_release`  | `{takeover_id}`                                                    | `{released_at, diff_summary}`                        | resumes agent, triggers re-verify; emits `operator.takeover_end` |

### `operator.*` event payloads (persisted to event log + streamjson)

| Event kind               | Data fields                                                                 |
|--------------------------|-----------------------------------------------------------------------------|
| `operator.approve`       | `{session_id, ask_id, approval_id, decision, reason, actor}`                |
| `operator.override`      | `{session_id, ac_id, reason, actor}`                                        |
| `operator.budget_change` | `{session_id, prev_usd, new_usd, delta_usd, actor, dry_run}`                |
| `operator.pause`         | `{session_id, actor}`                                                       |
| `operator.resume`        | `{session_id, actor, paused_duration_ms}`                                   |
| `operator.inject`        | `{session_id, task_id, text, priority, actor}`                              |
| `operator.takeover_start`| `{session_id, takeover_id, reason, pty_path, max_duration_s, actor}`        |
| `operator.takeover_end`  | `{session_id, takeover_id, released_at, diff_summary, actor}`               |

All events mirror to streamjson as `subtype:"stoke.operator.<kind>"` (C1: extend streamjson, do not fork).

### `chat.DescentGate`

```go
// internal/chat/descent_gate.go
package chat

type DescentGate struct {
    Repo        string                      // session cwd (git repo root)
    StartCommit string                      // SHA captured at session open
    Bus         bus.Publisher
    EventLog    eventlog.Log
    Operator    operator.Operator
    Engine      func(context.Context, plan.AcceptanceCriterion, plan.DescentConfig) plan.DescentResult
}

// ShouldFire returns true iff the turn dirtied any source file under Repo.
func (g *DescentGate) ShouldFire(ctx context.Context) (bool, []string, error)

// Run builds mini-descent ACs for the touched files and invokes Engine with the
// trimmed DescentConfig (§Mini-descent config trimming). Returns a chat-ready
// verdict struct that the chat session renders inline.
func (g *DescentGate) Run(ctx context.Context, changed []string) (ChatVerdict, error)
```

`ChatVerdict` carries an ordered list of AC outcomes (build, test, typecheck) plus a failure class that drives the retry/accept/edit Ask widget.

## Part 1 — Chat mini-descent flow

### 1.1 Trigger logic (when does descent fire)

At session start, `buildChatSession` captures `startCommit := git rev-parse HEAD` (shell out; empty SHA → skip the gate entirely with a one-line warning — no-git environments are valid).

After **every** chat turn that returns from the agentloop, the chat dispatcher (around `chatOnceREPL` / `chatOnceShell`) calls `DescentGate.ShouldFire(ctx)`:

1. Run `git status --porcelain` in `Repo`. If empty → no fire.
2. Run `git diff --name-only HEAD -- .` (tracked edits) + list untracked files. Union = `touched`.
3. Filter to "source" extensions AND common config/test files:
   - Source allowlist: `.go, .ts, .tsx, .js, .jsx, .py, .rs, .java, .kt, .rb, .php, .cs, .swift, .c, .cc, .cpp, .h, .hpp, .m, .mm`.
   - Config allowlist (triggers dependency rebuild): `package.json, pnpm-lock.yaml, go.mod, go.sum, Cargo.toml, Cargo.lock, requirements.txt, pyproject.toml, uv.lock, poetry.lock`.
   - Test allowlist: `*_test.go, *.test.ts, *.test.js, *.spec.ts, *.spec.js, test_*.py, *_spec.rb`.
   - Explicit skip: `*.md, *.txt, *.gitignore, LICENSE*, docs/**, .claude/**, .stoke/**, specs/**, plans/**`.
4. If `len(touched_filtered) == 0` after filtering → no fire (pure prose/docs turn).
5. Otherwise fire.

**Rationale:** the gate is aggressive — one source file write triggers it — but deterministic. Markdown/spec-only turns and internal-state edits (`.claude/*`, `.stoke/*`) never fire to avoid descent loops on log/cache writes.

### 1.2 Mini-descent config trimming (why the defaults change)

Chat is interactive. A full 8-tier descent (multi-analyst + 3 repairs + env fix + AC rewrite) can take 5-10 minutes and cost $1+. That is the right shape for `stoke ship`; it is the wrong shape when the operator is waiting for the prompt to return. The gate therefore constructs a trimmed `DescentConfig`:

```go
cfg := plan.DescentConfig{
    MaxCodeRepairs:       1,                       // one T4 attempt only
    MaxRepairsPerFile:    1,                       // same cap
    MultiAnalystEnabled:  false,                   // deterministic T3 only
    SoftPassPolicy:       plan.SoftPassInteractive,// route to Operator.Ask
    Operator:             g.Operator,              // terminal in chat
    OnLog:                g.onLog,                 // stream to chat reply
    RepairFunc:           g.chatRepairFunc,        // invokes a short agentloop
    EnvFixFunc:           g.chatEnvFixFunc,        // reused from app
    PreEndTurnCheckFn:    nil,                     // irrelevant in chat
}
```

Each knob maps to a concrete cost/latency outcome:

| Knob                      | Chat value                | Why |
|---------------------------|---------------------------|-----|
| `MaxCodeRepairs`          | `1`                       | one retry is usable guidance; more burns operator time and budget. |
| `MaxRepairsPerFile`       | `1`                       | per-file cap matches per-AC cap in a 1-AC chat run. |
| `MultiAnalystEnabled`     | `false`                   | avoids the 5-LLM T3 panel (~$0.10, ~30s); deterministic classification is enough for chat. |
| `SoftPassPolicy`          | `SoftPassInteractive`     | soft-pass always escalates to `Operator.Ask`; the operator is literally waiting. |
| `Operator`                | terminal operator         | reuses spec-7 terminal surface; Notify streams lines inline. |
| `OnLog`                   | chat-reply streamer       | every descent log line becomes a bulleted line under the tool-call output. |

The engine itself is unmodified; only these field values differ from the `stoke ship` call site.

### 1.3 Synthetic acceptance-criteria construction

`DescentGate.Run` builds ACs from the touched file set. The builder is skill-aware (reuses `internal/skillselect/` detection already present in the repo):

- **Go** (`*.go` touched): `AC{ID:"chat.build", Command:"go build ./..."}`, `AC{ID:"chat.vet", Command:"go vet ./..."}`, `AC{ID:"chat.test", Command:"go test ./... -count=1 -timeout=2m -run=."}` — narrowed to affected packages via `testselect.BuildGraph()` when available.
- **TS/JS** (`*.ts|*.tsx|*.js|*.jsx`): `AC{ID:"chat.build", Command:"pnpm build"}` (or `npm run build` if no pnpm), `AC{ID:"chat.typecheck", Command:"pnpm tsc --noEmit"}` (if tsconfig.json exists), `AC{ID:"chat.test", Command:"pnpm test -- --run"}` (if package.json test script).
- **Python** (`*.py`): `AC{ID:"chat.test", Command:"pytest -q"}` (if pytest config) else skip.
- **Rust**: `AC{ID:"chat.build", Command:"cargo check"}`, `AC{ID:"chat.test", Command:"cargo test"}`.
- **Dep manifest only** (`package.json` without TS/JS code): trigger install first: `AC{ID:"chat.install", Command:"pnpm install --frozen-lockfile"}` then rerun previous build ACs.

When `testselect` returns empty affected set → fall back to full `go test ./...`. When a build tool is missing → the AC is skipped and a Notify warns "no build tool for touched files; descent limited to exists-check".

### 1.4 Verbatim chat-session output format

Normal pass:

```
> add a /health endpoint that returns { status: "ok" }

Creating health endpoint...
  ✓ File created: src/routes/health.ts
  ✓ Build passes (tsc --noEmit: exit 0)
  ✓ Test passes (GET /health returns 200)
Done. 3 files modified.
```

Repair triggered:

```
> add rate limiting to all API routes

Implementing rate limiter...
  ✓ File created: src/middleware/rateLimit.ts
  ✗ Build fails: Cannot find module 'express-rate-limit'
  → Installing dependency...
  ✓ Build passes after install
  ✓ Tests pass
Done. 4 files modified, 1 dependency added.
```

Soft-pass / unresolved after one repair → Ask widget:

```
> refactor the cache layer

Refactoring cache...
  ✓ File modified: src/cache/store.ts
  ✗ Test fails: TestCache_Eviction (timeout after 30s)
  → Retrying once...
  ✗ Test still failing after 1 repair attempt

The agent cannot resolve this automatically. What would you like?
  [retry]        try again (burns ~$0.20, ~1 minute)
  [accept-as-is] keep the file as modified, mark chat turn complete
  [edit-prompt]  abandon this turn and let me restate the request
?
```

The three labels are the `operator.Option.Label` strings passed to `Operator.Ask`. The reply either triggers one more descent round (retry), returns success with a `soft_pass:true` flag on the chat turn (accept-as-is), or rolls back the worktree edits via `git checkout -- <touched>` (edit-prompt).

### 1.5 Dispatcher integration seam

`internal/chat/session.go` gains one new call site, placed AFTER the agentloop returns and BEFORE the user-visible reply is flushed:

```go
if gate != nil {
    fire, changed, _ := gate.ShouldFire(ctx)
    if fire {
        verdict, err := gate.Run(ctx, changed)
        session.renderDescentVerdict(verdict, err) // emits lines per §1.4
    }
}
```

`renderDescentVerdict` appends to the assistant message buffer that is about to be returned to the user's TTY/stream — it does not open a second round-trip to the model. If the operator picks `retry` in the Ask, the gate re-runs inline; if it picks `edit-prompt`, the session discards the assistant draft and re-enters the user-input state.

## Part 2 — Operator control commands (CLI surface)

New entries under `cmd/stoke/`: `ctl_status.go`, `ctl_approve.go`, `ctl_override.go`, `ctl_budget.go`, `ctl_pause.go`, `ctl_resume.go`, `ctl_inject.go`, `ctl_takeover.go`. Each is a thin CLI wrapper that:

1. Resolves the target session:
   - `status` with no arg → discover all `/tmp/stoke-*.sock`, query each, aggregate.
   - All other verbs → require `<session_id>` positional arg; construct `/tmp/stoke-<session_id>.sock` (or `http://127.0.0.1:<port>/ctl` if `--ctl-url` set).
2. Marshals the verb payload.
3. Writes one NDJSON line to the socket; reads one NDJSON line back.
4. Renders response to stdout (human-readable for TTY, raw JSON when `--json`).
5. Returns exit code 0 on `OK=true`, 1 otherwise.

### 2.1 `stoke status`

With no argument, discovers all sockets and renders one row per running session:

```
SESSION     MODE        STATE       PLAN            TASK (IN-FLIGHT)                COST      BUDGET   PAUSED
ses_4k2f    chat        executing   -               chat-turn (descent T4)          $0.42     $5.00    no
ses_9p8q    ship        executing   pln_a1b2c3d4    S2.T3 descent-hardening         $1.87     $4.00    no
ses_x1y2    chat        waiting     -               operator-ask: soft-pass?        $0.18     $5.00    yes
```

Columns read from `status` response `Data`: `{state, mode, plan_id, task:{id,title,phase}, cost_usd, budget_usd, paused}`. `state ∈ {idle, executing, waiting, paused, done, crashed}`.

With `<session_id>` arg, expands to full detail (task tree, recent events, open Asks).

### 2.2 Individual commands

```
stoke status [<session_id>] [--json]
stoke approve <session_id> [--approval-id ID] [--decision yes|no] [--reason STR]
stoke override <session_id> <ac_id> [--reason STR]
stoke budget <session_id> --add USD [--dry-run]
stoke pause <session_id>
stoke resume <session_id>
stoke inject <session_id> "text of the new requirement"
stoke takeover <session_id> [--reason STR] [--max-duration 10m]
```

`--approval-id` is optional: when omitted, `approve` matches the most recent open Ask. `--decision` defaults to `yes` (matches the chat/terminal "press enter" ergonomics); explicit `no` sends a rejection that is surfaced to the session as `Option.Label:"no"`.

`takeover` opens an interactive PTY session bound to the running agent's stdin/stdout; see Part 5.

## Part 3 — Running-session IPC server (`internal/sessionctl/`)

### 3.1 Server bootstrap

Every `stoke run` / `stoke ship` / `stoke chat` invocation calls `sessionctl.StartServer(sessionID, opts)` before entering its main loop:

```go
srv, err := sessionctl.StartServer(sess.ID, sessionctl.Opts{
    SocketDir: "/tmp",        // override via STOKE_CTL_DIR
    HTTPAddr:  opts.Listen,    // "" = socket only; ":9100" = HTTP on + socket
    Bus:       appBus,
    EventLog:  appEventLog,
    Operator:  appOperator,
    CostTrack: appCostTrack,
    Scheduler: appScheduler,
    Signaler:  newPGIDSignaler(os.Getpid()),
})
defer srv.Close()
```

The server:
- Creates `/tmp/stoke-<session_id>.sock` with mode `0600` (owner only).
- Registers verb handlers (table below).
- If `HTTPAddr != ""`, also listens HTTP `POST /ctl` with bearer auth (`Authorization: Bearer $STOKE_CTL_TOKEN`).
- Rotates/prunes stale socket files on startup (`os.Remove` any existing path — single-writer-per-session).
- Unlinks the socket file on clean shutdown.

### 3.2 Wire protocol

Newline-delimited JSON, one request per connection by default, with optional keep-alive (`"keep_alive": true` in request). Matches CloudSwarm supervisor (RT-CLOUDSWARM-MAP §1).

Request:
```json
{"verb":"status","request_id":"01HW5...","payload":{}}
```
Response:
```json
{"request_id":"01HW5...","ok":true,"data":{"state":"executing",...},"error":"","event_id":""}
```

Timeouts: client-side 5s default for read-only verbs (`status`), 30s for mutating verbs, configurable via `--ctl-timeout`.

### 3.3 Verb handler contract

Each handler in `internal/sessionctl/handlers.go`:

1. Validates payload (stdlib `json.Unmarshal` into per-verb struct; reject unknown fields via `dec.DisallowUnknownFields()`).
2. For HTTP requests, verifies `STOKE_CTL_TOKEN` with `subtle.ConstantTimeCompare`.
3. Executes the mutation (bus publish, eventlog append, scheduler/cost/operator calls).
4. Returns a `Response` with `EventID` when the operation appended to the event log.

Handlers MUST NOT block indefinitely. Long-running side effects (takeover PTY, pause→resume roundtrip) return immediately with a handle; the client polls `status` for completion.

### 3.4 Discovery

`stoke status` without args:
```go
matches, _ := filepath.Glob(filepath.Join(ctlDir, "stoke-*.sock"))
for _, sock := range matches {
    resp, err := clientOnceNDJSON(sock, Request{Verb: "status"})
    // merge resp.Data into aggregate table
}
```

Dead sockets (file exists but `connect` refused `ECONNREFUSED`) are pruned automatically (`os.Remove`) — the owning process crashed. A warning is printed once per `stoke status` invocation.

## Part 4 — Event log + streamjson mutation

Every successful verb handler calls the spec-3 helper:

```go
eventlog.EmitBus(s.Bus, s.EventLog, eventlog.Event{
    SessionID: sess.ID,
    Type:      "operator.approve",   // or whichever verb
    Data:      marshalJSON(payload),
    ParentID:  parentEventID,         // threads back to the Ask event when applicable
})
```

Three consequences:

1. **Bus subscribers** (costtrack, scheduler, operator queue) react in-process.
2. **Event log persists** to SQLite — later audits, replays, and `stoke status` history queries are served from this table.
3. **Streamjson mirror** — a subscriber registered at sessionctl startup translates `operator.*` bus events into `emitter.EmitSystem(subtype:"stoke.operator.<verb>", data:...)` for CloudSwarm consumers (spec-2 C1).

### 4.1 Audit query

The audit SQL that the AC uses to verify persistence:

```sql
SELECT COUNT(*) FROM events WHERE type LIKE 'operator.%';
```

After even one `stoke approve` cycle this must be ≥ 1.

## Part 5 — Takeover mode

### 5.1 Lifecycle

```
1. operator:  stoke takeover ses_xyz
2. ctl:       handler validates, captures session PGID
3. ctl:       syscall.Kill(-pgid, SIGSTOP)    — agent pauses on next syscall
4. ctl:       emits operator.takeover_start (event_id = ev_A)
5. ctl:       allocates PTY, returns {takeover_id:"tko_01HW...", pty_path:"/tmp/stoke-tko-01HW....pts"}
6. CLI:       attaches the current terminal to the PTY (raw mode on, restore on exit)
7. operator:  drives bash / browser / file edits as themselves
8. operator:  types `exit` OR hits Ctrl-D OR hits max-duration timer
9. CLI:       sends takeover_release{takeover_id} — server:
                 a. computes diff vs pre-takeover SHA (git diff --stat)
                 b. SIGCONT to agent PGID
                 c. injects a user-role reminder into the running task:
                    "Operator made changes during takeover; the following files were
                     modified: X, Y, Z. Re-running verification against the new state."
                 d. enqueues a descent re-verify (spec-1 plan.VerificationDescent over
                    current ACs)
                 e. emits operator.takeover_end (event_id = ev_B, parent_id = ev_A)
10. agent:    on next turn sees reminder + fresh git state; descent confirms pass/fail
```

### 5.2 Constraints

- Only one takeover per session at a time. A second `takeover_request` while another is active returns `OK=false, error:"takeover already active"`.
- `max_duration_s` default 600 (10 min). On expiry, the server auto-releases, emits `operator.takeover_end` with `reason:"timeout"`, SIGCONTs the agent.
- PTY cleanup on crash: systemd/bubbletea-like deferred `os.Remove` of `pty_path`; orphaned PTYs pruned at sessionctl startup.
- The agent worktree may be dirty after takeover. The descent re-verify is what resolves dirtiness into pass/fail — the reminder is diagnostic, not prescriptive.

### 5.3 Signaler abstraction

`sessionctl.Signaler` (interface) decouples signaling for tests:

```go
type Signaler interface {
    Pause(pgid int) error   // SIGSTOP
    Resume(pgid int) error  // SIGCONT
}
```

Production impl uses `syscall.Kill(-pgid, ...)`. Windows and non-POSIX builds provide a no-op Signaler + takeover returns `OK=false, error:"takeover unsupported on this OS"`.

## Part 6 — Approval routing

Three previously-distinct approval paths converge:

| Source                                  | Mechanism                                     | Unified target           |
|-----------------------------------------|-----------------------------------------------|--------------------------|
| CloudSwarm HITL stdin (spec-2)          | `hitl.Reader.Read(ask_id, timeout)`            | `sessionctl.approve` evt |
| Terminal `Operator.Ask` (spec-7)        | in-process channel from huh widget             | `sessionctl.approve` evt |
| External `stoke approve <id>`           | Unix socket RPC (this spec)                    | `sessionctl.approve` evt |

### 6.1 Router

A new struct `sessionctl.ApprovalRouter` owns a map of `ask_id → chan Decision`. The three sources converge on its single `Resolve(ask_id, decision)` method:

```go
func (r *ApprovalRouter) Register(ask_id string, timeout time.Duration) <-chan Decision
func (r *ApprovalRouter) Resolve(ask_id string, d Decision) error  // unblocks the waiter
```

- `Operator.Ask` in the NDJSON implementation registers an ask_id, emits the prompt, and waits on the returned channel. `hitl.Reader` reads stdin and calls `Resolve`.
- Terminal operator does the same — waits on the channel, with the huh widget resolving it from the local TTY.
- `stoke approve` calls the sessionctl `approve` verb; the handler calls `Resolve`.

All three produce `operator.approve` events with the same payload shape — the source is recorded in `actor` (`"cli:term"`, `"cli:socket"`, `"cloudswarm:stdin"`).

### 6.2 Unresolved `--approval-id`

When `stoke approve <session>` is called without `--approval-id`, the handler returns the OLDEST open ask_id. Race condition (two operators approving simultaneously) → `ApprovalRouter.Resolve` is atomic on its map; the second caller receives `OK=false, error:"ask_id no longer open"`.

## Part 7 — Event reliability (crashed/exited session handling)

### 7.1 Socket gone, process gone

Client connect → `ECONNREFUSED` on a socket that exists on disk:
1. Client `os.Remove` the stale socket path.
2. Client returns exit code 1 with `error: "session not running"` on stdout.
3. Nothing is appended to the event log (there is no live server to emit through).

### 7.2 Socket present, process alive but unresponsive

Client connect succeeds but server never reads the request (client write timeout after 30s):
- Client returns exit code 2 with `error: "session unresponsive; try 'stoke status' and consider --force-kill"`.
- No event emitted.

### 7.3 Mid-command process crash

Handler appends to event log, then crashes before writing response:
- Event is durable (SQLite WAL — committed).
- Client read times out → exit code 2.
- Next `stoke status` discovers the stale socket, prunes it, and the event log shows the action took place.
- Downside: the operator sees no confirmation; they must check `stoke events --session <id> --last 5` (a helper added in this spec as a thin query wrapper).

### 7.4 Command fired after clean exit

Race window: `stoke run` finishes and unlinks the socket just before `stoke inject` fires. Client sees `ENOENT` on connect → returns `error:"session ended"`, exit 1. No event emitted, no state mutation. The injected task text is discarded (audit-logged to stderr only).

### 7.5 Approval against vanished session

`stoke approve` for a session that crashed AFTER registering an Ask but BEFORE resolving it:
- The CloudSwarm/terminal waiters are already dead (process gone).
- The `stoke approve` call sees ECONNREFUSED, prunes socket, returns `error:"session ended; approval discarded"`.
- The event log keeps the original `operator.ask` entry unresolved — replay tools mark it ORPHAN.

## Business Logic — cross-part summary

1. `stoke chat` starts → sessionctl server listening on `/tmp/stoke-<id>.sock`.
2. Operator types a request; chat dispatcher runs the turn.
3. Turn ends → DescentGate fires iff source files dirtied.
4. Trimmed descent runs (one repair max, deterministic T3, soft-pass=ask_operator).
5. On soft-pass, Operator.Ask → ApprovalRouter registers ask_id → blocks on channel.
6. In parallel, `stoke approve <session>` from another terminal resolves the channel.
7. Event log records `operator.approve` with source `cli:socket`.
8. Descent resumes with the decision; chat reply renders the final bullet list.

Meanwhile, `stoke status` from a third terminal can discover the session at any moment via `/tmp/stoke-*.sock` glob.

## Error Handling

| Failure                                          | Strategy                                   | Operator sees |
|--------------------------------------------------|--------------------------------------------|---------------|
| DescentGate `git` binary missing                 | Log once, disable gate for session         | Notify: "git not available; descent gate off" |
| Chat turn dirties only docs                      | No fire (filter rules §1.1)                | silent |
| Descent T4 retry consumed, still failing         | `SoftPassInteractive` → Operator.Ask       | three-button prompt (§1.4) |
| Socket file stuck from prior crashed run         | `os.Remove` on server startup              | silent |
| `stoke status` hits dead socket                  | prune + warn once                          | stderr: "pruning stale socket ses_X" |
| `stoke approve` without `--approval-id`, no open asks | handler returns `OK=false`           | "no pending approvals" |
| `stoke takeover` while another active            | reject                                     | "takeover already active" |
| HTTP `POST /ctl` with bad token                  | 401                                        | "unauthorized" |
| `stoke inject` on paused session                 | accept (queued); emits event               | "queued task; resume to dispatch" |
| `stoke budget --add 1.00 --dry-run`              | compute, do NOT publish budget_change      | shows old/new without mutating |
| PTY allocation fails on takeover                 | reject, SIGCONT agent, emit takeover_end with reason:"pty_alloc_failed" | error exit 1 |

## Boundaries — What NOT To Do

- Do NOT modify descent engine internals (spec-1 owns them).
- Do NOT modify the Operator interface (spec-7 owns `Operator`, `Option`, `NotifyKind`).
- Do NOT modify the HITL stdin protocol (spec-2 owns `hitl.Reader`).
- Do NOT modify the Executor interface or eventlog schema (spec-3 owns them).
- Do NOT add a new bus event taxonomy package — publish `operator.*` kinds directly via `bus.Publish` and let the existing streamjson subscriber mirror.
- Do NOT run a full 8-tier descent in chat mode. The trimmed config is non-negotiable — if an operator wants the full ladder, they run `stoke ship`.
- Do NOT add new CLI global flags; all verbs live under their own subcommands.
- Do NOT persist socket-to-session mappings in the event log (sockets are process-local transport, not durable state).
- Do NOT allow multiple concurrent takeovers per session.
- Do NOT default-on HTTP mode; socket-only by default, HTTP requires explicit `--listen`.
- Do NOT commit `/tmp/stoke-*.sock` to anywhere — they are ephemeral.
- Do NOT auto-resume a session on client disconnect during takeover — require explicit `takeover_release` or timer expiry.
- Do NOT skip descent on config-only changes (`package.json` alone must rerun install).

## Testing

### Chat descent gate (`internal/chat/`)
- [ ] Happy: turn writes `src/foo.go` → ShouldFire true → Run executes build AC → returns Pass → chat reply contains `✓ Build passes`.
- [ ] Docs-only turn writes `README.md` → ShouldFire false → no descent run, no output lines.
- [ ] Config-only turn writes `package.json` → ShouldFire true → install AC triggers before build.
- [ ] Repair path: fake RepairFunc that succeeds once → chat output shows `→ Retrying` then `✓`.
- [ ] Soft-pass path: RepairFunc fails once → Operator.Ask emitted with three labels; fake operator returns `accept-as-is` → turn ends with soft_pass:true.
- [ ] `edit-prompt` reply triggers `git checkout --` on touched files (verify tree clean after).
- [ ] git not installed (PATH strip) → gate logs once, disables; subsequent turns never call git.
- [ ] Extension filter: mixed turn (`.md` + `.go`) → fires on the `.go`, ignores `.md`.

### Sessionctl (`internal/sessionctl/`)
- [ ] Socket round-trip: server starts, client sends `status`, gets `OK=true` with expected `state`.
- [ ] Unknown verb → `OK=false, error:"unknown verb"`.
- [ ] Payload missing required field → `OK=false, error:"validation: ..."`.
- [ ] HTTP mode with wrong token → 401.
- [ ] HTTP mode with correct token → same `status` response as socket mode.
- [ ] Concurrent `pause` + `resume` sequence: state transitions pause→resume; `operator.pause` and `operator.resume` both in eventlog.
- [ ] `budget_add --dry-run` does NOT write to eventlog or publish bus event.
- [ ] `inject` appends a task; scheduler picks it up on next tick.
- [ ] Approval routing: ApprovalRouter.Register returns chan; Resolve from three distinct sources (socket, hitl, terminal) each unblocks it; second Resolve on same ask_id returns error.
- [ ] Discovery: two sessions running → glob finds both sockets; one socket stale → pruned, warning printed.
- [ ] Takeover happy path (fake Signaler + fake PTY): takeover_request pauses, takeover_release resumes, events emitted in order.
- [ ] Takeover timeout auto-release after `max_duration_s` with `reason:"timeout"`.
- [ ] Second takeover request while active → rejected.
- [ ] Server unlink socket on clean shutdown (`os.Stat` returns ENOENT).
- [ ] Stale socket at startup → removed, new socket created.

### CLI integration
- [ ] `stoke status` with 0 sockets → prints header + "no running sessions".
- [ ] `stoke status` with 2 sockets → prints 2 rows.
- [ ] `stoke approve <id>` with no `--approval-id` and 1 open ask → resolves.
- [ ] `stoke budget <id> --add 1.00` publishes `operator.budget_change` with `delta_usd=1.0`.
- [ ] `stoke inject <id> "X"` returns new task_id and exit 0.
- [ ] `stoke takeover` exits cleanly on `exit` in PTY.
- [ ] `--json` flag returns raw `Response.Data` JSON.

### Event reliability
- [ ] Session crash mid-handler → SQLite still has the committed event; client sees timeout.
- [ ] Approval against ended session → graceful "session ended; approval discarded".
- [ ] HTTP POST on session that just exited → 502/ECONNREFUSED surfaced as exit 1.

## Acceptance Criteria

- WHEN a chat turn writes a source file THE SYSTEM SHALL invoke VerificationDescent with `MaxCodeRepairs=1` and `MultiAnalystEnabled=false`.
- WHEN a chat turn writes only documentation THE SYSTEM SHALL NOT invoke the descent engine.
- WHEN chat descent returns soft-pass THE SYSTEM SHALL route the decision through Operator.Ask with the three labels `retry`, `accept-as-is`, `edit-prompt`.
- WHEN `stoke status` is run with no args THE SYSTEM SHALL discover all `/tmp/stoke-*.sock` files and aggregate their responses.
- WHEN any operator control verb mutates state THE SYSTEM SHALL emit an `operator.<verb>` event to bus AND persist it to the event log.
- WHEN a session is paused via `stoke pause` THE SYSTEM SHALL SIGSTOP its process group and reject tool dispatches until resumed.
- WHEN `stoke takeover` releases THE SYSTEM SHALL re-run verification descent over the post-takeover worktree state.
- WHEN the approval router resolves an ask_id THE SYSTEM SHALL reject any second resolution for the same ask_id.
- WHEN a client connects to a socket whose owning process has exited THE SYSTEM SHALL prune the socket and exit non-zero.
- WHEN `--listen :PORT` is set THE SYSTEM SHALL require a valid `STOKE_CTL_TOKEN` on every HTTP request.

### Bash AC commands

```bash
# 1. Chat gate unit tests.
go test ./internal/chat/... -run TestDescentGate

# 2. Sessionctl RPC.
go test ./internal/sessionctl/... -run TestSocketRPC
go test ./internal/sessionctl/... -run TestApproveRouting
go test ./internal/sessionctl/... -run TestTakeoverLifecycle
go test ./internal/sessionctl/... -run TestDiscovery

# 3. Build + vet + full suite.
go build ./cmd/stoke
go vet ./...
go test ./...

# 4. Status output smoke test.
./stoke status | head -5

# 5. Chat with --listen flag — background + query.
./stoke chat --listen :9100 &
CHAT_PID=$!
sleep 2
./stoke status :9100 | grep -q 'chat'
kill $CHAT_PID

# 6. Budget dry-run has no eventlog effect.
./stoke budget ses_test --add 1.00 --dry-run
sqlite3 .stoke/events.db 'SELECT COUNT(*) FROM events WHERE type = "operator.budget_change" AND json_extract(data, "$.dry_run") = 1' | grep -q '^0$'

# 7. Inject appends a task.
./stoke inject ses_test "run tests again"

# 8. Operator events are persisted.
sqlite3 .stoke/events.db 'SELECT COUNT(*) FROM events WHERE type LIKE "operator.%"'
```

## Implementation Checklist

1. [ ] **Create `internal/chat/descent_gate.go`.** Struct + methods per §Data Models. `ShouldFire` shells out to `git status --porcelain` and `git diff --name-only HEAD`; union list; apply extension allow/skip filters from §1.1. Return `(bool, []string, error)`.
2. [ ] **Build synthetic AC factory.** New file `internal/chat/descent_acs.go`. Skill-aware mapping per §1.3 (Go, TS/JS, Python, Rust, config-manifest). Reuse `skillselect.Detect(root)` to pick which ACs to produce. When `testselect` available, narrow test commands to affected packages. Each AC has `ID:"chat.<phase>"`, `Command:"..."`.
3. [ ] **Wire trimmed `DescentConfig` builder.** In `descent_gate.go`, construct the config with constants `MaxCodeRepairs=1`, `MaxRepairsPerFile=1`, `MultiAnalystEnabled=false`, `SoftPassPolicy=SoftPassInteractive`. Inject `Operator` (from session) and `OnLog` (chat-reply streamer that buffers lines into the assistant message). Build `RepairFunc` that dispatches a short agentloop turn with a "fix this error" directive derived from the last failing AC stderr.
4. [ ] **Integrate gate into chat session.** Edit `internal/chat/session.go` (and equivalent in `dispatcher.go`) to call `DescentGate.ShouldFire`/`Run` AFTER the agentloop returns and BEFORE flushing the reply. On `soft-pass:accept-as-is` return from Ask, mark the chat turn metadata `soft_pass:true` and include a one-line summary. On `edit-prompt`, `git checkout --` the touched files and re-enter user-input state (no reply flushed).
5. [ ] **Capture start commit in `buildChatSession`.** Edit `cmd/stoke/chat.go:398`. Run `git rev-parse HEAD`; store on the session struct. On empty output, set `gate = nil` and log one warning — gate disabled for non-git sessions.
6. [ ] **Create `internal/sessionctl/` package.** Files: `server.go`, `client.go`, `handlers.go`, `router.go`, `takeover.go`, `types.go`, `signaler_unix.go`, `signaler_other.go`. `types.go` defines `Request`, `Response`, `Opts`, `Signaler`. `server.go` owns socket + HTTP lifecycle, verb dispatch, event emission. `client.go` provides `Call(sock, req) (Response, error)` used by CLI wrappers.
7. [ ] **Implement verb handlers per table §Data Models.** Each handler validates payload, performs mutation, emits event via `eventlog.EmitBus`, returns `Response`. Use `dec.DisallowUnknownFields()` for strict parsing. HTTP mode verifies `STOKE_CTL_TOKEN` with `subtle.ConstantTimeCompare`.
8. [ ] **Implement `ApprovalRouter`.** `router.go`: map `ask_id → chan Decision` guarded by mutex. `Register`, `Resolve`, `List` (for "oldest open ask_id" query). Wire into sessionctl so the `approve` handler calls `Resolve`; terminal/NDJSON Operator implementations Register + wait on the channel.
9. [ ] **Implement `Signaler` for Unix + no-op fallback.** `signaler_unix.go` (build tag `//go:build unix`): `syscall.Kill(-pgid, SIGSTOP)` / `SIGCONT`. `signaler_other.go`: returns `errors.New("takeover unsupported on this OS")`.
10. [ ] **Implement takeover.** `takeover.go`: allocate PTY, spawn `bash` (or `$SHELL`), wire stdin/stdout to PTY, emit `operator.takeover_start`, SIGSTOP session PGID. On release (user exits, ctx cancel, or `max_duration_s` timer), SIGCONT, compute `git diff --stat` vs pre-takeover SHA, inject a user-role reminder into the agent's message queue (`session.injectSystemNote(...)`), trigger a descent re-verify, emit `operator.takeover_end` with `parent_id` linking to start event.
11. [ ] **Socket discovery.** `client.go` exports `DiscoverSessions(ctlDir) []string`. Uses `filepath.Glob(filepath.Join(ctlDir, "stoke-*.sock"))`. Dead socket handling: `Call` → ECONNREFUSED → `os.Remove(path)` + return typed error.
12. [ ] **Emit streamjson mirrors.** In `server.go`, register a bus subscriber on all `operator.*` kinds that calls `streamjson.EmitSystem("stoke.operator."+kind, data)`. One subscriber, not per-verb wiring.
13. [ ] **CLI wrappers.** `cmd/stoke/ctl_*.go` — eight files, one per verb (status, approve, override, budget, pause, resume, inject, takeover). Each parses flags, resolves session_id → socket path (or `--ctl-url`), calls `sessionctl.Client.Call`, renders response. `status` with no arg calls `DiscoverSessions` and aggregates.
14. [ ] **Render `stoke status` table.** Column formatter in `cmd/stoke/ctl_status.go`. Tab-aligned when TTY; raw JSON when `--json`. Dead-socket pruning warnings go to stderr.
15. [ ] **Wire sessionctl into `stoke run` / `stoke ship` / `stoke chat`.** In each command's entry point (`cmd/stoke/main.go`, `chat.go`, `ship.go`), call `sessionctl.StartServer(sess.ID, Opts{...})` before entering the main loop. `defer srv.Close()`. Read `--listen` flag (global or per-command) into `Opts.HTTPAddr`.
16. [ ] **Wire Operator.Ask into ApprovalRouter.** In `internal/operator/ndjson.go` and `terminal.go`, on each `Ask` call: generate `ask_id`, call `router.Register(ask_id, timeout)`, emit the prompt, block on returned channel. Terminal impl additionally runs its huh widget and calls `router.Resolve` from the widget callback. NDJSON impl's `hitl.Reader` callback calls `router.Resolve`.
17. [ ] **Update event-type registry in `internal/eventlog/types.go`.** Add `operator.approve, operator.override, operator.budget_change, operator.pause, operator.resume, operator.inject, operator.takeover_start, operator.takeover_end` to the canonical list + validator warnings.
18. [ ] **Unit tests.** Per §Testing sections. Use `testing/fstest`-style fake Signaler, fake PTY (io.Pipe pair), fake bus + fake eventlog. Ensure no test leaves sockets behind (`t.Cleanup` removes them).
19. [ ] **Integration test.** `internal/sessionctl/integration_test.go` — spawn real `./stoke chat --listen :0`, run `stoke status`, `stoke inject`, verify event log rows. Use `t.TempDir` for `.stoke/`.
20. [ ] **CI gate.** `go build ./cmd/stoke && go vet ./... && go test ./...` all green.

## Rollout

- Ship behind no feature flag. Chat mini-descent fires automatically; if it misbehaves, operators can `export STOKE_CHAT_DESCENT=0` to disable the gate (the one env flag introduced by this spec; default 1).
- `sessionctl` is always-on — the socket is cheap. `--listen` remains opt-in for HTTP.
- Takeover requires POSIX; Windows builds print "takeover unsupported" and return non-zero.
- Monitor bus-event counts per verb for the first week; expect the bulk (>90%) to be `operator.approve` from chat soft-passes.

## Metrics

| Item                   | Metric                                     | How measured                               | Target |
|------------------------|--------------------------------------------|--------------------------------------------|--------|
| Chat descent coverage  | % of chat turns that touched source & fired| counter: fired / touched                   | ≥ 95% |
| Chat descent cost      | avg USD added per chat turn when fired     | costtrack delta around gate.Run            | ≤ $0.05 |
| Chat descent latency   | p50 / p95 added ms per chat turn when fired| timestamp deltas                           | p50 ≤ 2s, p95 ≤ 10s |
| Operator approve flow  | count `operator.approve` / week            | SQL on event log                           | baseline — track trend |
| Takeover usage         | count `operator.takeover_start` / week     | SQL on event log                           | low single digits (escape hatch, not norm) |
| Stale socket prune     | count prunes / `stoke status` call         | stderr warning counter                     | ≤ 1 per call after first week |
