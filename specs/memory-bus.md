<!-- STATUS: done -->
<!-- BUILD_COMPLETED: 2026-04-22 -->
<!-- CREATED: 2026-04-21 -->
<!-- DEPENDS_ON: executor-foundation (Task 19), operator-ux-memory Part D (wisdom SQLite handle), memory-full-stack (sibling; distinct stoke_memories table) -->
<!-- BUILD_ORDER: 23 -->

# Memory Bus — Scoped Live Worker-to-Worker Memory

## 1. Overview

Stoke runs parallel workers inside a single SOW session (`cmd/stoke/sow_native.go` `ParallelWorkers`, currently ~8 concurrent). Today those workers share nothing mid-session except the filesystem: worker A can discover "this monorepo uses `workspace:*` pnpm refs" or "the integration suite flakes on `TestFoo`", and that knowledge does not reach worker B until the post-session wisdom extraction writes a row to `stoke_memories` — i.e., *next* session. The cost is duplicated budget on the same mistakes, duplicated tool calls, and silent divergence between workers on ambiguous codebase facts.

RS-7 adds a **scoped memory bus**: a live, session-scoped worker-to-worker communication layer with 6 visibility scopes (session, session-step, worker, all-sessions, global, always). It is deliberately distinct from the cross-session knowledge store in `specs/memory-full-stack.md`. The two stores share the same SQLite database handle (same process, same WAL) but occupy different tables with different retrieval semantics, different lifetimes, and different operator controls. The bus is transient-by-default; the memory-full-stack store is long-lived-by-default.

## 2. Distinction vs memory-full-stack.md

| Property | `stoke_memories` (memory-full-stack) | `stoke_memory_bus` (this spec) |
|---|---|---|
| Purpose | Cross-session semantic/episodic/procedural knowledge | Live intra/inter-session worker comms |
| Retrieval | FTS5 + sqlite-vec RRF hybrid; 4 prompt hook points; consolidation pipeline | Scope-filter WHERE clause; polling cursor; injected under `## Active Memories` H2 block |
| Dedup | Embedding cosine ≥ 0.92 or Jaccard ≥ 0.85 during hygiene | Unique on `(scope, scope_target, key)` at INSERT time |
| Lifetime | Indefinite with importance decay and retention tiers | `expires_at` column; enforced by `retention-policies.md` |
| Operator surface | `stoke memory {list,show,put,search,consolidate,gc}` CLI | r1-server-ui-v2 memory explorer panel |
| Writers | Consolidation pipeline, wisdom adapter, `stoke memory put` CLI | Running workers via tool calls; HITL resolvers; descent handlers |
| Readers | Planner, worker-dispatch, delegation, verifier hook points | Every worker on every turn (via `Bus.Recall` + prompt injection) |

They share the same `*sql.DB` handle and the same PRAGMA baseline. They do not share tables, rows, or retrieval code.

## 3. Schema

```sql
CREATE TABLE IF NOT EXISTS stoke_memory_bus (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    created_at          TEXT    NOT NULL,
    expires_at          TEXT,                            -- NULL = no auto-expiry; enforced by retention-policies.md
    scope               TEXT    NOT NULL,                -- one of the 6 Scope constants below
    scope_target        TEXT    NOT NULL DEFAULT '',     -- TaskID / StepID / repo hash / '' for global
    session_id          TEXT    NOT NULL,                -- always populated; originating session
    step_id             TEXT    NOT NULL DEFAULT '',     -- step within the session (e.g., "S2-AC3"); blank when N/A
    task_id             TEXT    NOT NULL DEFAULT '',     -- originating worker's task ID
    author              TEXT    NOT NULL,                -- "worker:<id>" | "operator" | "system"
    key                 TEXT    NOT NULL,                -- de-dup discriminator within (scope, scope_target)
    content             TEXT    NOT NULL,                -- plaintext payload (empty string when encrypted)
    content_encrypted   BLOB,                            -- populated by encryption-at-rest spec when active; NULL otherwise
    content_hash        TEXT    NOT NULL,                -- SHA-256(content) for ledger reference without leaking content
    tags                TEXT    NOT NULL DEFAULT '[]',   -- JSON array of tag strings
    metadata            TEXT    NOT NULL DEFAULT '{}',   -- JSON object; free-form
    read_count          INTEGER NOT NULL DEFAULT 0,
    last_read_at        TEXT,
    UNIQUE (scope, scope_target, key)                    -- dedup: re-Remember with same key is UPSERT
);

CREATE INDEX IF NOT EXISTS idx_membus_scope           ON stoke_memory_bus(scope, scope_target);
CREATE INDEX IF NOT EXISTS idx_membus_session         ON stoke_memory_bus(session_id);
CREATE INDEX IF NOT EXISTS idx_membus_step            ON stoke_memory_bus(session_id, step_id);
CREATE INDEX IF NOT EXISTS idx_membus_task            ON stoke_memory_bus(task_id);
CREATE INDEX IF NOT EXISTS idx_membus_expires         ON stoke_memory_bus(expires_at);
CREATE INDEX IF NOT EXISTS idx_membus_id_cursor       ON stoke_memory_bus(id);
```

`UNIQUE (scope, scope_target, key)` drives the UPSERT behavior: re-calling `Remember` with the same `(scope, scope_target, key)` updates `content`, `content_hash`, `tags`, `metadata`, `expires_at` and leaves `created_at` and `read_count` intact. The insertion statement uses `INSERT ... ON CONFLICT DO UPDATE SET ...`.

## 4. Visibility scopes

