<!-- STATUS: done -->
<!-- BUILD_COMPLETED: 2026-04-22 -->
<!-- CREATED: 2026-04-21 -->
<!-- DEPENDS_ON: executor-foundation (eventlog §), provider-pool (model calls during replay) -->
<!-- BUILD_ORDER: 18 -->

# Event Log Proper — Durable SQLite-Backed Session Log with Hash Chain

## 1. Overview

Stoke today has two partial implementations of the "durable session log" concept. `internal/bus/wal.go` is an append-only NDJSON WAL that lives under `.stoke/bus/events.log`: every event published on the in-memory hub is `json.Marshal`-ed, written, and `fsync`-ed. It works, and `cmd/stoke/resume_cmd.go` reads it to report what a dead session was doing — but the read side is observability only. No code path today can walk the WAL, reconstruct task state, and resume execution. The WAL also has no tamper seal, no indexed query (session scans are O(N) over the whole file), and no mechanism for cross-session joins. `executor-foundation.md` already outlined a successor design but the package was never built, and the resume command still talks to `bus.WAL` directly.

This spec promotes that successor into a shipped `internal/eventlog/` package: SQLite + WAL, one `events` table, hash-chain integrity (`parent_hash = sha256(prev.id || prev.payload || prev.parent_hash)`), ULID primary keys, indexes on `session_id` / `task_id` / `mission_id` / `sequence`. Events bridge both ways — a helper `eventlog.EmitBus(bus, log, ev)` publishes to the existing in-memory hub AND appends to the SQLite log in one call. `internal/bus/` stays as the live publisher feeding subscribers (`hub/builtin` honesty gate, cost tracker, etc.); `internal/eventlog/` is the durable audit + replay substrate. `cmd/stoke/resume_cmd.go` swaps its `bus.OpenWAL`+`ReadFrom` pair for `eventlog.Open`+`ReplaySession`, and `cmd/stoke/sow_native.go` gains a `--resume-from=<session-id>` flag that reads the eventlog, reconstructs the last in-progress task, and dispatches the next task from there — closing the Task 18 MVP gap.

## 2. Why not just promote `hub/bus.go`

The in-memory hub (`internal/bus/bus.go` lines 106–124) and its subscriber machinery (`hub/`) are already load-bearing. Subscribers register with Observe / Gate / Transform priorities, process events synchronously in priority order, and can veto or mutate payloads. The `Event` struct carries `Scope` (mission/branch/loop/task/stance IDs) — a shape tuned for in-process fan-out. The durable-log concerns (hash chain, indexed range scans, replay cursors, cross-session FK) need a storage substrate, not a subscriber. Gluing them by making `bus.WAL` the canonical source of truth would either (a) force every subscriber call to pay a sha256 + SQLite commit on the hot path, or (b) inject transaction machinery into the hub in a way that breaks the Observe/Gate/Transform priority contract. Adding `internal/eventlog/` alongside — with `EmitBus` as the single opinionated bridge — preserves both surfaces. Existing bus subscribers keep working unchanged; new code paths call `EmitBus` and get durable + in-process delivery in one line.

## 3. Schema

File: `internal/eventlog/schema.sql`, embedded via `//go:embed`.

