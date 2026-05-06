<!-- STATUS: done -->
<!-- BUILD_COMPLETED: 2026-04-22 -->
<!-- CREATED: 2026-04-20 -->
<!-- DEPENDS_ON: spec-2 (preferred, for event emission targets) but implementable standalone -->
<!-- BUILD_ORDER: 3 -->

# Executor Foundation — Implementation Spec

## Overview
The foundation that unblocks Tier-3 executors (browser, research, delegate, deploy). Introduces three new packages — `internal/eventlog/` (SQLite+WAL session log per RT-05), `internal/executor/` (a task-type-agnostic `Executor` interface plus a `CodeExecutor` wrapper over the existing SOW flow), and `internal/router/` (lightweight `Classify` + Intent Gate per RT-11 / Factory DROID). It also generalizes `AcceptanceCriterion` by adding a `VerifyFunc` field (RT-STOKE-SURFACE §1, D13) and makes executors publish lifecycle events onto the existing-but-unused `internal/bus/`. No behavioural change for today's `stoke ship --sow`; the surface is additive and discoverable so spec-4/5/6 can implement Research/Browser/Delegate/Deploy executors without touching the descent engine.

## Stack & Versions
- Go 1.22 (existing `go.mod`)
- SQLite via `modernc.org/sqlite` (already vendored by `internal/session/sqlstore.go`)
- ULID via `github.com/oklog/ulid/v2` (add dependency)
- No new LLM model calls in this spec; Haiku fallback in router uses existing `model.Resolve` / `provider.Provider`

## Existing Patterns to Follow
- SQLite store with WAL + `busy_timeout`: `internal/session/sqlstore.go`
- Bus publish shape: `internal/bus/bus.go`, event types at lines 31-69
- Provider abstraction for Haiku classifier: `internal/provider/`, `internal/model/`
- Acceptance criterion struct: `internal/plan/sow.go:87-96` (extend, do not replace)
- `runACCommand`: `internal/plan/verification_descent.go:829-832` (extend to honor `VerifyFunc`)
- Harness tool authorization (used by Intent Gate DIAGNOSE mode): `internal/harness/tools/`
- Existing SOW entry point wrapped by `CodeExecutor`: `cmd/r1/sow_native.go` (specifically `execNativeTask` / `NativeRunner.Run`)

## Library Preferences
- SQLite driver: `modernc.org/sqlite` (already used)
- ULID: `github.com/oklog/ulid/v2`
- JSON canonicalization for hash chain: `encoding/json` with sorted-keys marshalling in `eventlog/canonical.go` (do NOT pull `jsonc`/external libs)
- Hashing: `crypto/sha256` stdlib
- Iterator for `Read(...)`: Go 1.23 `iter.Seq[Event]` — if build target is Go 1.22, fall back to channel iterator with context cancel

## Data Models

### `eventlog.Event`
| Field      | Type              | Constraints                                            | Default       |
|------------|-------------------|--------------------------------------------------------|---------------|
| `ID`       | string (ULID)     | PK, sortable, monotonic per process                    | newly minted  |
| `TS`       | time.Time         | UTC, nanosecond, stored as `INTEGER` unix-nanos        | `time.Now().UTC()` |
| `SessionID`| string            | NOT NULL, indexed                                      | required      |
| `BranchID` | string            | NOT NULL                                               | `"main"`      |
| `Type`     | string            | NOT NULL, one of the event-type enum                   | required      |
| `CallID`   | string            | tool-call correlation; empty if n/a                    | `""`          |
| `ParentID` | string            | causality chain, empty at root                         | `""`          |
| `Data`     | json.RawMessage   | canonical JSON, ≤ 1 MiB                                | `{}`          |
| `Hash`     | string (64-hex)   | `SHA256(prev.Hash ‖ canonical(event_without_hash))`    | computed      |

### SQLite DDL (eventlog/schema.sql)

