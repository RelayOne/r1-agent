<!-- STATUS: ready -->
<!-- CREATED: 2026-04-20 -->
<!-- DEPENDS_ON: spec-2 (streamjson subtypes), spec-7 (cost data + progress data), spec-11 (sessionctl for pause/approve) -->
<!-- BUILD_ORDER: 12 -->

# Live TUI Renderer — Implementation Spec

## Overview

`stoke ship`, `stoke chat`, and `stoke run` today either print raw NDJSON (spec-2) or use the minimal `internal/tui.Runner` text printer. At a TTY the operator deserves a live dashboard: session tree, focused task with ACs ticking, descent ticker for T2→T8 loops, cost gauge, recent-events scroll. This spec ships a pure-local Bubble Tea renderer (`internal/tui/renderer/`) that tees off the streamjson emitter (spec-2) and reads spec-7 cost data at 60 FPS. CloudSwarm and progress.md are unchanged — all consumers read the same bus. Fallbacks cover non-TTY, small terminals, monochrome, and Bubble Tea init failure so `--output stream-json` and CI pipes stay byte-identical. S-1 promise: "users see progress locally."

## Stack, Libraries, Patterns

- Go 1.22+; `bubbletea v0.25.0`, `lipgloss v1.1.0`, `charmbracelet/x/term v0.2.1` — all already in `go.mod`. No new third-party deps.
- Reuse: `internal/streamjson/emitter.go` + spec-2 `TwoLane` (subscribe via new `Tee()`); `internal/tui/interactive.go:40-157` Bubble Tea patterns (`Init`/`Update`/`View`, `tea.Tick`, mutex-guarded state); `internal/tui/runner.go` headless path (our `--no-tui` target); `internal/viewport/` for scrolling panes; `internal/costtrack/` Snapshot (spec-7 §G); `sessionctl` client (spec-11) for pause/approve.
- TTY detection: `term.IsTerminal(int(os.Stdout.Fd()))`. Color detect: `term.EnvColorProfile()` + `NO_COLOR` env. Size: `tea.WindowSizeMsg`. Event ingest: stdlib `chan` cap 1024.
- Event schema: `specs/cloudswarm-protocol.md:41-66` — every subtype drives a specific model field (§Event → UI mapping).

## Data Models

### `renderer.Event`

Normalized struct, parsed once at tee ingest; the `Update` switch never touches JSON.

| Field | Type | Source |
|-------|------|--------|
| `Type` | `string` | top-level (`system`, `hitl_required`, `complete`, `error`, `mission.aborted`) |
| `Subtype` | `string` | e.g. `session.start`, `task.complete`, `descent.tier`, `ac.result` |
| `SessionID`, `TaskID`, `ACID` | `string` | `_stoke.dev/session`, `/task_id`, `/ac_id` |
| `Tier` | `string` | `T1`..`T8` for descent events |
| `Category` | `string` | `code_bug`, `ac_bug`, `env_bug`, `acceptable_as_is` |
| `Attempt` | `int` | retry/repair counter |
| `Verdict`, `Reason`, `File` | `string` | pass/fail/soft-pass, one-line reason, path |
| `CostUSD` | `float64` | only on `cost.update` |
| `Ts` | `time.Time` | RFC3339 parsed once |
| `Raw` | `map[string]any` | full payload, used by scroll/status panes |

### `renderer.Model` (Bubble Tea)