```sql
PRAGMA journal_mode = WAL;
PRAGMA synchronous  = NORMAL;
PRAGMA busy_timeout = 5000;
PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS events (
    id           TEXT    PRIMARY KEY,             -- ULID (sortable, monotonic per process)
    sequence     INTEGER NOT NULL UNIQUE,         -- monotonic per DB (autoincrement via max(sequence)+1)
    type         TEXT    NOT NULL,                -- dotted namespace, e.g. task.dispatch
    session_id   TEXT,
    mission_id   TEXT,
    task_id      TEXT,
    loop_id      TEXT,
    timestamp    TEXT    NOT NULL,                -- RFC3339Nano UTC
    emitter_id   TEXT,
    payload      BLOB,                            -- canonical JSON bytes (sorted keys)
    parent_hash  TEXT,                            -- sha256(prev.id || prev.payload || prev.parent_hash), hex; "" at root
    causal_ref   TEXT                             -- optional FK-style pointer into events.id
);

CREATE INDEX IF NOT EXISTS idx_events_session ON events(session_id);
CREATE INDEX IF NOT EXISTS idx_events_task    ON events(task_id);
CREATE INDEX IF NOT EXISTS idx_events_mission ON events(mission_id);
CREATE INDEX IF NOT EXISTS idx_events_seq     ON events(sequence);
CREATE INDEX IF NOT EXISTS idx_events_type    ON events(type);

-- Single-row head pointer table makes chain-append a single read.
CREATE TABLE IF NOT EXISTS event_chain_head (
    id          INTEGER PRIMARY KEY CHECK (id = 1),
    head_id     TEXT    NOT NULL DEFAULT '',
    head_hash   TEXT    NOT NULL DEFAULT '',
    head_seq    INTEGER NOT NULL DEFAULT 0
);
INSERT OR IGNORE INTO event_chain_head (id) VALUES (1);
```

Notes:
- `session_id` / `mission_id` / `task_id` / `loop_id` mirror `bus.Scope` so `EmitBus` is a trivial field copy.
- `sequence` is a single chain across the DB (not per-session). This keeps the replay cursor a single integer and matches RT-04 §2. Per-session ordering is available via `ORDER BY id` (ULIDs are time-sortable).
- `parent_hash` is stored hex-encoded SHA256 (64 chars).
- Canonical JSON rules: UTF-8, sorted object keys, no whitespace, no HTML escaping. Implemented in `internal/eventlog/canonical.go` with a `Marshal(any) ([]byte, error)` that recursively sorts map keys. Used only for hash input; the `payload` column stores those same canonical bytes so verification is byte-for-byte reproducible.

## 4. Hash chain

Algorithm on every `Append`:

```
prev_id, prev_hash, prev_seq := head_row()    // from event_chain_head
ev.ID       = ulid.Make().String()
ev.Sequence = prev_seq + 1
payload     = canonicalJSON(ev.PayloadStruct)
chain_input = prev_id + string(payload) + prev_hash
ev.ParentHash = hex(sha256(chain_input))

INSERT INTO events (...)
UPDATE event_chain_head SET head_id=ev.ID, head_hash=ev.ParentHash, head_seq=ev.Sequence
COMMIT
```

`Verify(ctx)` walks the chain in order (`SELECT * FROM events ORDER BY sequence`), recomputing `parent_hash` from the previous row's `id || payload || parent_hash` and comparing to the stored value. On mismatch it returns `ErrChainBroken{Sequence: N, Expected, Got}` pointing at the first corrupted row. Verification is O(N) and a cold walk — intended for startup integrity checks or a `stoke eventlog verify` CLI, not the hot path.

Performance cost per append: one `crypto/sha256` call over `len(id) + len(payload) + 64` bytes, plus one UPDATE of `event_chain_head`. On a seeded 1000-event log (~50 KiB avg payload), local benchmark target: ≤ 2 ms per `Append` p50 on modest hardware, ≤ 10 ms p99. Measured in `log_bench_test.go`; flagged as a gate, not a hard fail.

## 5. API

File: `internal/eventlog/log.go`.