```sql
-- One DB file: .stoke/events.db. Single table. Single writer. Many readers.
PRAGMA journal_mode = WAL;
PRAGMA synchronous  = NORMAL;
PRAGMA busy_timeout = 5000;
PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS events (
    id          TEXT    PRIMARY KEY,                -- ULID
    ts          INTEGER NOT NULL,                   -- unix nanoseconds
    session_id  TEXT    NOT NULL,
    branch_id   TEXT    NOT NULL DEFAULT 'main',
    type        TEXT    NOT NULL,
    call_id     TEXT    NOT NULL DEFAULT '',
    parent_id   TEXT    NOT NULL DEFAULT '',
    data        BLOB    NOT NULL,                   -- canonical JSON bytes
    hash        TEXT    NOT NULL                    -- SHA256 hex, chained
);

CREATE INDEX IF NOT EXISTS ix_events_session_id   ON events(session_id, id);
CREATE INDEX IF NOT EXISTS ix_events_session_type ON events(session_id, type);
CREATE INDEX IF NOT EXISTS ix_events_call_id      ON events(call_id) WHERE call_id != '';
CREATE INDEX IF NOT EXISTS ix_events_parent_id    ON events(parent_id) WHERE parent_id != '';

-- Per-session hash-chain head pointer. One row per (session_id, branch_id).
CREATE TABLE IF NOT EXISTS event_heads (
    session_id TEXT NOT NULL,
    branch_id  TEXT NOT NULL,
    head_id    TEXT NOT NULL,
    head_hash  TEXT NOT NULL,
    PRIMARY KEY (session_id, branch_id),
    FOREIGN KEY (head_id) REFERENCES events(id)
);
```

### Event-type enum (initial, extensible)

```
session.start       session.end
task.dispatch       task.complete         task.fail
llm.turn
tool.call           tool.result
verify.tier         verify.start          verify.end
worktree.merge
context.compact
branch.fork         branch.merge
failure
```

Unknown types are accepted (no CHECK constraint) so Tier-3 executors can add subtypes without schema churn. A registry in `eventlog/types.go` holds the canonical list for docs + validation.

### `executor.Executor` (full Go definition)

```go
// internal/executor/executor.go
package executor

import (
    "context"

    "github.com/anthropics/stoke/internal/plan"
)

// Effort is a coarse knob per D12. Keep the set small.
type Effort string

const (
    EffortLow    Effort = "low"
    EffortMed    Effort = "medium"
    EffortHigh   Effort = "high"
)

// Deliverable is what an executor produced (files, URLs, report paths, …).
// Shape is intentionally open so Research / Deploy can embed their own payloads.
type Deliverable struct {
    Kind       string            // "code", "research_report", "deploy_url", ...
    Files      []string          // paths produced or mutated
    Payload    map[string]any    // executor-specific
    Summary    string            // human-readable one-liner
}

// RepairFunc and EnvFixFunc mirror the descent engine's existing callback shape.
// An executor returns concrete ones so descent T4/T5/T7 can dispatch.
type RepairFunc func(ctx context.Context, directive string) error
type EnvFixFunc func(ctx context.Context, cause, stderr string) bool

type Executor interface {
    // Execute performs the task and returns a deliverable. Publishes
    // task.dispatch on entry and task.complete / task.fail on exit.
    Execute(ctx context.Context, p *plan.SOW, effort Effort) (Deliverable, error)

    // BuildRepairFunc wires the descent engine's T4/T7 dispatcher for this task type.
    BuildRepairFunc(p *plan.SOW) RepairFunc

    // BuildEnvFixFunc wires the descent engine's T5 dispatcher.
    BuildEnvFixFunc() EnvFixFunc

    // BuildCriteria converts a task + deliverable into the AC set fed into descent.
    // Code executor returns Command-based ACs; Research returns VerifyFunc-based ones (spec-4).
    BuildCriteria(task plan.Task, d Deliverable) []plan.AcceptanceCriterion
}
```

### `router.TaskType` and Intent

```go
// internal/router/router.go
package router

type TaskType string

const (
    TaskCode     TaskType = "code"
    TaskResearch TaskType = "research"
    TaskBrowser  TaskType = "browser"
    TaskDeploy   TaskType = "deploy"
    TaskDelegate TaskType = "delegate"
    TaskChat     TaskType = "chat"
)

type Intent string

const (
    IntentImplement Intent = "IMPLEMENT"
    IntentDiagnose  Intent = "DIAGNOSE"
    IntentAmbiguous Intent = "AMBIGUOUS"
)
```