```go
package renderer

type SessionNode struct {
    ID, Title, Reason string
    Status            Status        // pending|running|done|failed|blocked
    Attempt, MaxAttempts, SoftPasses int
    Tasks                           []TaskNode
    StartedAt, FinishedAt           time.Time
}
type TaskNode struct {
    ID, Title, FocusKey string
    Status              Status
    Attempt             int
    ACs                 []ACNode
}
type ACNode struct {
    ID, Title, Verdict, Tier string
    Status                   ACStatus // pass|fail|softpass|in_descent|pending
    AttemptN, AttemptMax     int
}
type DescentTick struct {
    Ts                               time.Time
    FromTier, ToTier, Label, Category, Detail string
    DurMs                            int64
}
type Model struct {
    sessions     []SessionNode
    order        []string           // insertion order
    focusSession, focusTask int
    costSpent, costBudget, costBurn float64
    costByKind   map[string]float64 // worker|reviewer|descent|reasoning
    descent      []DescentTick      // ring cap 8
    events       []Event            // ring cap 64
    eventOffset  int
    hitlOpen     bool
    hitlReq      HITLRequest
    hitlChoice   int                // 0=Approve 1=Reject 2=See full
    width, height int
    tooSmall, monochrome bool
    evCh         <-chan Event
    sessCtl      *sessionctl.Client // nil-safe
    done         bool
    quitReason   string             // "user"|"signal"|"stream_eof"
}

type Config struct {
    Output     io.Writer            // typically os.Stdout
    Events     <-chan Event          // tee'd from streamjson TwoLane
    SessCtl    *sessionctl.Client    // nil-safe (p/a/r become no-op toasts)
    DrivesStdin bool                 // true when TUI owns the subprocess stdin
    FPSCap     int                   // default 60
}
```

## CLI Surface

Flags added to `stoke ship`, `stoke chat`, `stoke run`, `stoke build`:

| Flag / Env | Default | Purpose |
|---|---|---|
| `--tui` | auto (TTY) | force TUI |
| `--no-tui` | auto (!TTY) | disable TUI; bare NDJSON |
| `--output stream-json` | (existing) | forces `--no-tui` |
| `STOKE_TUI=0\|1` | unset | global override (warn if forced over non-TTY) |

**Resolution** (first match wins): `--output stream-json` → off; `--no-tui` → off; `--tui` → on (warn if not TTY); `STOKE_TUI` → as set; else `term.IsTerminal(os.Stdout.Fd())`.

**Backward compat**: `stoke build --tui` keeps working; under the hood it constructs `renderer.Model` instead of the old `InteractiveModel` (which becomes a thin alias). `--headless` on `stoke build` aliases to `--no-tui` (one-line stderr deprecation warn). Bare NDJSON mode is byte-identical to today's `--output stream-json`.

## Layout

Verbatim mockup (80×20 minimum). Box drawing uses light Unicode; falls back to ASCII when monochrome.

```
┌ stoke ship sentinel-mvp ──────────────────────── $8.42 / $15.00 ┐
│                                                                 │
│  ▶ S3 API routes      [▰▰▰▰▰▱▱▱]  3/5 ACs  attempt 2/3         │
│    ✓ AC1 GET /tasks returns 200                                 │
│    ✓ AC2 POST /tasks creates                                    │
│    ⚖ AC3 Auth middleware — SOFT-PASS (env)                     │
│    ✓ AC4 Error handler 4xx                                      │
│    ✦ AC5 Rate limiter — DESCENT T4 repair 2/3                   │
│         └─ classify: code_bug (leaky bucket not decrementing)   │
│                                                                 │
│  ○ S4 Frontend pages   blocked on S3                            │
│  ○ S5 Integration      blocked on S4                            │
│                                                                 │
│  ✓ S1 Foundation types (4/4, attempt 1)                         │
│  ✓ S2 DB schemas       (5/5, attempt 2, 1 soft-pass)            │
│                                                                 │
├ descent ticker ────────────────────────────────────────────────┤
│  T2→T3 classify      code_bug        +4.2s                      │
│  T3→T4 repair        attempt 2       file: src/middleware/rl.ts │
│  T4→T2 verify        run pnpm build                             │
│                                                                 │
├ last 5 events ─────────────────────────────────────────────────┤
│  15:42:01  mcp.call.complete  linear.create_issue   4.1s        │
│  15:41:58  worker.tool         bash "pnpm test"                 │
│  15:41:52  descent.classify    code_bug  A1=code A2=ac A3=code  │
│  15:41:47  worker.file_write   src/middleware/rl.ts             │
│  15:41:42  worker.tool         bash "grep leaky src/"           │
│                                                                 │
├ [p]ause  [a]pprove  [s]tatus  [?]help                           │
└─────────────────────────────────────────────────────────────────┘
```