```go
package eventlog

import (
    "context"
    "iter"

    "github.com/ericmacdougall/stoke/internal/bus"
)

// Log is the durable, hash-chained event store. Callers never construct
// one directly — use Open.
type Log struct { ... }

// Open opens (or creates) the SQLite DB at dbPath, runs the embedded DDL,
// sets WAL + busy_timeout pragmas, and loads the chain head pointer.
func Open(dbPath string) (*Log, error)

// Close flushes pending statements and closes the DB handle.
func (l *Log) Close() error

// Append writes ev to the log, computing its hash chain entry, ULID, and
// sequence atomically in a single transaction. Populates ev.ID, ev.Sequence,
// ev.ParentHash in place on success.
func (l *Log) Append(ev *bus.Event) error

// ReadFrom yields every event with sequence >= from, in ascending
// sequence order. Cancellable via ctx (iterator exits on ctx.Done()).
// Internally batches LIMIT 1000 to bound memory on long histories.
func (l *Log) ReadFrom(ctx context.Context, sequence uint64) iter.Seq2[bus.Event, error]

// ReplaySession yields events whose Scope.LoopID, Scope.MissionID, or
// Scope.TaskID match sessionID. Matches the heuristic in resume_cmd.go
// so the swap is behavior-preserving.
func (l *Log) ReplaySession(ctx context.Context, sessionID string) iter.Seq2[bus.Event, error]

// Verify walks the whole chain and returns ErrChainBroken on the first
// hash mismatch. Intended for startup integrity / CLI audit.
func (l *Log) Verify(ctx context.Context) error

// EmitBus publishes ev to the in-memory hub AND appends it to l. If the
// append fails, the bus publish is rolled back only insofar as future
// subscribers won't see the event ID (there is no retro-unpublish); the
// error is returned and callers decide whether to abort. This is the
// single blessed bridge — all executors and supervisors call it instead
// of b.Publish + l.Append separately.
func EmitBus(b *bus.Bus, l *Log, ev bus.Event) error
```

Contract details:

- `Append` mutates `ev.ID`, `ev.Sequence`, and writes the row. It also updates `ev.Timestamp` to `time.Now().UTC()` if zero. Callers may pre-set `Timestamp` (useful for replay / import).
- `ReadFrom` and `ReplaySession` return `iter.Seq2[bus.Event, error]` (Go 1.23). Each yielded tuple is either `(event, nil)` or `(zeroEvent, err)` — on error, the iterator terminates after yielding the error once.
- `EmitBus` ordering: append first, publish second. Reasoning: if SQLite fails, no subscriber sees a phantom event; if the publish panics, the durable record still exists and can be replayed later.
- `Open` runs `Verify` only when `STOKE_EVENTLOG_VERIFY_ON_OPEN=1`. Default is skip so cold starts stay fast.

## 6. SOW-runner restart hook

CLI flag: `stoke sow --resume-from=<session-id>` on `cmd/stoke/sow_native.go`. Semantics:

1. Open `.stoke/events.db` via `eventlog.Open`.
2. Call `log.ReplaySession(ctx, sessionID)`; collect events into a slice (bounded — most sessions <10k events; 100k is the hard cap before the runner errors with "session too large, compact first").
3. Walk events backwards until the most recent task boundary event:
   - `task.complete` — means task N finished cleanly; dispatch task N+1.
   - `task.fail` — means task N failed mid-descent; re-dispatch task N with an explicit `resume_retry=true` flag so it picks up its worktree artifacts.
   - `task.dispatch` (no matching complete/fail) — means task N crashed mid-flight; re-dispatch task N fresh.
   - `session.end` — nothing to resume; exit 0 with "session already complete".
   - no events at all — behaves like a fresh start (no `--resume-from`).
4. Before dispatching, emit a `session.resumed` event with payload `{resumed_from_sequence, last_event_type, next_task_id}` so the audit trail records the decision.

Three required test cases in `cmd/stoke/sow_native_resume_test.go`:

- **Fresh start**: no events in the log → runner behaves exactly like `stoke sow` with no flag. Asserts zero side-effects from resume logic.
- **Mid-task crash**: seed log with `session.start`, `task.dispatch{task_id:T1}` only → runner re-dispatches T1 exactly once, no duplicate dispatch observed in the post-run log.
- **Mid-session crash**: seed log with `session.start`, `task.dispatch{T1}`, `task.complete{T1}` → runner dispatches T2 (next per plan), does NOT re-dispatch T1.

**Orphan tool-call handling** — during replay, if the last event for a given `call_id` is a `tool.call` with no matching `tool.result`, the reconstructor emits a synthetic `tool.result` with `{status:"error", error:"orphan — system restarted mid-call", call_id: <original>}` and appends it to the log before continuing. Rationale: the model's context must stay consistent — a bare tool_use with no tool_result triggers an Anthropic API error on the next turn. The synthetic result keeps the history replayable. The policy table (matches executor-foundation §idempotent) lives in `internal/eventlog/idempotent.go`: read-only / pure tools are flagged for re-dispatch instead of synthesis; worktree-mutating tools always get the synthetic error.