```go
// Scope controls who sees a memory via Recall. Exactly one value per row.
type Scope string

const (
    // ScopeSession — visible to every worker in the same session_id.
    // Typical use: "worker 2 discovered the repo uses workspace:* refs; all
    // session-peers should know before they edit package.json".
    ScopeSession Scope = "session"

    // ScopeSessionStep — visible only to workers whose (session_id, step_id)
    // matches scope_target. Typical use: "the linter for S2-AC3 needs --strict;
    // next retry of this step should pick it up". scope_target is "<step_id>".
    ScopeSessionStep Scope = "session_step"

    // ScopeWorker — visible only to the worker whose TaskID matches
    // scope_target. Private notes the worker writes to itself between turns
    // (self-reminders across microcompaction). scope_target is "<task_id>".
    ScopeWorker Scope = "worker"

    // ScopeAllSessions — visible to every worker in every session of the
    // current R1 instance (same process, same machine). Typical use:
    // "this dev laptop has no network to pypi; cache offline wheels".
    ScopeAllSessions Scope = "all_sessions"

    // ScopeGlobal — visible across R1 instances on the same machine (and
    // across machines if r1-server syncs them, out-of-scope for this spec).
    // scope_target is "" for truly global or a repo hash for repo-scoped.
    ScopeGlobal Scope = "global"

    // ScopeAlways — every Recall returns this, regardless of filters.
    // OPERATOR-ONLY: workers MUST NOT write to ScopeAlways. The Remember
    // path rejects worker-authored ScopeAlways writes with ErrScopeForbidden.
    // Typical use: "STOP: the prod DB is being restored, do not push migrations".
    ScopeAlways Scope = "always"
)
```

The `ScopeAlways` write-block is enforced in `Bus.Remember`: if `author` begins with `"worker:"` and `req.Scope == ScopeAlways`, return `ErrScopeForbidden` before enqueuing. Operator writes (`author == "operator"`) and system writes (`author == "system"`) are permitted.

## 5. Architecture (writer-goroutine pattern per correction 1)

SQLite WAL permits exactly one writer at a time; N goroutines contending for `db.Exec` serialize on an internal mutex and give flat throughput. The bus uses a single writer goroutine + buffered channel + 5 ms batch window pattern to convert N concurrent `Remember` calls into O(1) transactions per 5 ms tick.

### 5.1 Types

```go
type Bus struct {
    db      *sql.DB
    writes  chan writeRequest       // cap 256; backpressure blocks senders
    stop    chan struct{}           // closed by Close()
    done    chan struct{}           // writerLoop signals clean exit
    readDSN string                  // DSN for cross-process read-only opens
    signal  *net.UnixConn           // optional UDS wake signal to r1-server
}

type writeRequest struct {
    ctx    context.Context
    req    RememberRequest
    result chan error              // cap 1; writer closes after commit
}
```

### 5.2 `writerLoop`

Runs in its own goroutine, started by `NewBus`. Pseudocode:

```
batch := make([]writeRequest, 0, 256)
ticker := time.NewTicker(5 * time.Millisecond)
for {
    select {
    case <-b.stop:
        flushBatch(batch); close(b.done); return
    case wr := <-b.writes:
        batch = append(batch, wr)
        if len(batch) >= 256 {
            flushBatch(batch); batch = batch[:0]
        }
    case <-ticker.C:
        if len(batch) > 0 { flushBatch(batch); batch = batch[:0] }
    }
}
```

### 5.3 `flushBatch`