### Icon legend

| Glyph | Meaning | Monochrome fallback |
|-------|---------|---------------------|
| `▶` | running session (focused) | `>` |
| `○` | pending / blocked | `o` |
| `✓` | completed (pass) | `v` |
| `✗` | failed | `x` |
| `⚖` | soft-pass (approved by operator or auto) | `~` |
| `✦` | AC in active descent | `*` |
| `▰`/`▱` | progress fill / empty | `#`/`.` |

Status line at bottom shows key bindings; updates when modal is open to show modal keys.

### Compact mode (terminal <80×20)

Single line, overwritten in place with `\r`:

```
stoke ship sentinel-mvp  S3 T1 3/5 ACs  descent T4 (2/3)  $8.42/$15.00
```

## Event → UI component mapping

Every streamjson subtype (per spec-2 §Data Models → Event table) maps to exactly one model update. Unknown subtypes are appended to the event scroll with no tree/gauge effect (forward-compat).

| Event `type` | Subtype | Lane | Model update |
|---|---|---|---|
| `system` | `session.start` | obs | Append `SessionNode{Status:Running}` to `sessions`; set `focusSession` if first running |
| `system` | `session.complete` | obs | Mark matching session `done` or `failed` based on `_stoke.dev/verdict`; bump `SoftPasses` if reason includes soft-pass |
| `system` | `plan.ready` | obs | Preload `sessions` with pending nodes from `_stoke.dev/plan` |
| `system` | `task.dispatch` | obs | Add `TaskNode{Status:Running}` under its session; set `focusTask`; increment `Attempt` |
| `system` | `task.complete` | crit | Mark task `done`/`failed`; tick `x/y ACs` counter |
| `system` | `ac.result` | obs | Update matching `ACNode.Status` to `pass`/`fail`/`softpass` per `_stoke.dev/verdict` |
| `system` | `descent.start` | obs | Set AC `Status=in_descent`, init `Tier=T1`, `AttemptN=0` |
| `system` | `descent.tier` | obs | Push `DescentTick{FromTier,ToTier,Label}`; update AC `Tier`/`AttemptN` |
| `system` | `descent.classify` | obs | Update AC `Verdict` with `"classify: <category> (<reasoning excerpt>)"` |
| `system` | `descent.resolve` | obs | Set AC to final `Status` (`pass`/`softpass`/`fail`); clear descent indicator |
| `hitl_required` | — | crit | Open `hitlOpen=true`; populate `hitlReq` |
| `system` | `hitl.timeout` | obs | Close modal; append scroll line with reason |
| `system` | `cost.update` | obs | Update `costSpent`, `costByKind`; recompute `costBurn` |
| `system` | `progress` | obs | (ignored — progress.md renderer owns this; TUI already rebuilds same state) |
| `system` | `mcp.call.complete` | obs | Append to events ring (server+method+duration) |
| `system` | `worker.tool` | obs | Append to events ring (tool+brief input) |
| `system` | `worker.file_write` | obs | Append to events ring (path) |
| `system` | `stream.dropped` | obs | Append dim `[dropped N events]` line to events ring |
| `system` | `concurrency.cap` | obs | Show `max-workers=N` in status line (header right-side, after cost) |
| `system` | `intent.ambiguous` | obs | Append scroll line; if `--interactive`, no modal (operator spec handles Ask) |
| `system` | `task.intent.changed` | obs | Re-render task row label with new intent badge |
| `complete` | any | crit | Final `session.complete` fan-out; mark renderer `done=true`; set `quitReason="stream_eof"` |
| `error` | any | crit | Flash header red; append scroll line; if fatal, `done=true` |
| `mission.aborted` | — | crit | Header shows `ABORTED` banner; `done=true` |

Ring buffer policy: `events` cap 64 entries (drop-oldest on overflow). `descent` cap 8 ticks. Terminal views slice the tail.