## 7. Implementation checklist

1. [ ] Add `github.com/oklog/ulid/v2` to `go.mod` if not already present; `go mod tidy`. Confirm no other new deps.
2. [ ] Create `internal/eventlog/schema.sql` with the DDL from §3. Embed via `//go:embed schema.sql`.
3. [ ] Create `internal/eventlog/canonical.go` with `Marshal(v any) ([]byte, error)` that produces deterministic JSON (sorted map keys, no HTML escape, stdlib number format). Unit test: `canonical_test.go` covering nested maps, arrays, unicode strings, nil, numbers — byte-equal on repeated calls.
4. [ ] Create `internal/eventlog/log.go` with the `Log` struct, `Open(dbPath string) (*Log, error)` that opens SQLite via `modernc.org/sqlite`, runs the embedded DDL, sets `journal_mode=WAL`, `synchronous=NORMAL`, `busy_timeout=5000`.
5. [ ] Implement `(*Log).Close() error` — closes the DB handle; idempotent.
6. [ ] Implement `(*Log).headRow() (id, hash string, seq uint64, err error)` — internal helper returning the current chain head from `event_chain_head` row 1.
7. [ ] Implement `(*Log).Append(ev *bus.Event) error`: BEGIN IMMEDIATE, read head, mint ULID, canonicalize payload, compute sha256, INSERT event, UPDATE head, COMMIT. Populate `ev.ID`, `ev.Sequence`, `ev.ParentHash`, `ev.Timestamp` (if zero). Retries on `SQLITE_BUSY` up to 3× with 10/40/160 ms backoff.
8. [ ] Implement `(*Log).ReadFrom(ctx, sequence) iter.Seq2[bus.Event, error]` using Go 1.23 `iter.Seq2`. Stream with `LIMIT 1000 OFFSET 0` batched, each batch opens a fresh statement keyed off the last yielded sequence. Honor `ctx.Done()`.
9. [ ] Implement `(*Log).ReplaySession(ctx, sessionID) iter.Seq2[bus.Event, error]`. SQL: `WHERE session_id=? OR mission_id=? OR loop_id=? OR task_id=? ORDER BY sequence`. Same batched iterator pattern as ReadFrom.
10. [ ] Implement `(*Log).Verify(ctx) error`. Full-table ascending walk, recompute `parent_hash` per row, return `*ErrChainBroken{Sequence, Expected, Got}` on first mismatch.
11. [ ] Define `ErrChainBroken` error type with `Sequence uint64`, `Expected string`, `Got string` fields and an `Error()` method. Exported.
12. [ ] Create `internal/eventlog/bus_bridge.go` with `EmitBus(b *bus.Bus, l *Log, ev bus.Event) error` — append first (mutates ev.ID/Sequence/ParentHash via pointer copy), then publish to the bus. Document ordering rationale in a comment.
13. [ ] Create `internal/eventlog/idempotent.go` with `IsIdempotentTool(name string) bool`. Mirror the table from executor-foundation §idempotent (read_*/grep/glob/bash-readonly/browser_extract_text → true; write/edit/apply_patch/git commit → false; default false).
14. [ ] Create `internal/eventlog/log_test.go` covering: Append+ReadFrom yields events in order; hash chain is valid across 100 appends; Verify on clean log returns nil; flipping one byte in `events.payload` via raw SQL makes Verify return ErrChainBroken pointing at that row.
15. [ ] Add `TestReplaySession_ScopeMatching` to `log_test.go`: seed events with varying Scope.{LoopID,MissionID,TaskID,BranchID}; assert ReplaySession matches the same rows resume_cmd.go currently matches (behavior-preserving).
16. [ ] Add `TestAppend_ConcurrentWriters` to `log_test.go`: 4 goroutines × 250 appends on distinct session_ids → 1000 rows total, all chain hashes valid, all sequences unique and monotonic.
17. [ ] Add `TestAppend_SQLiteBusyRetry` to `log_test.go`: simulate BUSY via a blocking tx from another DB handle; assert Append retries 3× and either succeeds or returns a wrapped `eventlog: busy` error.
18. [ ] Add `TestEmitBus_AppendBeforePublish` to `log_test.go`: use a fake bus that records publish calls; assert Append wrote the row BEFORE the bus saw the event (via a hook that inspects SQLite mid-publish).
19. [ ] Add `TestVerify_OnFreshLog` + `TestVerify_OnCorruptedLog` to `log_test.go` — seed 1000 events, verify clean; flip one byte, verify reports `ErrChainBroken{Sequence: N}`.
20. [ ] Add `log_bench_test.go` with `BenchmarkAppend` — seed 1000 events, assert p50 ≤ 2 ms and p99 ≤ 10 ms on local hardware (soft gate, fails CI only if p99 > 50 ms).
21. [ ] In `cmd/stoke/resume_cmd.go`, replace the `bus.OpenWAL` + `w.ReadFrom(0)` pair with `eventlog.Open(filepath.Join(repo, ".stoke", "events.db"))` + `log.ReplaySession(ctx, sessionID)`. Collect the iterator into a slice to preserve the existing `reconstructSession` API.
22. [ ] Preserve resume_cmd.go's `--list` mode by adding `(*Log).ListSessions(ctx) ([]string, error)` that returns distinct non-empty `session_id`, `mission_id`, `loop_id` values.
23. [ ] Add `cmd/stoke/resume_cmd_test.go` covering the three resume-behavior cases (fresh, mid-task, mid-session) using a tempdir eventlog seeded by Append directly.
24. [ ] In `cmd/stoke/sow_native.go`, add `--resume-from=<session-id>` flag parsing. When set: open the eventlog, call `log.ReplaySession`, run the backwards-walk decision logic described in §6, emit `session.resumed`, then dispatch the chosen task.
25. [ ] Implement `internal/eventlog/resume.go` with `DecideResume(events []bus.Event, plan *plan.SOW) (nextTask string, mode ResumeMode, err error)` — pure function, no IO, returns one of `ResumeFreshStart | ResumeRetryTask | ResumeNextTask | ResumeAlreadyDone`. Covered by unit tests over seeded event slices.
26. [ ] Implement orphan tool-call synthesis: in `DecideResume`, walk events forward tracking `call_id → (tool.call, tool.result)` pairs; for each orphan where `IsIdempotentTool(name)` is false, return a list of synthetic `tool.result` events the caller must `Append` before dispatching. Idempotent orphans are flagged for re-dispatch instead.
27. [ ] Add `cmd/stoke/sow_native_resume_test.go` covering: fresh-start no-op, mid-task-crash re-dispatches the crashed task exactly once, mid-session-crash dispatches the next task. Each test seeds events directly via Append and asserts on post-run log state.
28. [ ] Add `cmd/stoke/sow_native_resume_orphan_test.go` covering: seed a `tool.call` for `write_file` with no matching result → DecideResume returns a synthetic `tool.result{error:"orphan"}`; seed a `tool.call` for `read_file` → DecideResume flags for re-dispatch, no synthesis.
29. [ ] Wire `Verify` into `stoke eventlog verify` subcommand in `cmd/stoke/eventlog_cmd.go` (new file). Flags: `--db <path>` (default `.stoke/events.db`). Exit codes: 0 clean, 1 ErrChainBroken, 2 IO error.
30. [ ] Add `cmd/stoke/eventlog_cmd_test.go` covering: clean log exits 0; tampered log exits 1 with the broken sequence number in stderr.
31. [ ] Run `gofmt -w ./internal/eventlog/ ./cmd/stoke/` and `go vet ./...`; fix any reported issues.
32. [ ] Migration: if `.stoke/bus/events.log` exists on open and `.stoke/events.db` does not, print a one-line warning pointing at `stoke eventlog import-wal` (a follow-up subcommand — NOT in this spec; just leave the breadcrumb). Existing bus WAL continues working; eventlog is additive.
33. [ ] Run `go build ./cmd/stoke && go test ./... && go vet ./...` and confirm all three are green.
34. [ ] Run the seeded 1000-event verification test end-to-end: `stoke eventlog verify --db testdata/seeded.db` exits 0 in <500 ms.
35. [ ] Run the crash-simulation integration test: kill a `stoke sow` process mid-task (SIGKILL), then `stoke sow --resume-from=<session>` and assert the same task is dispatched exactly once (grep event log for duplicate `task.dispatch{task_id:T1}` entries; count must be exactly 2 — the original and the resumed).