## API / Package Surface

### `internal/eventlog/`

```go
package eventlog

type Log interface {
    Append(ctx context.Context, ev Event) error
    Read(ctx context.Context, sessionID, afterID string) iter.Seq[Event]
    Replay(ctx context.Context, sessionID string) (State, error)
    Close() error
}

type State struct {
    LastEventID    string
    LastEventHash  string
    Messages       []llm.Message      // rehydrated since last context.compact
    OpenToolCalls  map[string]Event   // call_id → tool.call with no matching tool.result
    Branch         string
}

// Open returns a Log backed by SQLite at dbPath (creates if absent).
// Honors STOKE_EVENTLOG_JSONL=1 — when set, mirrors Append to
// <dbPath>.jsonl (JSONL, one event per line, for jq inspection).
func Open(dbPath string) (Log, error)
```

**Append semantics**
1. Begin transaction.
2. `SELECT head_hash FROM event_heads WHERE session_id=? AND branch_id=?`.
3. Compute `ev.ID = ulid.Make()`, `ev.Hash = sha256(head_hash ‖ canonical(ev_minus_hash))`.
4. `INSERT INTO events`.
5. `INSERT OR REPLACE INTO event_heads`.
6. Commit. If `STOKE_EVENTLOG_JSONL=1`, append one canonical-JSON line to `<dbPath>.jsonl` after the SQLite commit (SQLite is authoritative; JSONL is advisory).

**Read semantics**
- `iter.Seq[Event]` yields rows `WHERE session_id=? AND id > ? ORDER BY id`. Cancellable via ctx.
- Large sessions: use `LIMIT 1000` batching inside the iterator to avoid loading the whole table in memory.

### `internal/executor/`

```go
// internal/executor/code.go
package executor

// CodeExecutor wraps the existing SOW flow. It does NOT re-implement planning,
// descent, or verification — it calls into sow_native.go.
type CodeExecutor struct {
    Bus      bus.Publisher    // from internal/bus
    EventLog eventlog.Log
    // Runner is a function injected at wiring time; in main.go it is
    // set to execNativeTask so this package has no import cycle with cmd/.
    Runner   func(ctx context.Context, in sow.NativeInput) (sow.NativeOutput, error)
}

func (c *CodeExecutor) Execute(ctx context.Context, p *plan.SOW, effort Effort) (Deliverable, error)
func (c *CodeExecutor) BuildRepairFunc(p *plan.SOW) RepairFunc
func (c *CodeExecutor) BuildEnvFixFunc() EnvFixFunc
func (c *CodeExecutor) BuildCriteria(task plan.Task, d Deliverable) []plan.AcceptanceCriterion
```

Lifecycle events published by every `Executor.Execute` (not just Code):

| Event            | When                                   | Data payload                                      |
|------------------|----------------------------------------|---------------------------------------------------|
| `task.dispatch`  | start of Execute                       | `{task_id, kind, effort, plan_id}`                |
| `verify.start`   | before AC loop                         | `{task_id, ac_count}`                             |
| `verify.end`     | after AC loop                          | `{task_id, passed, soft_passed, failed_ids}`      |
| `task.complete`  | end of Execute, err==nil               | `{task_id, files: [...], duration_ms, cost_usd}`  |
| `task.fail`      | end of Execute, err!=nil               | `{task_id, err_class, fingerprint}`               |

These are published through `internal/bus/` AND written to the event log (single call in a helper — `eventlog.EmitBus(bus, log, ev)`). This is the point where the bus finally has publishers (RT-STOKE-SURFACE §12).

### `internal/router/`

```go
func Classify(ctx context.Context, input string, p provider.Provider) (TaskType, error)
func ClassifyIntent(ctx context.Context, input string, p provider.Provider) (Intent, error)

// Deterministic-only (no LLM); returns zero-value + false if ambiguous.
func ClassifyDeterministic(input string) (TaskType, bool)
func ClassifyIntentDeterministic(input string) (Intent, bool)
```