## Business Logic

### Startup

1. CLI resolves TUI on/off (§CLI Surface). Off → register existing `tui.Runner`; return.
2. On: create `Events` channel cap 1024; subscribe via new `streamjson.TwoLane.Tee()` (spec-2 extension) — decoder goroutine converts each payload to `renderer.Event`, non-blocking send for observability, force-deliver for critical (evict oldest obs).
3. Detect monochrome (`NO_COLOR` or ascii color profile); detect initial size; `tooSmall=true` iff `width<80 || height<20`.
4. `tea.NewProgram(model, tea.WithAltScreen(), tea.WithMouseCellMotion()).Run()` in its own goroutine. If it returns error → one stderr warn, fall back to `tui.Runner` printf for the remainder of the session.

### Frame budget

`Update` is cheap (slice/map mutations). `View` rebuilds lipgloss strings. Throttle via `tea.Tick(16*time.Millisecond)`: if no event has arrived since last tick, skip re-render. 60 FPS = 16.6ms. On burst (`len(Events)>100`), drain non-blockingly, apply all, render once.

### HITL modal

Triggered by `type:"hitl_required"` on the critical lane. Set `hitlOpen=true`, `hitlChoice=0`. Main UI dims (lipgloss `Faint()`). Modal (centered, ≥60×10):

```
┌ APPROVAL REQUIRED ────────────────────────────┐
│  Reason: Soft-pass approval at T8             │
│  AC:     AC-03 (auth middleware)              │
│  Tier:   T8  Category: acceptable_as_is       │
│  Evidence: 3 analysts agreed env-gap only     │
│  [ Approve ]  [ Reject ]  [ See full descent ]│
└────────────────────────────────────────────────┘
```

`←/→` cycles choice; `enter` commits via `sessCtl.Approve(ask_id, note)` / `sessCtl.Reject(...)` off the render goroutine. `See full` swaps in a viewport over the modal showing full `_stoke.dev/context` JSON; `esc` returns. If `sessCtl==nil` AND `Config.DrivesStdin`, write decision line to subprocess stdin; otherwise show toast "session control unavailable". Timeout is owned by spec-2 — when `hitl.timeout` fires the modal auto-closes with a dim `[timed out]` footer for 3s. One modal at a time; concurrent `hitl_required` events queue.

### Sessionctl

Spec-11 client: `Pause(sessID)`, `Resume(sessID)`, `Approve(askID, note)`, `Reject(askID, note)`, `Status()`. All calls run in a goroutine so `Update` never blocks; results return via a `sessCtlReplyMsg` and append to the events ring. Nil-safe: missing socket path → key bindings emit toasts only.

### Key bindings

| Key | Mode | Action |
|-----|------|--------|
| `p` | main | Pause focused session via sessionctl |
| `r` | main | Resume focused session via sessionctl |
| `a` | main | Approve pending HITL (if modal closed: scans for the oldest pending hitl_required and approves it) |
| `s` | main | Pop detailed status pane (viewport w/ full `_stoke.dev/*` JSON for focused task) |
| `↑`/`k` | main | Scroll events log up one line |
| `↓`/`j` | main | Scroll events log down |
| `tab` | main | Cycle focus to next running session |
| `shift+tab` | main | Cycle to previous running session |
| `enter` | main | Dive into focused task detail view |
| `esc` | detail/modal | Return to main |
| `?` | any | Toggle help overlay |
| `q` / `ctrl+c` | any | Exit TUI; session keeps running in background; print `session <id> continues in background` to stderr |
| `←`/`→` | modal | Cycle Approve/Reject/See full |
| `enter` | modal | Commit modal choice |

### Quit semantics

- `q` / `ctrl+c` on the TUI goroutine sends `tea.Quit`. It does NOT cancel the root context — the subprocess continues running under `sessionctl`. Last line printed on stdout before TUI exit: `session <id> continues; reattach with 'stoke attach <id>'` (spec-11 provides `attach`). If `stoke attach` does not yet exist, print `run with STOKE_TUI=0 ... | tail -f` as a degraded tip.
- If the root context is canceled (signal handler in `run_cmd.go`, spec-2), the TUI goroutine observes context and exits cleanly within 200ms by setting `done=true` and returning `tea.Quit`.

