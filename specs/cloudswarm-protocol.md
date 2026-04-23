<!-- STATUS: done -->
<!-- BUILD_COMPLETED: 2026-04-22 -->
<!-- CREATED: 2026-04-20 -->
<!-- DEPENDS_ON: (none — parallel with spec-1) -->
<!-- BUILD_ORDER: 2 -->

# CloudSwarm Protocol — Implementation Spec

## Overview

Stoke must be invokable as an opaque subprocess by CloudSwarm's `ExecuteStokeActivity` (`platform/temporal/activities/execute_stoke.py:288-319`). Today CloudSwarm calls `stoke run --output stream-json [--repo URL] [--branch NAME] [--model MODEL] TASK_SPEC` and streams NDJSON events off stdout; Stoke has no `run` subcommand, no `--output` flag, and no HITL stdin reader. This spec lands the `stoke run` command, the HITL event contract, the two-lane stream emitter, and the exit-code contract so that CloudSwarm's existing fixtures (`platform/temporal/activities/test_execute_stoke.py`) parse Stoke output correctly. It ships in parallel with spec-1 (descent hardening) and unblocks the enterprise (governance_tier=enterprise) tier — standalone operators get the same command with a 1-hour HITL timeout and auto-grant soft-pass. No code is rewritten; we wrap existing SOW + chat-intent paths.

## Stack & Versions

- Go 1.22+
- Existing `github.com/google/uuid` (already in `internal/streamjson`)
- Existing `internal/streamjson/emitter.go` (extend, do NOT replace)
- Existing `internal/chat/intent.go` classifier (reuse for free-text routing)
- Existing `cmd/stoke/sow_native.go::runSessionNative` (reuse for `--sow` path)
- Existing `cmd/stoke/descent_bridge.go` (extend `buildDescentConfig` with `SoftPassApprovalFunc`)
- No new third-party dependencies

## Existing Patterns to Follow

- Emitter: `internal/streamjson/emitter.go` — keep Claude Code wire format (`type`, `uuid`, `session_id`, `subtype`, `_stoke.dev/*`)
- SOW runner: `cmd/stoke/sow_native.go` — top-level entry is `runSessionNative`
- Command wiring: `cmd/stoke/main.go:1476-1480` — flag parsing + emitter construction pattern
- Descent hooks: `internal/plan/verification_descent.go:232-291` — add new hook field, preserve nil-default semantics
- Chat intent: `internal/chat/intent.go::ClassifyIntent` — deterministic keyword scan returns `Intent` enum
- Bus: `internal/bus/bus.go` — publish lifecycle events alongside streamjson (C3 from decisions)
- Signal trap pattern: `cmd/stoke/main.go` already installs `signal.Notify` for SIGINT/SIGTERM; extend rather than duplicate

## Library Preferences