Callers:
- `stoke chat` (free-text) → `Classify`
- `stoke run TASK` (spec-2 free-text) → `Classify`
- `stoke ship --sow` → DOES NOT call the router (bypasses, routes directly to `CodeExecutor`)
- Scheduler pre-dispatch → `ClassifyIntent` (Intent Gate, D29)

## Verb-scan regex tables

### TaskType (Classify) — deterministic phase

```
TaskResearch:  ^(?i)\s*(research|investigate|survey|find out about|literature review)\b
TaskBrowser:   ^(?i)\s*(browse|open|navigate|visit|screenshot|scrape|click|check (the )?(page|site|url))\b
TaskDeploy:    ^(?i)\s*(deploy|ship to (fly|vercel|cloudflare|netlify)|rollout|publish (to )?prod)\b
TaskDelegate:  ^(?i)\s*(delegate|hire|subcontract|hand off (to|this to))\b
TaskChat:      ^(?i)\s*(hi|hello|hey|what('s| is) up|thanks|explain|tell me)\b
TaskCode:      ^(?i)\s*(add|implement|fix|refactor|rename|migrate|port|create|build|generate|write( (a|the|some))?\s+(function|test|module|handler|type|struct|package))\b
```

Scan order: Deploy → Research → Browser → Delegate → Code → Chat. First match wins. No match → return `("", false)`; caller may fall through to Haiku via `Classify`.

### Intent (ClassifyIntent) — deterministic phase, per RT-11 §3 / Factory DROID

```
IntentImplement: (?i)\b(create|add|implement|fix|update|build|deploy|generate|refactor|rename|migrate|port|delete|write)\b
IntentDiagnose:  (?i)\b(check|verify|investigate|analyze|explain|audit|review|diagnose|what|how|why|where|when|which)\b
```

Precedence: if BOTH families match (e.g. "investigate and fix X"), return IMPLEMENT (the more restrictive tool scope superset). Zero matches → `AMBIGUOUS`, caller fires Haiku via `ClassifyIntent`.

### Haiku fallback prompt (LLM phase, both Classify and ClassifyIntent)

Tight single-turn: `"Classify the user's task. For TYPE answer exactly one of: code | research | browser | deploy | delegate | chat. For INTENT answer exactly one of: IMPLEMENT | DIAGNOSE | AMBIGUOUS. Input: <...>"` — two fields, reject any response not in the enum. Used only after deterministic phase returns `false`.

## AcceptanceCriterion change

File: `internal/plan/sow.go:87-96`. Add one field.

```go
type AcceptanceCriterion struct {
    ID          string
    Description string
    Command     string
    FileExists  string
    ContentMatch *ContentMatchCriterion

    // VerifyFunc is an in-process verifier. Not serialized; populated by
    // executors at BuildCriteria time. When BOTH Command and VerifyFunc are
    // set, Command wins (backward compatibility — existing ACs unchanged).
    VerifyFunc func(ctx context.Context) (passed bool, output string) `json:"-"`
}
```

### Backward-compat matrix (what `runACCommand` does)

| Command | FileExists | ContentMatch | VerifyFunc | Behavior                                                 |
|---------|------------|--------------|------------|----------------------------------------------------------|
| set     | any        | any          | any        | run Command (exit 0 = pass) — existing path              |
| unset   | set        | any          | any        | existing FileExists probe                                |
| unset   | unset      | set          | any        | existing ContentMatch path                               |
| unset   | unset      | unset        | set        | **new**: invoke VerifyFunc; passed bool is verdict       |
| unset   | unset      | unset        | unset      | existing error: "empty criterion"                        |

`runACCommand` (verification_descent.go:829) gains one new branch at the top:

```go
if ac.Command == "" && ac.FileExists == "" && ac.ContentMatch == nil && ac.VerifyFunc != nil {
    ok, out := ac.VerifyFunc(ctx)
    return out, ok
}
// existing logic unchanged
```

This is the single hook that makes descent task-type-agnostic (D13). Research executor in spec-4 supplies VerifyFunc-only ACs (URL fetch + LLM-judge); descent then runs the same 8-tier ladder over them.

## Intent Gate integration (RT-11 §7, D29)