## Fallback decision tree

```
                  Is stdout a TTY?
                  /             \
                NO               YES
                 │                │
       ┌─────────┴─────────┐      ├─ --output stream-json?
       │                   │      │   └ YES → NDJSON-only (same as --no-tui path)
  --tui explicit?     NDJSON-only │
       │                          │
       ├ YES → warn +            (TUI path)
       │        init Bubble Tea     │
       │        anyway              │
       │                            ├ terminal < 80×20?
       └ NO → NDJSON-only            │   └ YES → compact single-line mode
                                     │
                                     ├ NO_COLOR or ascii profile?
                                     │   └ YES → monochrome glyphs (§Icon legend fallback)
                                     │
                                     ├ tea.NewProgram().Run() error?
                                     │   └ YES → one stderr warn line, degrade to tui.Runner printf
                                     │
                                     └ Normal: full 80×20+ color TUI
```

Degradation is one-way per process. Once we fall back, we stay fallen back for the session.

## Backpressure

Per spec-2 two-lane design, mirrored at the TUI consumer:

- `Events` channel buffer: 1024.
- Critical event subtypes (`hitl_required`, `task.complete`, `error`, `complete`, `mission.aborted`) are always appended synchronously. If the channel is full and the next event is critical, we evict the oldest observability entry via a drain-one-then-send pattern (same trick as spec-2 `TwoLane.EmitSystem`).
- Observability events (`descent.tier`, `descent.classify`, `cost.update`, `mcp.call.complete`, `worker.tool`, `worker.file_write`, `stream.dropped`, `progress`, `session.start`, `plan.ready`, `task.dispatch`, `ac.result`, `descent.start`, `session.complete`, `concurrency.cap`, `intent.ambiguous`) are drop-oldest on overflow.
- A counter `droppedInTUI uint64` is incremented on each observability drop; the header shows `(dropped N)` in red when non-zero. Reset to zero after 5s with no further drops.
- Render loop runs at 60 FPS max (16ms tick); if a tick fires with no new event, `View` is skipped.

## Error Handling

| Failure | Strategy |
|---|---|
| Bubble Tea `Run` returns error | One stderr warn, fall back to `tui.Runner` printf for the rest of the session |
| Resize below 80×20 | Switch to compact mode; revert on next resize if large enough |
| Events channel overflow (observability) | Drop-oldest; increment counter; render `(dropped N)` header badge |
| Events channel overflow (critical) | Evict oldest obs, force-send; last-resort block ≤100ms then panic-recover and emit `error` scroll line |
| sessionctl connect / call fails | Toast "session control unavailable"; if inside modal, show red error footer, `esc` returns |
| Unknown subtype | Append verbatim to events scroll; tree unchanged |
| Malformed JSON from emitter tee | Skip; increment `malformedCount`; render `(malformed N)` header badge |
| `NO_COLOR` set | Disable all lipgloss color styles; use ASCII glyphs |
| Terminal loses TTY mid-run (e.g. screen detach) | `tea.Program` returns error on next tick → fallback to printf |
| `ctrl+c` during modal | Esc-equivalent; modal closes; root ctx NOT canceled |

## Boundaries — What NOT To Do