## 8. Acceptance

- `go build ./cmd/stoke && go test ./... && go vet ./...` all exit 0.
- Hash chain verified on a seeded 1000-event log via `stoke eventlog verify` (exit 0, <500 ms).
- `stoke sow --resume-from=<session-id>` on a crashed-mid-task session dispatches the crashed task exactly once (asserted by counting `task.dispatch` events with matching `task_id` — original + one resume = exactly 2).
- `stoke sow --resume-from=<session-id>` on a mid-session-complete session dispatches the next task, not the already-completed one.
- Orphan tool-call integration test: seed a `tool.call` for `write_file`, run resume, assert a synthetic `tool.result{error:"orphan"}` now appears in the log at the correct sequence and that the model context hydrated from the log has no bare `tool_use` blocks.

## 9. Rollout

No flag gate. The eventlog package is additive — the existing `bus.WAL` path keeps working unchanged; nothing breaks if callers don't migrate. The `--resume-from` CLI flag is opt-in per invocation. Recommended adoption order post-merge:

1. Week 1: ship eventlog + EmitBus; migrate one executor (`CodeExecutor.Execute` from executor-foundation) to use EmitBus. No user-visible behavior change.
2. Week 2: ship `stoke sow --resume-from` and the three resume tests. Gate: run on one real crashed session internally; verify exact-once dispatch.
3. Week 3: migrate resume_cmd.go to read from eventlog (tail of the MVP work). Once done, deprecate the `bus.WAL` read path — the WAL stays as a live publisher only. No user-facing deprecation needed; internal migration only.