Opens a single `BEGIN IMMEDIATE` transaction to acquire the writer lock up front (avoids `SQLITE_BUSY` mid-batch). Prepares the UPSERT statement once, executes in a loop, commits once, then signals each `writeRequest.result`. On context cancellation by a sender, the request is still written (we've already queued it) but the sender has moved on; the `result` send becomes a non-blocking drop.

```go
func (b *Bus) flushBatch(batch []writeRequest) {
    if len(batch) == 0 { return }
    tx, err := b.db.BeginTx(ctx, &sql.TxOptions{}) // BEGIN IMMEDIATE via driver
    // ... prepare UPSERT stmt, loop Exec, collect inserted IDs, Commit
    for i, wr := range batch {
        select { case wr.result <- perItemErr[i]: default: }
    }
    b.signalWake() // best-effort UDS notify
}
```

The `BEGIN IMMEDIATE` is critical: SQLite's default `BEGIN` is deferred, which upgrades to a write-lock on the first write and can then fail with `SQLITE_BUSY` if another connection grabbed it between rows. `BEGIN IMMEDIATE` takes the lock at the start of the transaction and holds it for the whole batch.

### 5.4 `Remember` sender path

```
case <-ctx.Done(): return ctx.Err()
case b.writes <- wr:                 // blocks if buffer full (backpressure)
}
select {
case err := <-wr.result: return err
case <-ctx.Done(): return ctx.Err() // drop; writer still commits
}
```

**Backpressure rule: block, do not drop.** When the channel is full (256 requests in flight) the sender blocks until the writer drains. Dropping memory writes silently would create non-determinism across retries; callers with deadlines use `ctx` to bound the wait.

### 5.5 PRAGMA baseline (verbatim, per correction 1)

Applied once at `NewBus` open on the primary (read/write) handle:

```
PRAGMA journal_mode       = WAL;
PRAGMA synchronous        = NORMAL;
PRAGMA busy_timeout       = 5000;
PRAGMA temp_store         = MEMORY;
PRAGMA cache_size         = -65536;       -- 64 MB page cache
PRAGMA mmap_size          = 268435456;    -- 256 MB mmap window
PRAGMA journal_size_limit = 67108864;     -- 64 MB WAL checkpoint ceiling
```

### 5.6 r1-server cross-process read

r1-server opens the same `server.db` file read-only with:

```
file:/abs/path/to/stoke/.stoke/wisdom.db?mode=ro&_journal_mode=WAL&_busy_timeout=5000
```

…and issues `PRAGMA query_only=1;` on the connection after open. This forbids any accidental DDL/DML, keeps the open path from needing write locks, and respects the WAL checkpoint the writer is running.

### 5.7 Wake signal (source of truth: polling)

r1-server background reader uses three mechanisms in descending authority:

1. **Polling SELECT (source of truth).** Every 200 ms: `SELECT ... FROM stoke_memory_bus WHERE id > :last_seen ORDER BY id LIMIT 256`. `last_seen` starts at 0 on process start. This works under all conditions, including when the writer process crashes and restarts and when r1-server is restarted after a long outage.
2. **UDS wake signal (optimization).** The writer process connects to `<data_dir>/r1-server.sock` after each `flushBatch` and writes a single byte. r1-server's bus reader selects on the UDS and wakes up early (sub-ms). Failure (connection refused, socket missing) is silent and degrades to polling.
3. **fsnotify on `<db>-wal` file (fallback).** If the UDS is unavailable but fsnotify is, r1-server watches the `-wal` sidecar and wakes on `WRITE` events. Coarser than UDS but still faster than pure 200 ms polling.

None of the three is authoritative except polling; UDS and fsnotify are purely latency optimizations. A reader that trusted UDS alone could miss writes if the notifier socket dropped a packet.

### 5.8 Future migration path

If we ever need scoped fan-out (e.g. "deliver this memory to every worker of every session on every machine, exactly once"), multi-consumer replay with per-consumer cursors, or cross-machine sync, the `Bus` interface (Remember/Recall) lets us swap the SQLite backend for embedded NATS + JetStream behind the same methods. Subjects would be `stoke.memory.<scope>.<target>`; JetStream consumers would replace the polling cursor. This is intentionally left as a forward path — we do **not** build it now.

## 6. API

```go
// RememberRequest is the input to Bus.Remember.
type RememberRequest struct {
    Scope       Scope                 // required
    ScopeTarget string                // required for Worker/SessionStep/repo-scoped Global; "" otherwise
    SessionID   string                // required
    StepID      string                // required when Scope == ScopeSessionStep
    TaskID      string                // author's worker task ID
    Author      string                // "worker:<id>" | "operator" | "system"
    Key         string                // dedup discriminator within (Scope, ScopeTarget); auto-hash of Content if empty
    Content     string                // payload; max 100 KB (V-115 parity)
    Tags        []string              // free-form labels
    Metadata    map[string]string     // free-form KV
    ExpiresAt   *time.Time            // nil = no auto-expiry
}

func (b *Bus) Remember(ctx context.Context, req RememberRequest) error
```

**Side-effects of `Remember`:**
1. Validates request (non-empty Content ≤ 100 KB, valid Scope, ScopeAlways only when Author != "worker:*").
2. Computes `content_hash = hex(sha256(Content))`.
3. UPSERTs into `stoke_memory_bus` via the writer goroutine.
4. Emits ledger node `memory_stored` via the standard `ledger.Write` path with edge `references → <task_node_id>` (issuing worker's task) when TaskID is known.
5. Emits hub event `EventMemoryStored` (`hub.Publish("memory.stored", ev)`) so subscribers (cost tracker, honesty gate, r1-server) can react.
6. Emits STOKE envelope event `stoke.memory.store` via `streamjson.Emitter.EmitStoke` carrying `{scope, scope_target, key, content_hash, ledger_node_id}` — **never `content`** (privacy).

```go
// RecallRequest is the input to Bus.Recall.
type RecallRequest struct {
    SessionID string                  // required
    StepID    string                  // required; "" allowed if worker is not inside a step
    TaskID    string                  // required; identifies the calling worker
    RepoHash  string                  // for repo-scoped global matching
    Tags      []string                // optional AND-filter on tags
    Since     int64                   // only return id > Since (polling cursor)
    Limit     int                     // default 256
}

// Memory is the returned row.
type Memory struct {
    ID          int64
    CreatedAt   time.Time
    ExpiresAt   *time.Time
    Scope       Scope
    ScopeTarget string
    SessionID   string
    StepID      string
    TaskID      string
    Author      string
    Key         string
    Content     string                // decrypted by encryption-at-rest spec if content_encrypted was set
    ContentHash string
    Tags        []string
    Metadata    map[string]string
    ReadCount   int64
}

func (b *Bus) Recall(ctx context.Context, req RecallRequest) []Memory
```

**Side-effects of `Recall`:**
1. Builds the scope-visibility WHERE clause (see §7).
2. SELECTs matching rows ORDER BY `id ASC` LIMIT `req.Limit`.
3. `UPDATE stoke_memory_bus SET read_count = read_count + 1, last_read_at = ? WHERE id IN (?)` — routed through the writer goroutine to stay single-writer.
4. Emits one ledger node `memory_recalled` per Recall call, carrying the list of `content_hash` values (**not `content`** — privacy; r1-server can cross-reference the `memory_stored` node via hash).
5. Emits hub event `EventMemoryRecalled` with the count and the list of hashes.
6. Emits STOKE envelope event `stoke.memory.recall` with the same payload shape.

Recall never goes through the writer goroutine for the SELECT (reads are concurrent under WAL), only the `UPDATE read_count` step is enqueued onto the writer channel.

## 7. Scope-visibility matching

Given a `RecallRequest{SessionID=S, StepID=P, TaskID=T, RepoHash=R}`, the SQL WHERE clause is:

```sql
WHERE
  (expires_at IS NULL OR expires_at > :now)
  AND id > :since
  AND (
      -- ScopeSession: any worker in same session
      (scope = 'session'       AND session_id = :S)
    OR
      -- ScopeSessionStep: only when session AND step match
      (scope = 'session_step'  AND session_id = :S AND scope_target = :P)
    OR
      -- ScopeWorker: only when target matches caller's TaskID
      (scope = 'worker'        AND scope_target = :T)
    OR
      -- ScopeAllSessions: every worker of this R1 instance
      (scope = 'all_sessions')
    OR
      -- ScopeGlobal: '' = truly global; otherwise repo-hash match
      (scope = 'global'        AND (scope_target = '' OR scope_target = :R))
    OR
      -- ScopeAlways: always returned regardless of filters
      (scope = 'always')
  )
ORDER BY scope = 'always' DESC, id ASC
LIMIT :limit
```

Specificity / privacy rules:

- `ScopeWorker` with `scope_target='X'` → **only** when `RecallRequest.TaskID == 'X'`. Other workers never see private scopes.
- `ScopeSessionStep` with `scope_target='S2-AC3'` → only when `SessionID == originating_session AND StepID == 'S2-AC3'`.
- `ScopeSession` → all workers in that session; no cross-session leak.
- `ScopeAllSessions` → all workers of the current R1 instance (same process).
- `ScopeGlobal` with `scope_target=''` → global; with `scope_target='<repo-hash>'` → only callers whose repo hash matches.
- `ScopeAlways` → every Recall, always, sorted first so prompt injection shows it prominently.

Tag filter (when `req.Tags` non-empty) applies as an `AND` on top of the scope clause via `json_each(tags)` against each requested tag.

## 8. Injection into worker prompts

In `cmd/stoke/sow_native.go` `buildSOWNativePromptsWithOpts()`, **after** the wisdom injection block (~line 3892 per current tree; search for `## Relevant learnings` to locate the insertion point), add a new H2 block. The block is elided entirely when `Recall` returns zero rows.

```go
// After wisdom injection, before skills/canonical-names block.
if bus != nil {
    mems := bus.Recall(ctx, memory.RecallRequest{
        SessionID: sess.ID,
        StepID:    step.ID,          // "" when not inside a step
        TaskID:    worker.TaskID,
        RepoHash:  memory.RepoHash(repoRoot),
        Limit:     64,                // hard cap for prompt sanity
    })
    if len(mems) > 0 {
        sb.WriteString("\n## Active Memories\n\n")
        sb.WriteString("The following are live notes from other workers or the operator. ")
        sb.WriteString("Treat them as authoritative context for this session.\n\n")
        for _, m := range mems {
            fmt.Fprintf(&sb, "- [%s/%s] %s", m.Scope, m.ScopeTarget, m.Content)
            if len(m.Tags) > 0 { fmt.Fprintf(&sb, " (%s)", strings.Join(m.Tags, ", ")) }
            sb.WriteString("\n")
        }
    }
}
```

The H2 block ordering relative to other blocks: `## Task Definition` → wisdom → **Active Memories** → skills → canonical-names → verification-reminders. The Active Memories block is token-capped by the caller using `tokenest.Count`; overflow drops lowest-id (oldest) first, preserving the most recent `ScopeAlways` and step-scoped notes.

## 9. Ledger nodes

Two new nodes in `internal/ledger/nodes/memory.go`. Both register via the standard `Register()` pattern and live under the normal node ID prefixes.

```go
// MemoryStored records a Bus.Remember call.
// ID prefix: mst-
type MemoryStored struct {
    Scope        string    `json:"scope"`
    ScopeTarget  string    `json:"scope_target"`
    SessionID    string    `json:"session_id"`
    StepID       string    `json:"step_id,omitempty"`
    TaskID       string    `json:"task_id,omitempty"`
    Author       string    `json:"author"`
    Key          string    `json:"key"`
    ContentHash  string    `json:"content_hash"`       // SHA-256 hex; NEVER raw content
    Tags         []string  `json:"tags,omitempty"`
    ExpiresAt    *time.Time `json:"expires_at,omitempty"`
    CreatedAt    time.Time `json:"created_at"`
    Version      int       `json:"schema_version"`
}

func (m *MemoryStored) NodeType() string   { return "memory_stored" }
func (m *MemoryStored) SchemaVersion() int { return m.Version }
func (m *MemoryStored) Validate() error {
    if m.Scope == ""        { return fmt.Errorf("memory_stored: scope is required") }
    if m.SessionID == ""    { return fmt.Errorf("memory_stored: session_id is required") }
    if m.Author == ""       { return fmt.Errorf("memory_stored: author is required") }
    if m.Key == ""          { return fmt.Errorf("memory_stored: key is required") }
    if m.ContentHash == ""  { return fmt.Errorf("memory_stored: content_hash is required") }
    if m.CreatedAt.IsZero() { return fmt.Errorf("memory_stored: created_at is required") }
    return nil
}

func init() { Register("memory_stored", func() NodeTyper { return &MemoryStored{Version: 1} }) }

// MemoryRecalled records a Bus.Recall call. It names the hashes returned,
// not the content, so auditors can cross-reference MemoryStored without
// the ledger leaking the payload.
// ID prefix: mrc-
type MemoryRecalled struct {
    SessionID    string    `json:"session_id"`
    StepID       string    `json:"step_id,omitempty"`
    TaskID       string    `json:"task_id"`
    ContentHashes []string `json:"content_hashes"`     // SHA-256 hex of each returned memory
    MatchCount   int       `json:"match_count"`
    CreatedAt    time.Time `json:"created_at"`
    Version      int       `json:"schema_version"`
}

func (m *MemoryRecalled) NodeType() string   { return "memory_recalled" }
func (m *MemoryRecalled) SchemaVersion() int { return m.Version }
func (m *MemoryRecalled) Validate() error {
    if m.SessionID == ""    { return fmt.Errorf("memory_recalled: session_id is required") }
    if m.TaskID == ""       { return fmt.Errorf("memory_recalled: task_id is required") }
    if m.CreatedAt.IsZero() { return fmt.Errorf("memory_recalled: created_at is required") }
    return nil
}

func init() { Register("memory_recalled", func() NodeTyper { return &MemoryRecalled{Version: 1} }) }
```

Edges emitted:

- `memory_stored --references--> <originating task node>` when TaskID known.
- `memory_recalled --references--> <memory_stored node>` for each matched hash (one edge per hash, joined by `content_hash`). This produces the task-level provenance edges the r1-server 3D view needs.

## 10. 3D visualization hints

Contract only (r1-server-ui-v2 owns the actual rendering code):

- `memory_stored` → shape **cloud/blob** (irregular ellipsoid, 12-ish vertex jitter), color **warm yellow** (`#F4C430`, 80% opacity).
- `memory_recalled` → shape **cloud/blob outline** (wireframe only), color **warm yellow transparent** (`#F4C430`, 25% opacity).
- Edge `memory_stored → <task that recalled it>` uses the standard `references` edge style (dotted-light). One edge per hash match.
- Hover tooltip shows `scope`, `scope_target`, `key`, `author`, `tags`.
- Click opens side panel with the full JSON node (content is not in the node; panel can optionally fetch content via an r1-server API if the operator is authorized — out of scope here).

## 11. Implementation checklist

### 11.1 Schema + PRAGMA

1. [ ] `internal/memory/bus_schema.go` — `const busSchema` holding the `CREATE TABLE IF NOT EXISTS stoke_memory_bus (...)` DDL from §3 plus all six indices. Include the `content_encrypted BLOB` column forward-compat for RS-9 even though this spec never writes it. Idempotent (`IF NOT EXISTS`). Test: `TestBusSchemaIdempotent` runs migration twice on the same file; asserts zero error and row count stable.
2. [ ] `internal/memory/bus_schema.go:applyPragmas(db *sql.DB)` — executes the 7 PRAGMA statements from §5.5 verbatim. Returns on first error. Test: `TestApplyPragmasBaseline` opens a fresh DB, calls `applyPragmas`, then reads each PRAGMA back via `SELECT` and asserts the expected value.
3. [ ] `internal/memory/bus_schema.go:migrate(db *sql.DB)` — wraps schema + pragma apply + index verify under a single `BEGIN/COMMIT` so partial migrations don't leave half-indices. Bumps `PRAGMA user_version` on the wisdom DB from whatever-memory-full-stack-sets to that value with a dedicated `bus_version` column or tracker (coordinate with memory-full-stack on the user_version namespace). Test: `TestMigrateRollback` injects a forced error mid-migration; asserts the table is not present.

### 11.2 Scope type + constants

4. [ ] `internal/memory/scope_bus.go` — `type Scope string` plus the 6 `Scope*` constants from §4 verbatim. Separate file from the existing `internal/memory/scope.go` used by memory-full-stack (that one is hierarchical Global/Repo/Task; this one is visibility). Test: `TestScopeConstantsString` asserts each constant's string form matches the DB value.
5. [ ] `internal/memory/scope_bus.go:ValidScope(s Scope) bool` — range check. Test: `TestValidScope` for all 6 valid + 1 invalid.
6. [ ] `internal/memory/scope_bus.go:RequiresScopeTarget(s Scope) bool` — returns true for Worker, SessionStep, and (conditionally) Global. Test: `TestRequiresScopeTarget`.
7. [ ] `internal/memory/scope_bus.go:ErrScopeForbidden` — sentinel error for worker writes to ScopeAlways. Test: asserted via `TestRememberRejectsWorkerAlways`.

### 11.3 Writer goroutine

8. [ ] New file `internal/memory/bus.go` — package `memory`. Contains `Bus`, `writeRequest`, `NewBus`, `Close`, `Remember`, `Recall`, `writerLoop`, `flushBatch`. Test: alongside, `internal/memory/bus_test.go`.
9. [ ] `internal/memory/bus.go:NewBus(db *sql.DB, opts Options) (*Bus, error)` — runs `migrate(db)`, constructs `Bus{db, writes: make(chan writeRequest, 256), stop: make(chan struct{}), done: make(chan struct{})}`, kicks off `writerLoop` in a goroutine. Returns ready-to-use bus. Test: `TestNewBusStartsWriter` asserts writerLoop is running (send + block test).
10. [ ] `internal/memory/bus.go:(*Bus).Close(ctx) error` — closes `stop`, waits on `done` or `ctx.Done()`. Idempotent (second close is no-op, gated by `sync.Once`). Test: `TestBusCloseIdempotent`, `TestBusCloseCtxTimeout`.
11. [ ] `internal/memory/bus.go:writerLoop()` — exact pseudocode from §5.2. Flush triggers: 256 requests OR 5 ms ticker OR `stop` channel closed. On stop: flush final batch (do not drop queued writes) then close `done`. Test: `TestWriterLoopBatchSize`, `TestWriterLoopTickerWindow`, `TestWriterLoopFlushOnClose`.
12. [ ] `internal/memory/bus.go:flushBatch(batch []writeRequest)` — `db.BeginTx` with IMMEDIATE via a `PRAGMA` hack or driver option (if `github.com/mattn/go-sqlite3` doesn't expose it directly, use `db.Exec("BEGIN IMMEDIATE")` + `db.Exec("COMMIT")` on a dedicated `*sql.Conn` grabbed via `db.Conn(ctx)` to keep the connection pinned). Prepares the UPSERT statement once, loops, collects per-row errors, commits. Test: `TestFlushBatchSingleTx` uses a SQLite trace callback to assert exactly one BEGIN/COMMIT per batch.
13. [ ] `internal/memory/bus.go:buildUpsertStmt()` — returns the parameterized UPSERT SQL: `INSERT INTO stoke_memory_bus (created_at, expires_at, scope, scope_target, session_id, step_id, task_id, author, key, content, content_hash, tags, metadata) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(scope, scope_target, key) DO UPDATE SET content=excluded.content, content_hash=excluded.content_hash, tags=excluded.tags, metadata=excluded.metadata, expires_at=excluded.expires_at`. Test: `TestUpsertDedupOnKey` verifies that two Remember calls with the same (scope, scope_target, key) leave one row with updated content.

### 11.4 Remember path

14. [ ] `internal/memory/bus.go:Remember(ctx, req RememberRequest) error` — validates req, computes SHA-256, routes through channel, waits on result. Test: `TestRememberHappyPath`.
15. [ ] `internal/memory/bus.go:validateRemember(req)` — non-empty Content ≤ 100 KB, ValidScope, ScopeAlways only when `!strings.HasPrefix(req.Author, "worker:")`, SessionID non-empty, ScopeSessionStep requires StepID, ScopeWorker requires ScopeTarget. Test: `TestValidateRememberAllBranches` (one case per rejection).
16. [ ] `internal/memory/bus.go:computeContentHash(s string) string` — `hex(sha256(s))`. Test: `TestComputeContentHashStable`.
17. [ ] `internal/memory/bus.go` — `Remember` backpressure: `select { case b.writes <- wr: case <-ctx.Done(): return ctx.Err() }`. Test: `TestRememberBlocksWhenFull` fills channel to 256, asserts the 257th blocks until one drains.
18. [ ] `internal/memory/bus.go` — `Remember` result wait: `select { case err := <-wr.result: return err; case <-ctx.Done(): return ctx.Err() }`. Even on cancellation, the enqueued write still commits. Test: `TestRememberCtxCancelAfterEnqueue` cancels ctx after enqueue; asserts row is still eventually present.
19. [ ] `internal/memory/bus.go` — ledger emission: after successful UPSERT, call `ledger.Write(&nodes.MemoryStored{...})` with the computed fields. Handle ledger errors by logging and proceeding (don't fail Remember because the ledger is slow). Test: `TestRememberEmitsLedgerNode`.
20. [ ] `internal/memory/bus.go` — hub emission: `hub.Publish("memory.stored", hub.MemoryStoredEvent{...})`. Test: `TestRememberEmitsHubEvent` with fake subscriber.
21. [ ] `internal/memory/bus.go` — STOKE emission: `emitter.EmitStoke("stoke.memory.store", map[string]any{"scope": ..., "scope_target": ..., "key": ..., "content_hash": ..., "ledger_node_id": ...})`. Test: `TestRememberEmitsStokeEvent` reads the stream.jsonl and asserts event shape.

### 11.5 Recall path

22. [ ] `internal/memory/bus.go:Recall(ctx, req RecallRequest) []Memory` — builds WHERE clause, SELECTs, then enqueues an `UPDATE read_count` write through the writer goroutine (fire-and-forget). Test: `TestRecallHappyPath`.
23. [ ] `internal/memory/bus.go:buildRecallWhere(req)` — returns the `(where string, args []any)` pair matching §7 exactly, including the `ORDER BY scope = 'always' DESC, id ASC`. Test: `TestBuildRecallWhere` snapshots the SQL against a golden file.
24. [ ] `internal/memory/bus.go` — tag filter: when `req.Tags` non-empty, append `AND EXISTS (SELECT 1 FROM json_each(tags) WHERE value = ?)` per tag. Test: `TestRecallTagFilter`.
25. [ ] `internal/memory/bus.go` — `UPDATE read_count` path: assemble a synthetic writeRequest whose flush branch touches `UPDATE stoke_memory_bus SET read_count = read_count + 1, last_read_at = ? WHERE id IN (...)`. Keeps single-writer invariant. Test: `TestRecallIncrementsReadCount`.
26. [ ] `internal/memory/bus.go` — ledger emission: one `MemoryRecalled` node per Recall call carrying the list of content hashes. Test: `TestRecallEmitsLedgerNode`.
27. [ ] `internal/memory/bus.go` — per-hash `references` edge: for each match, emit `ledger.Edge(recallNodeID, "references", storedNodeIDByHash[h])`. Lookup via a cheap in-memory map or a helper SELECT. Test: `TestRecallEmitsReferenceEdges`.
28. [ ] `internal/memory/bus.go` — hub + STOKE emission for Recall (`memory.recalled` / `stoke.memory.recall`). Test: `TestRecallEmitsHubAndStokeEvents`.

### 11.6 Scope-visibility matching

29. [ ] `internal/memory/bus_scope_match.go` — dedicated file for `buildRecallWhere`, split from `bus.go` for readability. Test: `TestScopeVisibilityWorkerIsolated`, `TestScopeVisibilitySessionStepMatches`, `TestScopeVisibilityAllSessionsVisible`, `TestScopeVisibilityGlobalRepoHashMatch`, `TestScopeVisibilityAlwaysFirstInResults`.
30. [ ] `internal/memory/bus_scope_match.go:containsScope(scopes []Scope, s Scope) bool` — helper. Test: N/A.
31. [ ] `internal/memory/bus.go` — `Recall` respects `expires_at`: `expires_at IS NULL OR expires_at > :now`. Test: `TestRecallExpiredRowsHidden`.

### 11.7 Ledger nodes

32. [ ] `internal/ledger/nodes/memory.go` — new file with `MemoryStored` + `MemoryRecalled` per §9 verbatim. `init()` blocks call `Register("memory_stored", ...)` and `Register("memory_recalled", ...)`. Test: `internal/ledger/nodes/memory_test.go` exercises `Validate()` for each required-field-missing case plus a happy path.
33. [ ] `internal/ledger/nodes/memory.go` — ensure `ContentHash` is always SHA-256 hex, never raw content. Validate rejects content that contains whitespace (cheap heuristic to catch accidental content leaks into the hash field). Test: `TestMemoryStoredRejectsNonHashContentHash`.
34. [ ] `internal/ledger/nodes/memory.go` — ensure `MemoryRecalled.ContentHashes` entries are all hex of length 64. Test: `TestMemoryRecalledValidatesHashFormat`.

### 11.8 Worker prompt integration

35. [ ] `cmd/stoke/sow_native.go:buildSOWNativePromptsWithOpts()` — add `## Active Memories` block after the wisdom injection (search for `## Relevant learnings` to find the insertion point ~line 3892). Code matches the §8 snippet. Only emits the block when `len(mems) > 0`. Test: `cmd/stoke/sow_native_bus_test.go:TestPromptInjectionWhenMemoriesPresent`, `TestPromptInjectionSkippedWhenEmpty`.
36. [ ] `cmd/stoke/sow_native.go` — token-cap the injected block via `tokenest.Count`; drop oldest first when over cap. Hard cap default 1200 tokens (reuse constant from memory-full-stack hook 2). Test: `TestActiveMemoriesBlock1200Cap`.
37. [ ] `cmd/stoke/sow_native.go` — flag guard: when `os.Getenv("STOKE_MEMORY_BUS") != "1"`, `bus` is nil and the injection block is skipped entirely. Test: `TestPromptInjectionGatedByEnvVar`.
38. [ ] `cmd/stoke/sow_native.go` — pass the `*memory.Bus` down from main into `buildSOWNativePromptsWithOpts` via the options struct. Construct the bus once in `main.go` and share across workers. Test: `TestBusPassedToWorkers`.

### 11.9 HITL + descent integration

39. [ ] `internal/hitl/` — when a HITL response comes back, the resolver calls `bus.Remember(RememberRequest{Scope: ScopeSession, SessionID: sess.ID, Author: "operator", Key: fmt.Sprintf("hitl:%s", hitlID), Content: "<operator decision>"})` so other workers in the same session see the decision without re-asking. Test: `internal/hitl/bus_integration_test.go`.
40. [ ] `internal/descent/` — tier escalation handlers call `bus.Remember(RememberRequest{Scope: ScopeSessionStep, ScopeTarget: step.ID, SessionID: sess.ID, Author: "system", Key: "descent:tier:"+tier, Content: "escalated to tier " + tier + "; reason: " + reason})`. Test: `internal/descent/bus_integration_test.go`.
41. [ ] `harness/tools/` — register new tools `remember` and `recall` that workers can call directly. Schema: `remember(scope, scope_target, key, content, tags?, expires_at?)`, `recall(limit?, tags?)`. Both validate scope via `ValidScope`. Test: `harness/tools/memory_bus_tool_test.go`.

### 11.10 r1-server read-only cursor polling

42. [ ] `cmd/r1-server/bus_reader.go` — new file; background goroutine that opens the wisdom DB read-only via the DSN in §5.6, applies `PRAGMA query_only=1`, and polls `SELECT id, created_at, scope, scope_target, session_id, step_id, task_id, author, key, content_hash, tags FROM stoke_memory_bus WHERE id > :last_seen ORDER BY id LIMIT 256` every 200 ms. `last_seen` starts at 0 and is persisted in `r1-server.db` across restarts. Test: `cmd/r1-server/bus_reader_test.go:TestBusReaderCursor`.
43. [ ] `cmd/r1-server/bus_reader.go:handleUDSWake()` — optional: `net.ListenUnix` on `<data_dir>/r1-server.sock`, wake the polling goroutine on any byte received. Silent fallback when the socket is unavailable. Test: `TestBusReaderUDSWake`.
44. [ ] `cmd/r1-server/bus_reader.go:handleFsnotifyFallback()` — when UDS is not active, subscribe to fsnotify `WRITE` events on `<db>-wal`. Same wake semantics. Test: `TestBusReaderFsnotifyWake` (skipped on platforms without fsnotify).
45. [ ] `cmd/r1-server/bus_reader.go` — ingest polled rows into r1-server's `session_events` table with `event_type='memory.stored'` + a separate table `memory_bus_rows` for the full bus row (ui-v2 spec consumes this). Test: `TestBusReaderIngest`.
46. [ ] `cmd/r1-server/bus_reader.go` — coordinate with `r1-server-ui-v2.md`: the UI owns rendering, this reader only lands rows. Do not add any HTML/CSS here.
47. [ ] `cmd/stoke/main.go` — after `bus.NewBus`, best-effort dial the UDS at `<data_dir>/r1-server.sock` and stash the conn on the Bus; `flushBatch` writes one byte per flush. Silent failure when socket is missing. Test: `TestBusSignalsUDS`.

### 11.11 Wiring + lifecycle

48. [ ] `cmd/stoke/main.go` — open the wisdom SQLite handle (or reuse the existing one from `internal/wisdom/sqlite.go`), pass it into `memory.NewBus`. Defer `bus.Close(ctx)` at process exit. Test: `TestMainBusLifecycle`.
49. [ ] `cmd/stoke/main.go` — `STOKE_MEMORY_BUS` flag check: when unset or `"0"`, skip `memory.NewBus` entirely and pass `nil` down. All Remember/Recall call sites must no-op safely on `bus == nil`. Test: `TestBusFlagOffNoOps`.
50. [ ] `internal/memory/bus.go` — `(*Bus) Remember` / `Recall` short-circuit on `b == nil`: return `nil` / `[]Memory{}` respectively. Covers the flag-off path. Test: `TestNilBusRemember`, `TestNilBusRecall`.
51. [ ] `app/` startup — construct `hub.MemoryStoredEvent` and `hub.MemoryRecalledEvent` types, register them in `internal/hub/events.go`. Test: `internal/hub/memory_events_test.go`.

### 11.12 Cross-cutting tests

52. [ ] `internal/memory/bus_race_test.go` — `go test -race` with 4 goroutines × 250 Remember calls each → 1000 rows committed, all distinct IDs, no deadlock. Plus one concurrent goroutine running `Recall` in a loop; must not block writers. Must not observe torn rows.
53. [ ] `internal/memory/bus_crossproc_test.go` — spawn a subprocess (or second `*sql.DB` handle with the read-only DSN) that opens the same DB file while the first handle writes; assert read-only handle sees new rows within 500 ms of commit (polling cadence plus one tick).
54. [ ] `internal/memory/bus_scope_matrix_test.go` — exhaustive matrix: for each of the 6 scopes × 5 caller contexts (same-session/same-step/same-task/different-session/different-instance), assert correct visibility. 30 cases minimum.
55. [ ] `internal/memory/bus_expiry_test.go` — insert a row with `expires_at = now - 1s`, assert Recall does not return it; with `expires_at = now + 1h`, assert it does.
56. [ ] `internal/memory/bus_ledger_test.go` — Remember → one `memory_stored` node, correct `content_hash`. Recall returning 3 rows → one `memory_recalled` node + 3 `references` edges.
57. [ ] `internal/memory/bus_bench_test.go` — `BenchmarkRememberBatched` targets ≥10k ops/sec under 8 concurrent senders. `BenchmarkRecallReadonly` targets ≥100k ops/sec on a prewarmed 10k-row table.
58. [ ] `cmd/stoke/sow_native_bus_test.go` — end-to-end: spin up bus, Remember a ScopeSession memory, build a worker prompt for a second worker in the same session, assert the `## Active Memories` block contains the Content.

## 12. Acceptance criteria

- `go build ./cmd/stoke && go build ./cmd/r1-server && go test ./... && go vet ./...` all green.
- `go test -race ./internal/memory -run TestBusRace` with 4 writers × 250 Remember calls → 1000 rows committed, all unique IDs, no deadlock, elapsed time within 2 s.
- Cross-process read works: with stoke actively writing, a second process opens the DB via `file:/.../wisdom.db?mode=ro&_journal_mode=WAL&_busy_timeout=5000` + `PRAGMA query_only=1`, reads rows, never blocks the writer.
- Ledger emits one `memory_stored` node per `Remember` call, with `content_hash = sha256(content)` (never raw content). `go test ./internal/ledger/nodes -run TestMemoryStoredValidates` green.
- `Recall` with `TaskID='X'` against a row `scope=worker, scope_target='X'` returns the row; against a row `scope=worker, scope_target='Y'` returns no row. No cross-worker private scope leakage.
- `Recall` with `SessionID='S1', StepID='S2-AC3'` returns only that session-step's memories plus session-wide, all-sessions, matching-global, and always memories; returns zero memories of other sessions' step scopes.
- `ScopeAlways` write from `author="worker:w1"` returns `ErrScopeForbidden`; same write from `author="operator"` succeeds.
- Prompt injection block appears under `## Active Memories` H2 with the correct format when `STOKE_MEMORY_BUS=1` and memories are returned; block is absent when the flag is unset.
- r1-server polling reader picks up new rows within 500 ms (polling cadence) and within 50 ms when the UDS signal is wired.

## 13. Testing

- **Per-file unit tests** — one `_test.go` alongside every new file (items 1-51).
- **Concurrency safety under `-race`** — item 52: `TestBusRace` with 4 writers × 250 Remembers + 1 concurrent Recall loop. No data races. No deadlock under 30 s test timeout.
- **Integration / eventual consistency** — item 53: two goroutines, one calling Remember, one calling Recall with an ever-advancing `Since` cursor; verifies every Remembered row is observed by Recall within one flush window (≤10 ms).
- **Cross-process read** — a second process (or a second `*sql.DB` opened with the read-only DSN in the same test binary) polls while the primary writes; asserts new rows visible within 500 ms.
- **Scope matrix** — item 54: 30-case matrix covering every (scope, caller-context) pair.
- **Expiry** — item 55: past `expires_at` hides the row; future `expires_at` surfaces it.
- **Ledger emission** — item 56: exact node count + edge count assertions.
- **Benchmarks (stretch)** — item 57: `BenchmarkRememberBatched` ≥10k ops/sec on a modern Linux laptop under 8 concurrent senders; `BenchmarkRecallReadonly` ≥100k ops/sec on a 10k-row prewarmed table.
- **End-to-end prompt injection** — item 58: seed bus, spin up a fake worker, assert the `## Active Memories` block renders.

## 14. Rollout

The bus is flag-gated via `STOKE_MEMORY_BUS=1` for the first two weeks after landing. With the flag unset:

- `memory.NewBus` is not called; the `*memory.Bus` passed into the worker prompt builder is `nil`.
- `Bus.Remember` and `Bus.Recall` on a `nil` receiver are no-ops (return `nil` / `[]Memory{}`).
- No prompt injection block is added.
- No ledger `memory_stored` / `memory_recalled` nodes are emitted.
- No STOKE envelope events are emitted.
- The existing wisdom extraction + `stoke_memories` + memory-full-stack flows remain unchanged. The bus table is still migrated (idempotent `CREATE IF NOT EXISTS`) so enabling the flag later does not require a cold migration.

With the flag on:

- Workers gain the `remember` / `recall` tool calls.
- The `## Active Memories` block is injected into every worker system prompt.
- HITL and descent handlers emit system/operator memories.
- r1-server's background reader picks up rows and the ui-v2 memory explorer panel lights up.

Flip path: after two clean weeks on the ladder suite (no regressions in rung pass-rate, no cost anomalies, no lost writes observed), default the flag on. Provide `STOKE_MEMORY_BUS=0` as the escape hatch so operators hitting a regression can roll back without a binary swap.

Graceful degradation: the flag-off path must be observably indistinguishable from pre-RS-7 Stoke. Confirmed via `TestBusFlagOffNoOps` and by running the full rung suite with the flag unset.