File: `internal/router/intent_gate.go`.

```go
// Gate is called by the scheduler before harness.Spawn on every task dispatch.
// Returns a TaskIntent and a (possibly-modified) tool set that the harness
// must pass to the worker.
func Gate(ctx context.Context, task plan.Task, tools harnessTools.Set, p provider.Provider) (Intent, harnessTools.Set, error)
```

- If IMPLEMENT → pass through tool set unchanged.
- If DIAGNOSE → return a clamped set from `harnessTools.ReadOnly(tools)` — strips any tool whose `Write == true` per the tool-authorization model. Enforcement is at the harness layer (not prompt), so a worker ignoring the prompt still cannot write.
- If AMBIGUOUS → default to DIAGNOSE (safer; matches RT-11 open-question 4 recommendation). Emit `bus` event `intent.ambiguous` with the task title so operators can observe.

DIAGNOSE output lands as a markdown report in `reports/<task-id>.md`, not in the source tree (RT-11 §7).

## Business Logic — Replay algorithm (pseudocode)

Per RT-05 §5:

```
func Replay(sessionID) State:
    cursor = db.Query(
        "SELECT id FROM events WHERE session_id=? AND type='context.compact'
         ORDER BY id DESC LIMIT 1", sessionID)
    start = cursor or db.Query("SELECT id FROM events WHERE session_id=?
                                AND type='session.start' ORDER BY id LIMIT 1")

    state = State{}
    open  = map[string]Event{}   // call_id -> tool.call with no result

    for ev in db.Stream("SELECT * FROM events WHERE session_id=? AND id >= ?
                         ORDER BY id", sessionID, start):
        if ev.Type == "context.compact":
            state.Messages = []      // reset window; older history stays on disk
            state.Messages.append(summary(ev.Data))
            continue
        if ev.Type == "llm.turn":
            state.Messages.append(fromLLMTurn(ev.Data))
            continue
        if ev.Type == "tool.call":
            open[ev.CallID] = ev
            state.Messages.append(fromToolCall(ev.Data))
            continue
        if ev.Type == "tool.result":
            delete(open, ev.CallID)
            state.Messages.append(fromToolResult(ev.Data))
            continue
        // other types: fold into state as needed by type
        state.LastEventID   = ev.ID
        state.LastEventHash = ev.Hash

    // Orphan handling: tool.call with no matching tool.result at wake time.
    for call_id, ev in open:
        if isIdempotent(ev.Data.name):
            // caller re-dispatches; Append is idempotent on (session_id, call_id, type='tool.call')
            enqueueRedispatch(ev)
        else:
            // default for worktree-mutating tools (D12 safety)
            synthetic = Event{
                Type: "tool.result", CallID: call_id,
                Data: {"status":"unknown","error":"harness_crashed"}}
            Append(synthetic)
            state.Messages.append(fromToolResult(synthetic.Data))

    state.OpenToolCalls = {}   // drained after handling
    return state
```

Idempotence table (`eventlog/idempotent.go`):

| Tool name prefix          | Idempotent? |
|---------------------------|-------------|
| `read_*`, `grep`, `glob`  | yes (replay OK) |
| `bash` (allowed readonly) | yes         |
| `browser_extract_text`    | yes         |
| `write_*`, `edit_*`, `apply_patch` | **no** — synthesize `{status:"unknown"}` |
| `git commit`, `git merge` | **no**      |
| `pnpm install`, `npm install` | no    |
| anything else             | no (default side-effectful) |

## Concurrency

- SQLite WAL permits **unlimited readers + exactly one writer**. Writes are serialized via `BEGIN IMMEDIATE` + `busy_timeout=5000ms`.
- Expected write rate: ≤ 50 events/sec/session under peak (LLM turns are the throttle, ~1/sec). With typical 4 concurrent missions, total ≤ 200/sec — well below the 10k writes/sec SQLite+WAL handles in `fly.io/blog/sqlite-internals-wal`.
- Cross-process safety: `stoke ship` and a running `stoke serve` (spec-5) may both write. Both hit the same `.stoke/events.db` with `busy_timeout=5000` + retry-on-`SQLITE_BUSY` (3 retries, exponential backoff 10/40/160ms). Retries logged to bus as `eventlog.busy_retry`.
- In-process serialization: `eventlog.Log` wraps the DB handle in a `sync.Mutex` that serializes `Append` calls (single-writer-per-process) to avoid wasting contention on the SQLite lock. Read iterators take a read-only handle and never block writers.
- Hash chain correctness under concurrency: the chain is per `(session_id, branch_id)`. Different sessions write independent chains; the `event_heads` PK guarantees serialized head updates per session regardless of global ordering.