- JSON: stdlib `encoding/json` with `json.NewEncoder(os.Stdout)` — no bufio wrapper per RT-04 §3 (os.Stdout is unbuffered; Encode writes full line + `\n` atomically under mutex)
- Timers: `time.NewTimer` + `time.AfterFunc` — do NOT use `os.Stdin.SetDeadline` (broken for Linux pipes per golang/go#24842)
- Stdin reader: goroutine + `chan []byte` + `select` per RT-04 §7
- UUIDs: `github.com/google/uuid` (already used by emitter)

## Data Models

### `streamjson.Event` (extension — keep backward compat)

New subtypes added under existing `system`/`result` events via `subtype` + `_stoke.dev/*` keys. Wire format unchanged.

| Subtype | Parent Type | Emitted When | CloudSwarm Action |
|---------|-------------|--------------|-------------------|
| `hitl_required` | top-level `type` | Worker/descent needs operator approval | Pauses subprocess (SIGSTOP), creates approval row, publishes NATS |
| `plan.ready` | `system` | SOW parsed, tasks ordered | Stores verbatim in `stoke_events` |
| `task.dispatch` | `system` | Scheduler picks task, worktree created | Stores verbatim |
| `task.complete` | `system` | Task exits with terminal verdict | Stores verbatim (CRITICAL — drain before exit) |
| `ac.result` | `system` | One AC passes/fails | Stores verbatim |
| `descent.start` | `system` | Descent engine entered for a failing AC | Stores verbatim |
| `descent.tier` | `system` | Tier boundary (T1-T8) | Stores verbatim (observability-only — drop-oldest safe) |
| `descent.classify` | `system` | Reasoning verdict emitted | Stores verbatim |
| `descent.resolve` | `system` | Descent resolved AC (pass/softpass/fail) | Stores verbatim |
| `session.start` | `system` | Top-level `stoke run` session booting | Stores verbatim |
| `session.complete` | `system` | Top-level session exited cleanly | Stores verbatim |
| `cost.update` | `system` | `costtrack.OnUpdate` fires | Stores verbatim (drop-oldest safe) |
| `progress` | `system` | Progress tick from `progress_renderer` | Stores verbatim (drop-oldest safe) |
| `error` | top-level `type` | Fatal runtime error (pre-result) | Stores verbatim (CRITICAL) |
| `complete` | top-level `type` | Terminal final line per RT-04 §6 | Stores verbatim (CRITICAL — last line before exit) |
| `stream.dropped` | `system` | Periodic drop counter from observability lane | Stores verbatim |
| `mission.aborted` | top-level `type` | Emitted from signal handler before exit | Stores verbatim (CRITICAL) |
| `concurrency.cap` | `system` | `STOKE_MAX_WORKERS` env read at startup (D10) | Stores verbatim |

All Stoke-specific fields live under `_stoke.dev/<key>` (RT-STOKE-SURFACE §14) so Claude-Code-only parsers see a superset, not a breaking change.

### `hitl.Request`

| Field | Type | Purpose | Default |
|-------|------|---------|---------|
| `Reason` | `string` | Human-readable "why" | required |
| `ApprovalType` | `string` | `"soft_pass" \| "file_write" \| "destructive_op"` | required |
| `File` | `string` | Path the approval concerns (if any) | `""` |
| `Context` | `map[string]any` | Freeform extras (AC id, tier, verdict, etc.) | nil |

### `hitl.Decision`

| Field | Type | Constraints | Default |
|-------|------|-------------|---------|
| `Approved` | `bool` | required | — |
| `Reason` | `string` | operator note | `""` |
| `DecidedBy` | `string` | e.g. `"user@example.com"` | `""` |

Wire format on stdin is the CloudSwarm-verbatim shape from RT-CLOUDSWARM-MAP §3:

```json
{"decision": true, "reason": "Approved by user", "decided_by": "user@example.com"}
```

Note: supervisor **base64-decodes** before writing to our stdin; we read plain JSON per line.

## CLI Surface

### `stoke run`

```
stoke run [flags] TASK_SPEC
stoke run --sow path/to/sow.md [flags]
```

**Flags:**

| Flag | Type | Purpose |
|------|------|---------|
| `--output` | string | `"stream-json"` (only value today; future: `"plain"`, `"tty"`) |
| `--repo` | string | Repo URL — if set, clone to tmp before dispatch |
| `--branch` | string | Branch name to check out |
| `--model` | string | Override primary model |
| `--sow` | string | Path to SOW; if present, switches to SOW mode |
| `--hitl-timeout` | duration | Override HITL wait (default 1h standalone, 15m CloudSwarm) |
| `--governance-tier` | string | `community` (default, standalone) \| `enterprise` (HITL-gated soft-pass) |

**Positional:** `TASK_SPEC` — free-text task. Ignored if `--sow` is set.

**Dispatch logic** (D-2026-04-20-01):

1. If `--sow` set → parse SOW, call existing `runSessionNative` with streamjson enabled.
2. Else if `TASK_SPEC` present → call `chat.ClassifyIntent(TASK_SPEC)`:
   - `IntentQuery` or unclassifiable → route to chat-intent flow (ephemeral SOW synthesis via chat, falls back to `runSessionNative`).
   - Other intents (abort/redirect/inject/pause) → return usage error (exit 2) since these make no sense in one-shot mode.
3. Else → print usage, exit 2.

### Backward compatibility

- Existing `stoke ship --output stream-json` keeps working (same emitter, same wire format). `run` does NOT replace `ship`; both commands share the emitter package.
- No existing command adds `--hitl-timeout` or `--governance-tier` — they are `run`-only.
- The emitter's current `EmitSystem`/`EmitAssistant`/`EmitUser`/`EmitResult`/`EmitStreamEvent` entrypoints are unchanged. New subtypes are emitted via the existing `EmitSystem` API with extra fields.

## Business Logic

### NDJSON Event Schema

Each line is a single JSON object terminated by `\n`. Full wire format (example `descent.tier` observability event):

```json
{"type":"system","subtype":"descent.tier","uuid":"018e…","session_id":"018e…","_stoke.dev/tier":"T4","_stoke.dev/ac_id":"AC-03","_stoke.dev/attempt":2,"_stoke.dev/category":"code_bug"}
```

Example `hitl_required` (CloudSwarm pauses subprocess on this line):

```json
{"type":"hitl_required","uuid":"018e…","session_id":"018e…","reason":"Soft-pass approval at T8","approval_type":"soft_pass","file":"internal/foo/bar.go","_stoke.dev/ac_id":"AC-03","_stoke.dev/tier":"T8","_stoke.dev/category":"acceptable_as_is"}
```

Example `complete` (final line before clean exit 0):

```json
{"type":"complete","uuid":"018e…","session_id":"018e…","subtype":"success","total_cost_usd":0.42,"num_turns":14,"duration_ms":120345}
```

### Two-Lane Emitter

Per RT-04 §4 and decision C1.

- **Critical lane** (cap 256, blocking): `hitl_required`, `task.complete`, `error`, `complete`, `mission.aborted`
- **Observability lane** (cap 1024, drop-oldest): `descent.tier`, `descent.classify`, `descent.resolve`, `progress`, `cost.update`, `stream.dropped`, `session.start`, `plan.ready`, `task.dispatch`, `ac.result`, `descent.start`, `session.complete`, `concurrency.cap`
- Background goroutine reads both channels; writes under mutex via `json.Encoder.Encode` on `os.Stdout` (no `bufio.Writer`, no `Flush`)
- Every 5s tick, if drop counter > 0, emit `stream.dropped {count:N}` and reset

### HITL Stdin Protocol

1. Stoke emits line: `{"type":"hitl_required", ...}` on critical lane (blocks if pipe back-pressured — OK, CloudSwarm will immediately SIGSTOP).
2. Stoke calls `hitl.RequestApproval(ctx, req)`:
   - Registers a single waiter (only one HITL at a time per session — guard with mutex; emit `error` + return reject if a second concurrent request comes in)
   - Launches stdin reader goroutine if not already running (singleton)
   - Reads lines, parses JSON, matches any pending waiter, delivers `Decision`
3. Timeout: `time.NewTimer(timeout)`; on fire → emit `hitl.timeout` observability event → return `Decision{Approved:false, Reason:"timeout"}`
4. If stdin closes (EOF) → return `Decision{Approved:false, Reason:"stdin_closed"}` → exit code 3

**Timeout defaults:**
- `--governance-tier=enterprise`: 15m (matches CloudSwarm workflow per RT-CLOUDSWARM-MAP §3 `workflows/stoke_agent.py:23`)
- `--governance-tier=community` (standalone, default): 1h
- `--hitl-timeout` override wins if set

**Error handling:**
- Malformed JSON line → emit `error` observability event with the raw line (base64-encoded as `_stoke.dev/raw`), discard that line, continue reading the next
- `decision` field missing/non-bool → treat as rejection with `reason:"malformed_decision"`
- Missing stdin (os.Stdin is nil or closed at startup) → at first `RequestApproval` call, return auto-reject and emit `error` subtype `stdin_missing`

### Descent Integration

Add new field to `plan.DescentConfig` in `internal/plan/verification_descent.go` (insertion near line 289, preserving defaults):

```go
// SoftPassApprovalFunc is called at T8 when all 6 soft-pass gates
// evaluate true. If nil, soft-pass is auto-granted (current
// behavior). If non-nil and returns false, descent returns FAIL
// instead of soft-pass.
SoftPassApprovalFunc func(ctx context.Context, ac AcceptanceCriterion, verdict ReasoningVerdict) bool
```

Wire in `cmd/stoke/descent_bridge.go::buildDescentConfig`:

- If `cfg.GovernanceTier == "enterprise"`: `dc.SoftPassApprovalFunc = func(ctx, ac, v) bool { d := hitl.RequestApproval(ctx, hitl.Request{Reason: "Soft-pass at T8", ApprovalType:"soft_pass", File: ac.ID, Context: map[string]any{"category": v.Category, "reasoning": v.Reasoning}}); return d.Approved }`
- Else (standalone): leave nil → auto-grant (current behavior)

The existing `OnLog` callback (verification_descent.go:290-297) stays, but is upgraded to emit structured events on both bus and streamjson. In `buildDescentConfig`, replace the current `fmt.Printf` body with:

```go
dc.OnLog = func(msg string) {
    emitter.EmitSystem("descent.tier", map[string]any{
        "_stoke.dev/session": session.ID,
        "_stoke.dev/message": msg,
    })
    bus.Publish(bus.Event{Kind: "descent.log", Data: msg})
    fmt.Printf("  [descent %s] %s\n", session.ID, msg)  // keep terminal output
}
```

### Exit Code Contract (D11)

| Code | Condition |
|------|-----------|
| 0 | All sessions passed (including soft-passes) |
| 1 | ≥1 session failed |
| 2 | Budget exhausted (`costtrack.OverBudget()` at entry) OR usage error |
| 3 | Operator aborted (HITL rejected) OR stdin closed mid-HITL |
| 130 | SIGINT |
| 143 | SIGTERM |

Implementation: top-level `main` in `run_cmd.go` computes an `exitCode int` from a run result struct; `os.Exit(exitCode)` only after `emitter.Drain(5 * time.Second)` completes.

### Graceful Shutdown

Per RT-04 §6:

1. `signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)` in `run_cmd.go`.
2. On signal: cancel root `context.Context`; start grace timer (5s).
3. Emit `mission.aborted` (critical lane, blocking) with `{reason: "signal", signal: "SIGINT"|"SIGTERM"}`.
4. Close both channels; background emitter drains until empty or timer fires.
5. `os.Exit(130 or 143)`.

**Do not exit from the signal handler** — route through context cancel so the emitter drains.

### Line-Buffered Stdout (SIGSTOP Survival)

Per RT-04 §5:

- Each event = one `json.Encoder.Encode` call = one `write(2)` of `buf + "\n"`.
- The encoder writes atomically under `e.mu` so no two goroutines can interleave bytes.
- Do NOT wrap `os.Stdout` in `bufio.Writer` — the existing emitter already does not, and adding it would require per-event `Flush` calls and still lose data on SIGSTOP if the writer blocks mid-flush.
- Kernel pipe buffer (64KB default) holds already-written bytes across pause/resume cycles — no user-space buffering means no loss.

### Per-Descent Event Emission

In `internal/plan/verification_descent.go`, define emission points that the `run_cmd.go` wiring subscribes to via the existing `OnLog` callback. Each tier boundary emits one observability event:

| Tier | Event Fields (all under `_stoke.dev/*`) |
|------|------------------------------------------|
| T1 | `{tier:"T1", ac_id, intent_confirmed:bool}` |
| T2 | `{tier:"T2", ac_id, passed:bool}` |
| T3 | `{tier:"T3", ac_id, category, reasoning}` |
| T4 | `{tier:"T4", ac_id, attempt, file_repair_count}` |
| T5 | `{tier:"T5", ac_id, env_fix_applied:bool}` |
| T6 | `{tier:"T6", ac_id, new_command}` |
| T7 | `{tier:"T7", ac_id, refactor_attempted:bool}` |
| T8 | `{tier:"T8", ac_id, all_gates_passed:bool, approval_required:bool}` |

Publishing to bus (`bus.Publish`) uses `Kind: "descent.<tierN>"` for in-process subscribers; publishing to streamjson uses `EmitSystem("descent.tier", ...)`.

### `stoke run` Dispatch (Go code sketch)

```go
// cmd/stoke/run_cmd.go
func runCommand(args []string) int {
    fs := flag.NewFlagSet("run", flag.ContinueOnError)
    var (
        output     = fs.String("output", "", "stream-json")
        repo       = fs.String("repo", "", "repo URL")
        branch     = fs.String("branch", "", "branch name")
        model      = fs.String("model", "", "model override")
        sow        = fs.String("sow", "", "SOW file path")
        hitlT      = fs.Duration("hitl-timeout", 0, "HITL wait override")
        govTier    = fs.String("governance-tier", "community", "community|enterprise")
    )
    if err := fs.Parse(args); err != nil {
        return 2
    }
    taskSpec := strings.Join(fs.Args(), " ")

    // Construct emitter & register graceful shutdown.
    emitter := streamjson.NewTwoLane(os.Stdout, *output == "stream-json")
    defer emitter.Drain(5 * time.Second)

    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()
    installSignalHandler(ctx, cancel, emitter)

    // HITL timeout defaulting.
    timeout := *hitlT
    if timeout == 0 {
        if *govTier == "enterprise" {
            timeout = 15 * time.Minute
        } else {
            timeout = 1 * time.Hour
        }
    }
    hitlSvc := hitl.New(emitter, os.Stdin, timeout)

    emitter.EmitSystem("session.start", map[string]any{
        "_stoke.dev/governance_tier": *govTier,
        "_stoke.dev/repo":            *repo,
        "_stoke.dev/branch":          *branch,
    })
    if workers := os.Getenv("STOKE_MAX_WORKERS"); workers != "" {
        emitter.EmitSystem("concurrency.cap", map[string]any{"_stoke.dev/max_workers": workers})
    }

    // Dispatch.
    switch {
    case *sow != "":
        return dispatchSOW(ctx, *sow, *repo, *branch, *model, emitter, hitlSvc, *govTier)
    case taskSpec != "":
        return dispatchFreeText(ctx, taskSpec, *repo, *branch, *model, emitter, hitlSvc, *govTier)
    default:
        fmt.Fprintln(os.Stderr, "usage: stoke run [--sow PATH | TASK_SPEC]")
        return 2
    }
}
```

### Two-Lane Emitter (Go code sketch)

```go
// internal/streamjson/twolane.go (new file, alongside emitter.go)
type TwoLane struct {
    base     *Emitter           // existing single-lane, used for writeEvent
    critical chan map[string]any
    observ   chan map[string]any
    dropped  atomic.Uint64
    done     chan struct{}
}

func NewTwoLane(w io.Writer, enabled bool) *TwoLane {
    tl := &TwoLane{
        base:     New(w, enabled),
        critical: make(chan map[string]any, 256),
        observ:   make(chan map[string]any, 1024),
        done:     make(chan struct{}),
    }
    go tl.run()
    return tl
}

func (tl *TwoLane) EmitSystem(subtype string, extra map[string]any) {
    evt := map[string]any{"type": "system", "subtype": subtype}
    for k, v := range extra { evt[k] = v }
    if isCritical(subtype) {
        tl.critical <- evt            // blocks intentionally
        return
    }
    select {
    case tl.observ <- evt:
    default:
        select { case <-tl.observ: default: }
        select { case tl.observ <- evt: default: tl.dropped.Add(1) }
    }
}

func (tl *TwoLane) run() {
    ticker := time.NewTicker(5 * time.Second)
    defer ticker.Stop()
    for {
        select {
        case evt, ok := <-tl.critical:
            if !ok { close(tl.done); return }
            tl.base.writeEvent(evt)
        case evt, ok := <-tl.observ:
            if !ok { continue }
            tl.base.writeEvent(evt)
        case <-ticker.C:
            if n := tl.dropped.Swap(0); n > 0 {
                tl.base.writeEvent(map[string]any{
                    "type": "system", "subtype": "stream.dropped",
                    "_stoke.dev/count": n,
                })
            }
        }
    }
}
```

### HITL Service (Go code sketch)

```go
// internal/hitl/hitl.go (new package)
type Request struct {
    Reason       string
    ApprovalType string
    File         string
    Context      map[string]any
}

type Decision struct {
    Approved  bool   `json:"decision"`
    Reason    string `json:"reason"`
    DecidedBy string `json:"decided_by"`
}

type Service struct {
    emitter *streamjson.TwoLane
    stdin   io.Reader
    timeout time.Duration
    mu      sync.Mutex
    waiter  chan Decision
}

func (s *Service) RequestApproval(ctx context.Context, req Request) Decision {
    s.mu.Lock()
    if s.waiter != nil {
        s.mu.Unlock()
        return Decision{Approved: false, Reason: "concurrent_request"}
    }
    s.waiter = make(chan Decision, 1)
    ch := s.waiter
    s.mu.Unlock()
    defer func() { s.mu.Lock(); s.waiter = nil; s.mu.Unlock() }()

    s.emitter.EmitTopLevel("hitl_required", map[string]any{
        "reason":        req.Reason,
        "approval_type": req.ApprovalType,
        "file":          req.File,
        "_stoke.dev/context": req.Context,
    })
    s.startStdinReaderOnce()

    t := time.NewTimer(s.timeout)
    defer t.Stop()
    select {
    case d := <-ch:
        return d
    case <-t.C:
        s.emitter.EmitSystem("hitl.timeout", map[string]any{"_stoke.dev/reason": req.Reason})
        return Decision{Approved: false, Reason: "timeout"}
    case <-ctx.Done():
        return Decision{Approved: false, Reason: "context_canceled"}
    }
}

func (s *Service) startStdinReaderOnce() { /* sync.Once → goroutine scans lines, json.Unmarshal into Decision, sends to s.waiter */ }
```

## Error Handling

| Failure | Strategy | User Sees |
|---------|----------|-----------|
| Malformed HITL JSON on stdin | Discard the bad line, emit `error` subtype `malformed_decision`, continue reading | Event line in observability lane |
| Stdin closed before HITL fires | Auto-reject, exit 3 | `complete` with subtype `error_stdin_closed` |
| Concurrent HITL request | Second caller gets `Approved=false, Reason:"concurrent_request"` | No user-visible side effect; logged |
| Pipe full (CloudSwarm not reading) | Critical events block (intentional); observability drops with counter | `stream.dropped` every 5s |
| SIGSTOP mid-event | No impact — `json.Encoder.Encode` is atomic w.r.t. `write(2)` + kernel pipe buffer holds bytes | N/A |
| SIGINT/SIGTERM | Cancel context, emit `mission.aborted`, drain 5s, exit 130/143 | Final `mission.aborted` line |
| Budget over at startup | Do NOT start; emit `error` subtype `budget_exhausted`, exit 2 | One `error` line + empty result |
| Unknown flag | Print usage to stderr, exit 2 | Stderr usage text |

## Boundaries — What NOT To Do

- Do NOT create `internal/events/` — extend `internal/streamjson/` (C1)
- Do NOT modify `cmd/stoke/ship_cmd.go` or other existing commands' emitter calls
- Do NOT add `bufio.Writer` around `os.Stdout` (breaks SIGSTOP survival, adds Flush calls)
- Do NOT call `os.Stdin.SetDeadline` (broken for Linux pipes per golang/go#24842)
- Do NOT invoke CloudSwarm's policy engine — Stoke is opaque to Cedar (RT-CLOUDSWARM-MAP §4)
- Do NOT read `STOKE_MAX_WORKERS` as a hard limit — it is CloudSwarm-informational only (D10); emit `concurrency.cap` on startup if set
- Do NOT add memory API calls to CloudSwarm (RT-CLOUDSWARM-MAP §7 — not an integration point)
- Do NOT change wire field names on existing `system`/`assistant`/`user`/`result`/`stream_event` events (backward compat)
- Do NOT rewrite `runSessionNative` — `run --sow` delegates to it unchanged
- Do NOT fork `stoke ship` — `run` is a new sibling command

## Testing

### `internal/streamjson/TwoLane`
- [ ] Happy: emit 10 observability events → all 10 appear on stdout in order
- [ ] Happy: emit `hitl_required` → appears immediately even under observability back-pressure
- [ ] Drop-oldest: fill observ channel to 1024, emit 10 more → dropped counter ≥10, `stream.dropped` emitted next tick
- [ ] Atomicity: 50 goroutines emit 100 events each → 5000 lines, each a valid standalone JSON object, no interleaved bytes
- [ ] No Flush required: pipe stdout to `cat` → all lines appear without process exit
- [ ] SIGSTOP survival: SIGSTOP the process mid-stream, read remaining kernel buffer from reader side → all pre-SIGSTOP lines readable

### `internal/hitl/Service`
- [ ] Happy: emit request, write `{"decision":true,...}` to stdin → returns `Approved=true`
- [ ] Timeout: emit request, don't write → after `timeout`, returns `Approved=false, Reason:"timeout"`
- [ ] Malformed line: write `not json\n` → discarded, pending request remains open, next valid line still delivers decision
- [ ] Stdin EOF: close stdin while waiting → returns `Approved=false, Reason:"stdin_closed"`
- [ ] Concurrent: second `RequestApproval` returns `Reason:"concurrent_request"` while first waits
- [ ] Context cancel: cancel ctx while waiting → returns `Reason:"context_canceled"`
- [ ] Governance timeout defaults: enterprise=15m, community=1h, override via `--hitl-timeout` wins

### `cmd/stoke/run_cmd.go`
- [ ] Usage: `stoke run` with no args → stderr usage, exit 2
- [ ] SOW path: `stoke run --sow /tmp/test.md --output stream-json` → first line is `session.start`, last line is `complete`
- [ ] Free-text path: `stoke run --output stream-json "build a server"` → chat-intent classifier routes, session proceeds
- [ ] Exit codes: successful run → 0; failed AC → 1; budget exhausted → 2; HITL reject → 3; SIGINT → 130
- [ ] Flag parity: all 7 flags listed above accept values and propagate to downstream config
- [ ] `--output` absent → emitter disabled (existing no-op behavior)

### Contract Tests (parse-compatibility with CloudSwarm fixtures)

Per `platform/temporal/activities/test_execute_stoke.py:149-154`, CloudSwarm's fixture asserts lines with `type` OR `event_type` parse and contain recognized payloads. New tests in `internal/streamjson/contract_test.go`:

- [ ] Every emitted line is valid JSON per `json.Valid`
- [ ] Every emitted line ends with exactly one `\n`
- [ ] Every line contains `type` string field
- [ ] Every `hitl_required` line contains `reason` string field (CloudSwarm reads at `execute_stoke.py:343-387`)
- [ ] Terminal `complete` is the last line emitted before exit
- [ ] Fixtures: `testdata/cloudswarm_fixtures/hitl_required.jsonl`, `task_complete.jsonl`, `descent_tier.jsonl` — reproduce the exact shapes CloudSwarm stores under `stoke_events.data`

## Acceptance Criteria

- WHEN `stoke run --help` is invoked THE SYSTEM SHALL print usage text containing `--output`, `--repo`, `--branch`, `--model`, `--sow`, `--governance-tier`, `--hitl-timeout`
- WHEN `stoke run --output stream-json "hello"` is invoked THE SYSTEM SHALL emit NDJSON lines with a first line carrying `"type":"system"` and `"subtype":"session.start"`
- WHEN `stoke run --output stream-json --sow PATH` successfully completes THE SYSTEM SHALL emit a final line with `"type":"complete"` and exit 0
- WHEN a worker invokes `hitl.RequestApproval` THE SYSTEM SHALL emit one line with `"type":"hitl_required"` and block until stdin delivers a matching JSON decision or timeout fires
- WHEN stdin delivers `{"decision":true,...}` within the timeout THE SYSTEM SHALL resume the worker with `Approved=true`
- WHEN the HITL timeout elapses with no stdin input THE SYSTEM SHALL emit `hitl.timeout` observability event, auto-reject, and (if at T8) return `FAIL` from descent
- WHEN `SIGINT` is received THE SYSTEM SHALL emit `mission.aborted`, drain the emitter for up to 5s, and exit 130
- WHEN budget is exhausted at entry THE SYSTEM SHALL emit `error` with subtype `budget_exhausted` and exit 2
- WHEN `STOKE_MAX_WORKERS` is set THE SYSTEM SHALL emit one `concurrency.cap` event during startup
- WHEN `--governance-tier=enterprise` is set AND descent reaches T8 soft-pass THE SYSTEM SHALL call `hitl.RequestApproval` and honor the decision (reject → AC fails, approve → soft-pass granted)
- WHEN `--governance-tier=community` is set (default) AND descent reaches T8 soft-pass THE SYSTEM SHALL auto-grant (current behavior, `SoftPassApprovalFunc` nil)
- WHEN 50 goroutines concurrently call `emitter.EmitSystem` THE SYSTEM SHALL produce 50×N lines each individually valid JSON with no interleaved bytes

### AC commands (bash)

```
./stoke run --help | grep -q -- '--output'
./stoke run --help | grep -q -- '--sow'
./stoke run --help | grep -q -- '--governance-tier'
./stoke run --output stream-json "hello" | head -1 | jq -e '.type == "system" and .subtype == "session.start"'
./stoke run --output stream-json --sow testdata/min.sow.md | tail -1 | jq -e '.type == "complete"'
go test ./internal/hitl/... -run TestHITLTimeout -v
go test ./internal/hitl/... -run TestHITLDecisionHappy -v
go test ./internal/hitl/... -run TestHITLStdinClosed -v
go test ./internal/streamjson/... -run TestTwoLaneEmitter -v
go test ./internal/streamjson/... -run TestTwoLaneDropOldest -v
go test ./internal/streamjson/... -run TestCloudSwarmFixtures -v
printf '%s\n' '{"decision":true,"reason":"ok","decided_by":"test"}' | ./stoke run --output stream-json --test-hitl-roundtrip | jq -e 'select(.subtype=="hitl.decision") | .["_stoke.dev/approved"]' | grep -q true
```

## Implementation Checklist

1. [ ] **Create `internal/streamjson/twolane.go`** (new file alongside existing `emitter.go`): `TwoLane` struct with `critical chan` (256) and `observ chan` (1024), background `run()` goroutine, atomic `dropped` counter, 5s ticker emitting `stream.dropped`. Wrap existing `*Emitter.writeEvent`. Expose `EmitSystem(subtype, extra)`, `EmitTopLevel(type, extra)`, `Drain(timeout)`, `Close()`. Mutex-serialized writes via existing `Emitter.writeEvent`. NO bufio.Writer. Tests in `twolane_test.go` covering: ordering, drop-oldest, concurrent emit atomicity, drain behavior. Pattern: matches RT-04 §8 sketch. Error: all marshal errors fall through existing `writeEvent` fallback path.

2. [ ] **Extend `internal/streamjson/emitter.go`** with helper `EmitTopLevel(type string, extra map[string]any)` for non-system top-level events (`hitl_required`, `error`, `complete`, `mission.aborted`). Keep existing 5 entrypoints unchanged. Do NOT rename any fields. Add package-level consts: `TypeHITLRequired = "hitl_required"`, `TypeComplete = "complete"`, `TypeError = "error"`, `TypeMissionAborted = "mission.aborted"`.

3. [ ] **Create `internal/hitl/` package**: `hitl.go` (Service struct, Request/Decision types, `RequestApproval`, stdin reader sync.Once), `hitl_test.go`. Service holds `emitter *streamjson.TwoLane`, `stdin io.Reader`, `timeout time.Duration`, `mu sync.Mutex`, `waiter chan Decision`. Stdin reader pattern per RT-04 §7 (bufio.Reader + ReadBytes('\n')). Timer via `time.NewTimer`. Malformed JSON → emit observability error and continue reading the next line. EOF → auto-reject.

4. [ ] **Add `SoftPassApprovalFunc` to `internal/plan/verification_descent.go` `DescentConfig`** (insert near line 289 preserving nil-default semantics): `SoftPassApprovalFunc func(ctx context.Context, ac AcceptanceCriterion, verdict ReasoningVerdict) bool`. In T8 block (lines 736-820), after all 6 gates pass, if `SoftPassApprovalFunc != nil`, call it; if it returns false, return FAIL instead of soft-pass. Emit `descent.resolve` observability event with `approval_required:true, approved:<result>`. Default (nil) behavior unchanged — auto-grant.

5. [ ] **Extend `cmd/stoke/descent_bridge.go::buildDescentConfig`** to accept a `governanceTier string` + `hitlSvc *hitl.Service` parameter. When tier=="enterprise": set `dc.SoftPassApprovalFunc = func(ctx, ac, v) bool { d := hitlSvc.RequestApproval(ctx, hitl.Request{...}); return d.Approved }`. Else: leave nil. Also upgrade existing `dc.OnLog` (lines 282-284) to emit structured `descent.tier` events via the emitter + `bus.Publish` while keeping the existing `fmt.Printf` terminal output for local debugging.

6. [ ] **Create `cmd/stoke/run_cmd.go`**: new subcommand. Flag parsing (7 flags), dispatch logic (SOW vs free-text), emitter construction, signal handler install, `session.start`/`concurrency.cap`/`session.complete`/`complete` emission, exit code computation (0/1/2/3/130/143). Free-text path calls `chat.ClassifyIntent`; if `IntentQuery`/unknown, synthesize ephemeral SOW via chat and feed to `runSessionNative`. SOW path calls `runSessionNative` directly with emitter + hitlSvc wired in. Register with main.go command dispatcher alongside `ship`, `build`, etc.

7. [ ] **Extend `cmd/stoke/main.go` command dispatcher** to route `"run"` → `runCommand(args)`. Preserve all existing command registrations. Ensure `stoke run --help` prints descriptive usage.

8. [ ] **Graceful shutdown in `run_cmd.go`**: `signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)` in a goroutine; on signal → cancel root `context.Context` → emit `mission.aborted` on critical lane → call `emitter.Drain(5 * time.Second)` → `os.Exit(130)` for SIGINT, `143` for SIGTERM. Must NOT exit from the signal handler itself.

9. [ ] **Wire emitter + hitlSvc through `runSessionNative` call path**: thread `*streamjson.TwoLane` and `*hitl.Service` as new optional fields on `sowNativeConfig` (existing struct in `cmd/stoke/sow_native.go`). Emit `plan.ready` after SOW parse, `task.dispatch` per task entry, `ac.result` per AC check, `task.complete` per task exit. Non-breaking: nil emitter/hitlSvc = no-op.

10. [ ] **Contract test fixtures**: create `internal/streamjson/testdata/cloudswarm_fixtures/*.jsonl` with exact shapes from RT-CLOUDSWARM-MAP §2-3 (`hitl_required.jsonl`, `task_complete.jsonl`, `descent_tier.jsonl`). Contract test `TestCloudSwarmFixtures` emits each event type, captures stdout, and asserts the fixture line matches (modulo uuid/timestamp fields, which are checked for presence and format only).

11. [ ] **Update `cmd/stoke/main.go` help text** to list `run` in the command table alongside `ship`, `build`, etc. One-line description: `"run    Execute a task or SOW with streaming events (CloudSwarm-compatible)"`.

12. [ ] **Integration test `cmd/stoke/run_cmd_test.go`**: shells out to built binary with `--output stream-json`, captures stdout, asserts event ordering (first `session.start`, last `complete`), validates every line is valid JSON, validates exit code mapping for each of the 6 conditions (0/1/2/3/130/143). Uses `os/exec.Command` with pipe stdin/stdout.

13. [ ] **Document in `docs/cloudswarm.md`**: one-page operator doc — how CloudSwarm invokes `stoke run`, the HITL wire format (plain JSON on stdin), the exit codes, the event namespace. Link from `README.md`. (This is the only new doc; the spec itself is not a doc.)

## D-9: Policy Engine (fail-closed authorization)

Stoke consults an authorization policy before executing any native tool call (bash, file_read, file_write, mcp_*). The policy layer is fail-closed: transport errors, timeouts, 5xx responses, malformed bodies, and zero-value results all resolve to Deny.

Two backends share the `policy.Client` interface:

1. **Tier 1 (local YAML)** — `STOKE_POLICY_FILE=/path/to/policy.yaml`
   Rules evaluated top-to-bottom, first match wins. 8 predicates: `matches`, `startswith`, `equals`, `in`, `>=`, `<=`, `>`, `<`, composable via AND.
2. **Tier 2 (Cedar-agent HTTP)** — `CLOUDSWARM_POLICY_ENDPOINT=https://...`
   PARC body (Principal / Action / Resource / Context) to `/v1/is_authorized`. Bearer auth via `CLOUDSWARM_POLICY_TOKEN`. 2 s default timeout.

Precedence if both env vars are set: tier 2 (cedar-agent) wins. If neither is set, Stoke falls back to `NullClient` which prints a one-line dev-mode banner and allows all actions.

Events emitted on every Check: `stoke.policy.check` (decision + latency + backend) and on every deny: `stoke.policy.denied` (principal + action + resource + reasons). Both stream through the same NDJSON emitter used for tool calls and mission lifecycle.

Operator tooling: `stoke policy validate <file.yaml>`, `stoke policy test <file.yaml> "principal=… action=… resource=…"`, `stoke policy trace --last-N N [--log path]`.

See `specs/policy-engine.md` (build order 10) for the full spec and the `internal/policy` package for the implementation.