- Do NOT parse `progress.md` — consume the event bus directly (spec-7 writes progress.md in parallel; two consumers of one stream).
- Do NOT re-implement streamjson emission — subscribe via `streamjson.TwoLane.Tee()` (add this helper in spec-2's emitter if absent; see Implementation Checklist item 1).
- Do NOT own cost computation — read `costtrack.Global.Snapshot()` only on `cost.update` events (trigger-driven; spec-7 §G owns math).
- Do NOT implement sessionctl server — spec-11 owns the socket; we are a client only.
- Do NOT re-implement HITL timeout — spec-2 `internal/hitl/` owns timing; we observe `hitl.timeout` events.
- Do NOT modify `internal/streamjson/emitter.go` field names or wire format — spec-2 is authoritative.
- Do NOT alter `internal/tui/interactive.go` public API — adapter pattern: keep it as a thin wrapper that constructs `renderer.Model` under the hood, so `stoke build --tui` users see no change.
- Do NOT require CloudSwarm for operation — this is a pure-local consumer. CloudSwarm subprocesses always set `--output stream-json` which disables TUI.
- Do NOT drive stdin by default — TUI is a passive consumer unless `Config.DrivesStdin=true` is set (e.g. `stoke chat` where it spawned the session).
- Do NOT block the Bubble Tea Update loop on network I/O (sessionctl calls always go through a goroutine + `tea.Cmd`).
- Do NOT remove the existing `internal/tui/runner.go` — it is the `--no-tui` / fallback target.

## Testing

### `internal/tui/renderer/`

- [ ] `TestModelUpdate`: synthetic events → assert model state transitions (session.start appends node; task.complete ticks counter; ac.result updates AC; descent.tier pushes to ticker ring).
- [ ] `TestDropOldest`: fill `Events` channel to 1024 with observability events, send 100 more → `droppedInTUI >= 100`, header contains `(dropped N)`.
- [ ] `TestCriticalAlwaysDelivered`: fill with observability, send 10 `hitl_required` → all 10 observed (possibly via eviction), none dropped, modal opens for first.
- [ ] `TestFallbackNoColor`: `NO_COLOR=1` → lipgloss styles are no-op, glyphs map to ASCII fallbacks.
- [ ] `TestFallbackSmallTerminal`: `tea.WindowSizeMsg{Width:70,Height:18}` → `tooSmall=true`, `View()` returns single-line format.
- [ ] `TestFallbackTeaInitError`: inject `tea.NewProgram` stub that errors → renderer returns without panic; subsequent `Event()` calls route to `tui.Runner` printf path.
- [ ] `TestQuitKeepsSessionAlive`: `q` key → `tea.Quit`, but root context NOT canceled; stderr line contains `continues in background`.
- [ ] `TestHITLModal`: emit `hitl_required` → `hitlOpen=true`, `enter` on Approve → sessionctl mock receives `Approve(ask_id)`; modal closes.
- [ ] `TestHITLSessionctlNil`: `Config.SessCtl=nil`, emit `hitl_required` → modal opens, `a` shows toast "session control unavailable".
- [ ] `TestEventScrollRing`: push 100 events → ring holds 64 most recent; `↑` scrolls offset, `↓` restores.
- [ ] `TestDescentTickerRing`: push 20 `descent.tier` → ring holds 8 most recent.
- [ ] `TestUnknownSubtype`: emit `system/stoke.experimental.foo` → appended to scroll verbatim; tree unchanged.

### `cmd/stoke/run_cmd_tui_integration_test.go`

- [ ] `TestTUIAutoEnabledInTTY`: fake PTY → TUI initializes; first rendered frame contains header `stoke run`.
- [ ] `TestTUIAutoDisabledInPipe`: pipe stdout → TUI skipped; first line on stdout is valid NDJSON with `type=system, subtype=session.start`.
- [ ] `TestStreamJSONForcesNoTUI`: `--tui --output stream-json` together → warn on stderr, NDJSON wins.
- [ ] `TestSTOKETUIEnvOverride`: `STOKE_TUI=0` + TTY → no TUI; `STOKE_TUI=1` + pipe → TUI (warn).
- [ ] `TestCompactMode`: run with `COLUMNS=60` → header is compact single-line overwritten via `\r`.

## Acceptance Criteria

- WHEN stdout is a TTY AND no `--no-tui` flag THE SYSTEM SHALL initialize the Bubble Tea renderer and display the header within 500ms of session start.
- WHEN `--output stream-json` is set THE SYSTEM SHALL disable the TUI unconditionally and emit first-line NDJSON `session.start` within 100ms of invocation.
- WHEN `STOKE_TUI=0` is set THE SYSTEM SHALL skip TUI init even on a TTY.
- WHEN a `hitl_required` event is received THE SYSTEM SHALL open a modal within one render tick (≤16ms).
- WHEN the operator presses `a` in the HITL modal THE SYSTEM SHALL call `sessionctl.Approve(ask_id)` off the render goroutine and close the modal.
- WHEN the events channel is full AND an observability event arrives THE SYSTEM SHALL drop the oldest observability entry and increment the drop counter.
- WHEN the events channel is full AND a critical event (`hitl_required`/`task.complete`/`error`/`complete`/`mission.aborted`) arrives THE SYSTEM SHALL deliver it by evicting oldest observability first.
- WHEN the terminal is resized below 80×20 THE SYSTEM SHALL switch to compact single-line mode.
- WHEN the operator presses `q` or Ctrl-C THE SYSTEM SHALL exit the TUI without canceling the root context and print a reattach hint to stderr.
- WHEN Bubble Tea fails to initialize THE SYSTEM SHALL log one stderr warn and fall back to the existing printf `tui.Runner` for the remainder of the session.
- WHEN `NO_COLOR` environment is set THE SYSTEM SHALL render ASCII glyphs with no ANSI color codes.

### AC commands (bash)

```bash
go test ./internal/tui/renderer/... -run TestModelUpdate -v
go test ./internal/tui/renderer/... -run TestDropOldest -v
go test ./internal/tui/renderer/... -run TestCriticalAlwaysDelivered -v
go test ./internal/tui/renderer/... -run TestFallbackNoColor -v
go test ./internal/tui/renderer/... -run TestFallbackSmallTerminal -v
go test ./internal/tui/renderer/... -run TestFallbackTeaInitError -v
go test ./internal/tui/renderer/... -run TestHITLModal -v
go test ./internal/tui/renderer/... -run TestQuitKeepsSessionAlive -v
go test ./cmd/stoke/... -run TestTUIAutoDisabledInPipe -v
go test ./cmd/stoke/... -run TestStreamJSONForcesNoTUI -v
STOKE_TUI=0 ./stoke ship --dry-run 2>/dev/null | head -1 | jq -e '.type == "system" and .subtype == "session.start"'
./stoke ship --tui --dry-run </dev/null 2>&1 | head -1 | grep -qE '(stoke|warning: not a TTY)'
./stoke run --help | grep -q -- '--tui'
./stoke run --help | grep -q -- '--no-tui'
go build ./cmd/stoke && go test ./... && go vet ./...
```

## Implementation Checklist

1. [ ] **Add `TwoLane.Tee()` helper to `internal/streamjson/twolane.go`** (extend spec-2's work): returns `<-chan map[string]any` channel with capacity 1024. Each event pushed to both critical/observ lanes is also non-blocking-sent to all registered tees. If a tee channel is full, drop-oldest observability and always-deliver critical (mirror the main emitter policy). Tee channels registered via `tl.AddTee(ch chan<- map[string]any, critical bool)`. Tests: tee receives all events in the same order as stdout.

2. [ ] **Create `internal/tui/renderer/event.go`**: define `renderer.Event` struct (fields per §Data Models), `ParseEvent(m map[string]any) (Event, bool)` converts emitter payload to typed struct. Unknown types still parse (preserve `Raw` map). One pass, no reflection.

3. [ ] **Create `internal/tui/renderer/model.go`**: `Model`, `SessionNode`, `TaskNode`, `ACNode`, `DescentTick` types per §Data Models. Initial state builders. `findSession(id)`/`findTask(sessID, taskID)`/`findAC(...)` O(1) via maps.

4. [ ] **Create `internal/tui/renderer/update.go`**: `func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd)`. Handle `tea.KeyMsg` per §Key bindings table; `tea.WindowSizeMsg` sets width/height and tooSmall flag; custom `eventMsg{Event}` dispatches into per-subtype handlers (22 switch arms matching §Event → UI component mapping). All mutations mutex-guarded.

5. [ ] **Create `internal/tui/renderer/view.go`**: main `View() string` + sub-renderers `renderHeader`, `renderSessionTree`, `renderDescentTicker`, `renderEventsScroll`, `renderStatusBar`, `renderHITLModal`, `renderCompact`. Use lipgloss styles; monochrome path skips colors. Use box-drawing Unicode by default, ASCII fallback.

6. [ ] **Create `internal/tui/renderer/style.go`**: lipgloss style definitions (header bar, session row, AC row, descent row, modal box, error banner). `Monochrome()` returns an alt style-set with all colors removed and Unicode→ASCII glyph map applied.

7. [ ] **Create `internal/tui/renderer/run.go`**: `Run(ctx context.Context, cfg Config) error`. Creates `tea.NewProgram(model, tea.WithAltScreen())` (skip altscreen when compact). Starts a goroutine that ranges over `cfg.Events` channel and calls `p.Send(eventMsg{ev})`. Handles `ctx.Done()` → `p.Quit()`. Returns after `p.Run()` exits; if returns error → caller degrades to `tui.Runner`.

8. [ ] **Extend `cmd/stoke/run_cmd.go`** (and `ship_cmd.go`, `chat_cmd.go`, `build_cmd.go`): add `--tui`/`--no-tui` flags, TUI resolution logic per §CLI Surface. When TUI enabled: construct `Events` channel, call `streamjson.TwoLane.AddTee(events, true)`, launch `renderer.Run` goroutine. When disabled: existing paths unchanged. Respect `STOKE_TUI` env. Deprecated alias: `--headless` → warn → `--no-tui`.

9. [ ] **Wire sessionctl client**: in `cmd/stoke/run_cmd.go`, if `$XDG_RUNTIME_DIR/stoke/<pid>.sock` exists or will be created by spec-11, pass `sessionctl.NewClient(path)` into `renderer.Config`. If spec-11 not landed yet, pass `nil` and rely on nil-safe no-op path.

10. [ ] **Refactor `internal/tui/interactive.go`** to be a thin wrapper: keep `NewInteractiveModel`/`Run` public names; under the hood construct `renderer.Model` and delegate. Preserve existing `TaskStart`/`Event`/`TaskComplete` hooks by mapping to synthetic `renderer.Event` structs. Tests in `interactive_test.go` pass unchanged.

11. [ ] **Backpressure tee registration**: extend `streamjson.TwoLane` to accept up to 4 tees; each tee gets its own drop counter. Emit `stream.dropped` on main stdout AND on each affected tee every 5s (existing emitter already does the main one).

12. [ ] **Compact mode writer**: `renderCompact(m) string` returns a single line; `run.go` writes it via `fmt.Fprintf(cfg.Output, "\r%s", line)` WITHOUT altscreen when `tooSmall`. Final newline on quit.

13. [ ] **Help overlay**: `renderHelp()` shows a table of all key bindings (§Key bindings) centered. Toggled by `?`; `esc` dismisses.

14. [ ] **Status pane**: `renderStatus()` is a viewport showing full `_stoke.dev/*` JSON for focused task + last 20 events. Triggered by `s`. Uses Bubble Tea `viewport` bubble (already imported by `internal/viewport/`).

15. [ ] **Integration test `cmd/stoke/run_cmd_tui_integration_test.go`**: covers §Testing cmd/stoke tests. Uses `creack/pty` for fake TTY OR falls back to env flag forcing (if adding pty dep is vetoed, drop PTY tests and rely on env-forced tests only — decision note required in PR).

16. [ ] **Update `cmd/stoke/main.go` help text** for `ship`, `chat`, `run`, `build` to list `--tui` and `--no-tui`. One-line summary: `"--tui       enable live TUI renderer (auto on TTY)"`.

17. [ ] **`go build ./cmd/stoke && go test ./... && go vet ./...`** all green. No new lint warnings. Verify binary size delta <1 MB (bubbletea/lipgloss already linked).