## Boundaries — What NOT To Do
- Do NOT rewrite SOW execution. `CodeExecutor` calls into existing `cmd/r1/sow_native.go` via injected `Runner`.
- Do NOT ship browser / research / deploy / delegate executors in this spec. That is spec-4/5/6.
- Do NOT wire streamjson emission in this spec. That is spec-2.
- Do NOT integrate memory retrieval. That is spec-7.
- Do NOT add the per-file repair cap — lives in spec-1 (D2).
- Do NOT change `AcceptanceCriterion.UnmarshalJSON`. The `VerifyFunc` field is tagged `json:"-"` and set only in-process by executors.
- Do NOT default-on `STOKE_EVENTLOG_JSONL`. Opt-in flag only.
- Do NOT couple the router to the scheduler's intent/verbalize gate (`internal/intent/`). They are separate concerns; intent-gate may consult intent/verbalize later but not in this spec.
- Do NOT import `cmd/r1/*` from `internal/executor/`. Use a `Runner func(...)` injected at wiring time to avoid an import cycle.

## Error Handling

| Failure                                        | Strategy                                                 | Caller Sees                       |
|------------------------------------------------|----------------------------------------------------------|-----------------------------------|
| SQLite `SQLITE_BUSY` on Append                 | retry 3× 10/40/160ms, then fail                          | `eventlog: busy` wrapped error    |
| SQLite disk full                               | Append fails; Runner MUST NOT proceed                    | `eventlog: disk full`             |
| Hash chain mismatch detected on Replay         | return `ErrChainBroken`; caller may refuse to continue   | CI gate flag `--strict-eventlog`  |
| Orphan `tool.call` on wake, non-idempotent     | synthesize `{"status":"unknown"}`                        | logged warn, replay continues     |
| Router Haiku call fails                        | fall back to `TaskCode` + `IntentDiagnose` (safe default)| warn on bus `router.fallback`     |
| Intent Gate AMBIGUOUS                          | DIAGNOSE mode; tools clamped; report-only                | bus `intent.ambiguous`            |
| Executor.Execute ctx cancelled                 | publish `task.fail{err_class:"cancelled"}` then return   | callers handle ctx.Err()          |

## Testing

### `internal/eventlog/`
- [ ] Happy: Append 3 events in order → Read yields them with monotonic ULIDs and chained hashes
- [ ] Hash chain: Append 10 events; flip one byte in `data` column via raw SQL; Replay returns `ErrChainBroken`
- [ ] Replay: session with `context.compact` mid-stream → Replay's `state.Messages` starts at the compact marker, not session.start
- [ ] Orphan tool.call (idempotent tool): Replay marks it for redispatch, does not synthesize
- [ ] Orphan tool.call (edit tool): Replay synthesizes `{status:"unknown"}`
- [ ] Concurrent writers: 4 goroutines × 100 Appends each on distinct session_ids → 400 rows, all chains valid
- [ ] JSONL mirror: `STOKE_EVENTLOG_JSONL=1` → `.stoke/events.db.jsonl` matches SQLite row-for-row
- [ ] Cross-session FK: event with `parent_id` referencing another session's event → Read joins correctly

### `internal/executor/`
- [ ] CodeExecutor.Execute publishes `task.dispatch` + `task.complete` on success (fake bus + fake runner)
- [ ] CodeExecutor.Execute publishes `task.fail` on runner error (non-nil err propagated)
- [ ] BuildRepairFunc returns a non-nil RepairFunc that wraps descent repair dispatch
- [ ] BuildCriteria for a code task returns Command-based ACs (VerifyFunc nil) — backward compat