## 10. Testing

Test coverage is enumerated inline in §7 checklist (items 3, 14, 15, 16, 17, 18) but consolidated here for the reviewer's benefit:

- **`internal/eventlog/canonical_test.go`** (item 3) — deterministic marshal on nested maps / arrays / unicode / nil / numbers; byte-equal across repeated calls.
- **`internal/eventlog/log_test.go`** covering:
  - Append + ReadFrom yields events in sequence order
  - Hash chain valid across 100 sequential appends
  - `Verify` on a clean log returns nil
  - `Verify` on a log with one tampered payload byte returns `ErrChainBroken` pointing at the tampered row (item 14)
  - `TestReplaySession_ScopeMatching` — matches the same rows `cmd/stoke/resume_cmd.go` currently matches (behavior-preserving delta, item 15)
  - `TestAppend_ConcurrentWriters` — 4 goroutines × 250 appends on distinct session_ids → 1000 rows, all chain hashes valid, all sequences unique + monotonic (item 16)
  - `TestAppend_SQLiteBusyRetry` — simulate BUSY from a blocking tx; assert 3× retry + wrapped `eventlog: busy` error (item 17)
  - `TestEmitBus_AppendBeforePublish` — assert SQLite row written BEFORE bus publish; fake bus records call order (item 18)
- **`internal/eventlog/log_bench_test.go`** — p50 ≤ 2ms, p99 ≤ 10ms per Append on a 1000-event seed with ~50 KiB avg payload (flagged as a gate, not a hard fail).
- **`cmd/stoke/sow_native_resume_test.go`** — three resume scenarios (fresh start, mid-task crash = re-dispatch same task, mid-session crash = dispatch next task); orphan tool-call synthesis emits a synthetic `tool.result` with `error="orphan"`.
- Run command: `go test -race -count=1 ./internal/eventlog/... ./cmd/stoke/...`.