### `internal/router/`
- [ ] Classify("deploy to fly.io") → TaskDeploy, no LLM call
- [ ] Classify("research the best Go browser lib") → TaskResearch, no LLM call
- [ ] Classify("add a function to foo.go") → TaskCode, no LLM call
- [ ] Classify("hmm make it better") → deterministic miss; Haiku called; returns enum member
- [ ] ClassifyIntent("implement foo") → IntentImplement
- [ ] ClassifyIntent("why is the build red") → IntentDiagnose
- [ ] ClassifyIntent("investigate and fix the leak") → IntentImplement (both match, Implement wins)
- [ ] ClassifyIntent("make it go") → AMBIGUOUS; Gate clamps tools to read-only

### `internal/plan/` (VerifyFunc backward-compat)
- [ ] AC with Command="true" only → passes via existing path
- [ ] AC with VerifyFunc only (returns true,"ok") → passes via VerifyFunc branch
- [ ] AC with BOTH Command="false" and VerifyFunc returning true → Command wins; AC fails
- [ ] AC with none set → returns error "empty criterion" (unchanged)

### Integration
- [ ] `bus.Subscribe` receives `task.dispatch` when CodeExecutor.Execute runs
- [ ] `.stoke/events.db` contains a `task.dispatch` row with matching `session_id` after a fake `stoke ship` run

## Acceptance Criteria

```bash
# Build + vet gate (CI-level)
go build ./... && go vet ./...

# eventlog package
go test ./internal/eventlog/... -run TestAppendReplay
go test ./internal/eventlog/... -run TestHashChain
go test ./internal/eventlog/... -run TestOrphanToolCall
go test ./internal/eventlog/... -run TestConcurrentAppend
go test ./internal/eventlog/... -run TestJSONLMirror

# executor package
go test ./internal/executor/... -run TestCodeExecutor

# router package
go test ./internal/router/... -run TestClassify
go test ./internal/router/... -run TestClassifyIntent
go test ./internal/router/... -run TestIntentGate

# AcceptanceCriterion generalization
go test ./internal/plan/... -run TestVerifyFuncBackwardCompat

# End-to-end: event is present after a dry-run task dispatch
sqlite3 .stoke/events.db 'SELECT COUNT(*) FROM events WHERE type = "task.dispatch"'
```

All `go test` invocations must exit 0. The final `sqlite3` query must return ≥ 1 after a minimal driver test harness writes one `task.dispatch` event.

## Implementation Checklist

1. [ ] Add `github.com/oklog/ulid/v2` to `go.mod`; run `go mod tidy`. No other new deps.
2. [ ] Create `internal/eventlog/schema.sql` with the DDL above. Embed via `//go:embed schema.sql`.
3. [ ] Create `internal/eventlog/canonical.go` — deterministic JSON marshalling (sorted keys, no HTML escape, fixed number format). Used for hash input.
4. [ ] Create `internal/eventlog/log.go` — `Open(dbPath)` opens SQLite, sets `journal_mode=WAL`, `synchronous=NORMAL`, `busy_timeout=5000`, executes embedded DDL. Returns `*sqlLog` implementing `Log`.
5. [ ] Implement `(*sqlLog).Append`: begin tx, read head hash, mint ULID, compute SHA256 chain, insert event + upsert head, commit. Honor `STOKE_EVENTLOG_JSONL=1` → append canonical JSON line to `<dbPath>.jsonl` after commit.
6. [ ] Implement `(*sqlLog).Read`: return `iter.Seq[Event]` (or channel iterator for Go 1.22) ranging rows `WHERE session_id=? AND id > ? ORDER BY id LIMIT 1000` in batches until exhausted or ctx cancelled.
7. [ ] Implement `(*sqlLog).Replay`: follow replay algorithm pseudocode. Return `State` with Messages rehydrated since last `context.compact`, `OpenToolCalls` resolved per idempotence table.
8. [ ] Create `internal/eventlog/idempotent.go` with the tool-name → idempotent? table. Default: not idempotent.
9. [ ] Create `internal/eventlog/types.go` — event-type enum constants + validator (warns on unknown types, does not reject).
10. [ ] Add `eventlog.EmitBus(bus.Publisher, Log, Event) error` helper in `internal/eventlog/bus_bridge.go` that publishes to the bus AND appends to the log in a single call (so every executor uses one line).
11. [ ] Create `internal/eventlog/log_test.go` covering all 8 testing bullets for eventlog (happy, chain tamper, replay-through-compact, both orphan cases, concurrent writers, JSONL mirror, cross-session FK).
12. [ ] Add `VerifyFunc func(ctx context.Context) (passed bool, output string) \`json:"-"\`` field to `internal/plan/sow.go` `AcceptanceCriterion` struct. Do NOT modify `UnmarshalJSON`.
13. [ ] Extend `runACCommand` in `internal/plan/verification_descent.go:829` with the new leading branch: if Command/FileExists/ContentMatch all empty and VerifyFunc non-nil, call VerifyFunc and return its output + pass bool. All other paths unchanged.
14. [ ] Add `internal/plan/sow_verifyfunc_test.go` covering the 4-row backward-compat matrix.
15. [ ] Create `internal/executor/executor.go` with `Executor` interface, `Effort`, `Deliverable`, `RepairFunc`, `EnvFixFunc` types exactly as specified.
16. [ ] Create `internal/executor/code.go` — `CodeExecutor` wrapping `sow_native.go`. Inject `Runner func(ctx, sow.NativeInput) (sow.NativeOutput, error)` at construction; main.go wires it to `execNativeTask`.
17. [ ] Implement `CodeExecutor.Execute`: publish `task.dispatch` → call Runner → publish `verify.start/end` around AC loop (delegate to existing descent) → publish `task.complete`/`task.fail`. All events emitted via `eventlog.EmitBus`.
18. [ ] Implement `CodeExecutor.BuildRepairFunc`, `BuildEnvFixFunc`, `BuildCriteria` by delegating to existing `descent_bridge.go` logic (factor shared code into small helpers if needed; DO NOT duplicate).
19. [ ] Create `internal/executor/code_test.go` with fake bus + fake runner + fake eventlog. Assert event sequence on success and on runner error.
20. [ ] Create `internal/router/router.go` with TaskType/Intent enums and `Classify` / `ClassifyIntent` / `ClassifyDeterministic` / `ClassifyIntentDeterministic` functions.
21. [ ] Implement `ClassifyDeterministic` using compiled `*regexp.Regexp` constants (one per TaskType) in scan order Deploy → Research → Browser → Delegate → Code → Chat. First match wins.
22. [ ] Implement `ClassifyIntentDeterministic` using Implement/Diagnose regexes; both-match → Implement; zero-match → Ambiguous.
23. [ ] Implement `Classify` and `ClassifyIntent` as thin wrappers: deterministic → return if matched; else call Haiku via `provider.Provider` with the tight prompt above; validate response is enum-valid; fallback to safe default + emit `router.fallback` on bus.
24. [ ] Create `internal/router/intent_gate.go` — `Gate(ctx, task, tools, provider) (Intent, harnessTools.Set, error)` that calls `ClassifyIntent` + clamps tools via `harnessTools.ReadOnly()` for DIAGNOSE/AMBIGUOUS.
25. [ ] Add `harnessTools.ReadOnly(set) Set` to `internal/harness/tools/` that returns a new set with `Write=true` tools removed. If the method already exists, reuse it.
26. [ ] Create `internal/router/router_test.go` covering the 8 Classify / ClassifyIntent / IntentGate testing bullets.
27. [ ] Wire `CodeExecutor` in `cmd/r1/main.go` at the same construction point as the current SOW runner. Guard behind nothing — CodeExecutor is a pure wrapper and must work day one.
28. [ ] Verify `go build ./cmd/r1`, `go vet ./...`, and all new package tests pass.
29. [ ] Verify the final acceptance sqlite3 query returns ≥ 1 after running a minimal integration test harness that writes one `task.dispatch` event through `CodeExecutor` (test lives in `internal/executor/integration_test.go`, uses tempdir + real SQLite).
30. [ ] Confirm no existing tests regress: `go test ./...` passes on the feature branch.
