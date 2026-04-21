<!-- STATUS: ready -->
<!-- CREATED: 2026-04-20 -->
<!-- DEPENDS_ON: spec-2 (hitl for NDJSON Operator), spec-3 (router + eventlog + Executor), optional spec-4/5 -->
<!-- BUILD_ORDER: 7 -->

# Operator UX + Memory — Implementation Spec

## Overview

This spec ships the human surface area for Stoke plus the full persistent-memory stack (decision D-2026-04-20-04). Seven related parts land together because they share the same operator mental model (plan → approve → execute → learn) and the same three integration seams (streamjson events, bus publish, agentloop prompt injection):

- **Part A**: `stoke plan` separate command producing an approvable `plan.json` artifact (D27, RT-11 Devin mode split).
- **Part B**: `Operator` ask/notify interface with `terminal` and `ndjson` implementations (D28, RT-11 Manus split).
- **Part C**: Intent Gate (DIAGNOSE vs IMPLEMENT) that masks write tools pre-dispatch (D29, RT-11 Factory DROID verbatim).
- **Part D**: Full memory stack — SQLite+FTS5+sqlite-vec, scope hierarchy, embedding 3-way fallback, consolidation pipeline, auto-retrieval at 4 injection points, adapters for `wisdom`/`skill`, and a `stoke memory` subcommand (D30, RT-08 end-to-end).
- **Part E**: Live meta-reasoner that runs **between** sessions and injects learned prevention rules into the next session's worker prompts (D31, RT-08 §9).
- **Part F**: `progress.md` renderer emitted at session/task/AC boundaries for human-readable runtime visibility (D33).
- **Part G**: Cost dashboard TUI widget over existing `internal/costtrack/` (D32).

Part D is the largest and drives the 8k word budget — the full-stack memory decision requires SQLite DDL, embedding fallback logic, consolidation pseudocode, and four auto-retrieval injection points specified concretely.

## Stack & Versions

- Go 1.22+
- SQLite (existing — already used by `session.SQLStore`, `wisdom/sqlite.go`)
- FTS5 (in-tree SQLite extension)
- `sqlite-vec` v0.1+ loaded as runtime extension (silent BM25-only fallback when absent)
- Anthropic Messages API via `internal/agentloop` (Sonnet 4.6 for consolidation + meta-reasoner; Haiku 4.5 for intent gate)
- OpenAI `text-embedding-3-small` for remote embeddings (Matryoshka truncation to 512d)
- `llama.cpp` HTTP server running `nomic-embed-text-v2` for local embeddings (optional)
- TUI: existing `github.com/charmbracelet/bubbletea` stack in `internal/tui/`

## Existing Patterns to Follow

- Plan/SOW structures: `internal/plan/sow.go` (Session, Task, AcceptanceCriterion)
- Lead-dev briefing: existing briefing builder in `internal/plan/` reused verbatim for plan.json
- Pre-flight AC: `PreflightACCommands` already in descent pipeline; reuse for plan pre-flight
- SOW orchestration: `cmd/stoke/sow_native.go` + `cmd/stoke/descent_bridge.go`
- Worker prompt injection points: `sow_native.go:3853-3950+` (RepoMap, API surface, wisdom, canonical names, skills)
- Event emitter: `internal/streamjson/emitter.go` (C1 — extend, do not fork)
- Bus: `internal/bus/bus.go` + `wal.go` (C3 — all parts publish to bus AND streamjson)
- Memory tiers + contradiction: `internal/memory/tiers.go`, `internal/memory/contradiction.go` (H-47, H-19 — DO NOT REPLACE, extend)
- Wisdom SQLite reference: `internal/wisdom/sqlite.go`
- Session SQL: `internal/session/sqlite.go` (WAL journal mode pattern)
- Harness tool auth: `internal/harness/tools/` (DIAGNOSE mode masks writes here)
- CostTrack: `internal/costtrack/` already tracks per-model cost with budget alerts
- TUI panels: `internal/tui/` existing Dashboard/Focus/Detail
- HITL stdin protocol: `internal/hitl/` (spec-2) — ndjson Operator consumes it
- Router: `internal/router/` (spec-3) — intent_gate extends it
- Event log: `internal/eventlog/` (spec-3) — `plan.approved` persisted here

## Library Preferences

- SQLite driver: `modernc.org/sqlite` (already present, pure-Go, CGO-free)
- sqlite-vec: loaded via `sqlite3_load_extension("vec0")` at open time; if `Ok()` false, continue without vectors
- Embeddings HTTP client: stdlib `net/http` with 30s timeout
- TUI progress bar: existing bubbletea `progress` component
- JSON for plan.json: stdlib `encoding/json` (indent 2 spaces)
- ULID: `github.com/oklog/ulid/v2` (already in go.mod per wisdom/sqlite)

---

# PART A — `stoke plan` command

## A.1 Command surface

```
stoke plan --sow path/to/spec.md [--output plan.json] [--yes] [--estimate-only] [--interactive]
stoke execute --plan plan.json [--resume]
stoke ship --sow path/to/spec.md           # existing combined path, unchanged
```

- `stoke plan` runs planning + pre-flight to completion, writes `plan.json`, prints a summary, **exits without executing**.
- `stoke execute --plan plan.json` replays the approved plan deterministically.
- `stoke ship` remains the CI/CD shortcut (plan → auto-approve → execute in one process).
- `--yes` auto-appends an approval to the plan artifact on write (use for CI).
- `--estimate-only` prints cost estimate and the DAG to stderr, skips artifact write.
- `--interactive` opens a terminal Operator session for approval (blocks on `Operator.Confirm`).

New file: `cmd/stoke/plan_cmd.go`. Roughly mirrors existing SOW command but stops at `plan.ready` emission. The executor path is a thin wrapper that asserts `bus.Event{Kind:"plan.approved"}` is present in the eventlog for `plan_id` before dispatching.

## A.2 plan.json shape

```json
{
  "plan_id": "pln_a1b2c3d4",
  "sow_path": "./specs/descent-hardening.md",
  "sow_hash": "sha256:5f2a...",
  "created_at": "2026-04-20T15:32:11Z",
  "stoke_version": "0.42.0",
  "dag": {
    "nodes": [
      {
        "id": "S1.T1",
        "kind": "execute",
        "title": "Add FileRepairCounts to DescentConfig",
        "file_scope": ["internal/plan/verification_descent.go"],
        "deps": [],
        "grpw": 0.81,
        "stance": "dev",
        "intent": "IMPLEMENT",
        "est_cost_usd": 0.42,
        "est_tokens": 18000,
        "est_duration_s": 90
      },
      {
        "id": "S1.T2",
        "kind": "execute",
        "title": "Enforce cap in T4 before RepairFunc",
        "file_scope": ["internal/plan/verification_descent.go"],
        "deps": ["S1.T1"],
        "grpw": 0.77,
        "stance": "dev",
        "intent": "IMPLEMENT",
        "est_cost_usd": 0.38,
        "est_tokens": 16000,
        "est_duration_s": 75
      }
    ],
    "edges": [{"from": "S1.T1", "to": "S1.T2", "kind": "data"}]
  },
  "briefings": {
    "S1.T1": {
      "system_prompt_hash": "sha256:ab12...",
      "context_bundle_ref": "ctx_9f8e7d",
      "repo_map_tokens": 2400,
      "skill_matches": ["go-refactor"],
      "canonical_names": ["FileRepairCounts", "MaxRepairsPerFile", "DescentConfig"]
    }
  },
  "preflight": {
    "protected_files_clean": true,
    "baseline_commit": "8611d48",
    "snapshot_ref": "snap_2026_04_20_153210",
    "repo_clean": true,
    "skills_detected": ["go"],
    "ac_preflight": {
      "S1.T1.AC1": {"command": "go build ./...", "status": "green"},
      "S1.T1.AC2": {"command": "go vet ./...", "status": "green"}
    },
    "env_classifier": {
      "build_required": ["GOPATH", "GOCACHE"],
      "runtime_only": ["ANTHROPIC_API_KEY", "OPENAI_API_KEY"],
      "ambiguous": []
    }
  },
  "cost_estimate": {
    "total_usd": 2.10,
    "p95_usd": 4.00,
    "token_budget": 220000,
    "by_stance": {"dev": 1.60, "reviewer": 0.35, "descent": 0.15}
  },
  "risks": [
    {"kind": "protected_file_touch", "path": "internal/plan/verification_descent.go", "severity": "info"},
    {"kind": "ac_soft_pass_likely", "ac_id": "S1.T1.AC2", "reason": "env-classifier flagged zero", "severity": "low"}
  ],
  "approval": null
}
```

On approval (via `--yes` or `Operator.Confirm`), the `approval` block is populated:

```json
"approval": {
  "actor": "operator:hello@relay.one",
  "ts": "2026-04-20T15:33:42Z",
  "mode": "interactive",
  "event_id": "ev_01HW..."
}
```

## A.3 plan.approved bus event

- `bus.Event{Kind: "plan.approved", Actor, Ts, Data: {plan_id, sow_hash, approval_mode}}`
- Persisted to eventlog (spec-3's SQLite `events` table). `stoke execute --plan` asserts: at least one `plan.approved` row with matching `plan_id` AND `sow_hash` equal to `SHA256(current SOW file)`.
- Mismatch → refuse to execute, print reason. Operator re-runs `stoke plan` for the new SOW.

## A.4 Resumability

`stoke execute --plan plan.json --resume` replays the eventlog up to the last observed `task.completed` or `session.completed`, then dispatches the next unsatisfied DAG node. Pre-flight is skipped if `baseline_commit` still matches `HEAD`.

---

# PART B — Ask/Notify interactive Operator

## B.1 Interface

New package: `internal/operator/`.

```go
package operator

type NotifyKind int

const (
    NotifyProgress NotifyKind = iota
    NotifyResult
    NotifyMemory
    NotifyCost
)

type Option struct {
    Label       string // short identifier ("retry", "abort")
    Description string // full human sentence
}

type Operator interface {
    // Non-blocking status/progress/result/cost. Must never halt the caller.
    Notify(kind NotifyKind, format string, args ...any)

    // Blocking structured multiple-choice question. Returns chosen Option.Label.
    Ask(prompt string, opts []Option) (string, error)

    // Blocking yes/no confirmation. Returns (true, nil) on affirmative.
    Confirm(prompt string) bool
}
```

Injection: `app.Orchestrator.Operator` (new field). Defaulted by the CLI based on invocation shape:

- `stoke plan --interactive` / `stoke chat` → `operator.NewTerminal(os.Stdin, os.Stdout)`
- `stoke run --output stream-json` (CloudSwarm) → `operator.NewNDJSON(emitter, hitlReader)`
- Test harness → `operator.NewFake(responses)` (maps prompt → canned reply)

## B.2 Terminal implementation

File: `internal/operator/terminal.go`. Uses `internal/tui/` huh-style prompts inline; when not attached to a TTY it falls back to a plain stderr prefix `[stoke notify progress] ...` and a `Scanln` loop for Ask/Confirm.

- `Notify` → stderr, single line, timestamp prefix, color-coded by kind (progress=dim, result=green, memory=blue, cost=yellow).
- `Ask` → vertical list widget + arrow-key select. Returns `Option.Label`.
- `Confirm` → `[y/N]` prompt, accepts `y|yes|1` (case-insensitive).

## B.3 NDJSON implementation

File: `internal/operator/ndjson.go`. Uses spec-2's streamjson extensions:

- `Notify` emits `streamjson.EmitSystem(subtype="stoke.operator.notify", data={kind, msg})` — non-blocking.
- `Ask` emits `streamjson.EmitSystem(subtype="hitl_required", data={ask_id, prompt, options})`, then blocks on `hitl.Reader.Read(ask_id, timeout)` which decodes base64 from supervisor stdin (spec-2 D8). The reply shape is `{"decision": <label>, "reason": str, "decided_by": str}`; `decision` is matched against `Option.Label`.
- `Confirm` is `Ask` with options `[{Label:"yes"}, {Label:"no"}]`; `true` iff returned label is `"yes"`.

Timeouts: 1h standalone default, 15min in CloudSwarm mode (matches spec-2 D8 HITL). On timeout, return the policy-default: for `Confirm` → `false`; for `Ask` → error `operator.ErrTimeout`.

## B.4 Descent T8 soft-pass integration

Amend `internal/plan/verification_descent.go:736-820` soft-pass gate 7 (NEW — appended after the existing 6 gates when Operator attached):

```go
// Gate 7: policy-aware operator sign-off (new)
if cfg.SoftPassPolicy == SoftPassInteractive && cfg.Operator != nil {
    choice, err := cfg.Operator.Ask(
        fmt.Sprintf("Soft-pass AC %s? Intent confirmed, category=%s.", ac.ID, verdict.Category),
        []operator.Option{
            {Label: "accept", Description: "Approve soft-pass and continue"},
            {Label: "retry", Description: "Run T4 repair once more"},
            {Label: "abort", Description: "Fail this session, escalate"},
        },
    )
    if err != nil || choice == "abort" { return Result{Kind: Fail, Reason: "operator rejected soft-pass"} }
    if choice == "retry" { return runT4Again(ctx) }
}
if cfg.SoftPassPolicy == SoftPassStrict && cfg.Operator != nil {
    if !cfg.Operator.Confirm(fmt.Sprintf("Strict mode: really soft-pass %s?", ac.ID)) {
        return Result{Kind: Fail, Reason: "operator rejected strict soft-pass"}
    }
}
// SoftPassAuto: existing behavior (Notify only, auto-grant)
cfg.Operator.Notify(operator.NotifyResult, "soft-pass granted for %s (%s)", ac.ID, verdict.Category)
```

Config (YAML): `descent.soft_pass_policy ∈ {auto, interactive, strict}`. Default `auto` for CI, `interactive` for `stoke chat`/`stoke plan --interactive`.

Governance tier `enterprise` (from operator config) forces `strict`.

---

# PART C — Intent Gate

Adopts Factory DROID's Phase 0 gate verbatim:

> "**Simple Intent Gate (run on EVERY message):** If you will make ANY file changes (edit/create/delete) or open a PR, you are in IMPLEMENTATION mode. Otherwise, you are in DIAGNOSTIC mode. If unsure, ask one concise clarifying question and remain in diagnostic mode until clarified."
>
> "Re-evaluate intent on EVERY new user message."

## C.1 Placement

New file: `internal/router/intent_gate.go`. Extends the spec-3 router.

- Runs on **every worker dispatch** (not every LLM turn — per-dispatch matches stoke's unit of work).
- Hook: `scheduler.Dispatch` calls `router.ClassifyIntent(task, sowExcerpt)` before `harness.Spawn`.
- Re-runs when bus emits `task.sow.updated` for the pending task.

## C.2 Classifier — two stages

**Stage 1 — deterministic action-verb scan** (zero-cost):

- IMPLEMENT verbs: `{add, create, implement, fix, refactor, delete, rename, migrate, port, write, build, apply, edit, patch, update}`
- DIAGNOSE verbs: `{explain, analyze, audit, investigate, review, diagnose, why, inspect, summarize, describe, compare, evaluate}`
- Match against lowercased task title + first 500 chars of SOW excerpt.
- Tie / zero-match / both-present → Stage 2.

**Stage 2 — Haiku LLM fallback** (~$0.001/call):

```
System: You are a task intent classifier. Output exactly one word: IMPLEMENT, DIAGNOSE, or AMBIGUOUS.
User: Task title: {title}
SOW excerpt: {excerpt[:1500]}
Does this task require file edits? Answer IMPLEMENT / DIAGNOSE / AMBIGUOUS.
```

- `IMPLEMENT` → full tool set.
- `DIAGNOSE` → read-only masked tools.
- `AMBIGUOUS` → proceed as DIAGNOSE (safer default per RT-11 open question 4); emit `intent.ambiguous` bus event with Operator.Notify + Ask for reclassification if `--interactive`.

## C.3 Tool authorization (harness/tools layer)

Extend `internal/harness/tools/` authorization model:

| Mode | Allowed | Blocked |
|------|---------|---------|
| IMPLEMENT | read_file, grep, glob, shell, bash, edit, write, multi_edit, git, pnpm, pr_create | (none) |
| DIAGNOSE | read_file, grep, glob, shell(readonly), bash(readonly) | edit, write, multi_edit, git_commit, pnpm install, pr_create, any `rm`/`mv` |

**Readonly bash allowlist**: `ls, cat, head, tail, grep, rg, find, file, stat, wc, git status, git log, git diff, git show, go list, go vet, go doc`. Blocked in readonly: `rm, mv, cp -f, > /path, >> /path, git commit, git push, git reset, pnpm install, npm install`.

DIAGNOSE output writes to `reports/<task-id>.md`, ledger node kind `diagnostic_report` (not `code_change`).

## C.4 Re-evaluation

Bus subscriber on `task.sow.updated{task_id}`:

```go
bus.SubscribeOn("task.sow.updated", func(ev bus.Event) {
    newIntent, _ := router.ClassifyIntent(task, excerpt)
    if newIntent != task.Intent {
        bus.Publish("task.intent.changed", map[string]any{
            "task_id": task.ID, "from": task.Intent, "to": newIntent,
        })
        scheduler.Requeue(task.ID) // re-dispatch with new tool mask
    }
})
```

---

# PART D — Memory system (full stack per D-2026-04-20-04)

Extends — does not replace — the existing CoALA tier model in `internal/memory/tiers.go` and contradiction detection in `internal/memory/contradiction.go` (RT-STOKE-SURFACE §7).

## D.1 SQLite + FTS5 backend

New file: `internal/memory/sqlite.go`. Mirrors `internal/wisdom/sqlite.go` structure.

### DDL

```sql
-- Main table (one row per memory item)
CREATE TABLE IF NOT EXISTS memories (
    id         TEXT PRIMARY KEY,                -- ULID
    agent_id   TEXT NOT NULL,
    task_id    TEXT,                            -- nullable (global/repo scope)
    scope      TEXT NOT NULL CHECK (scope IN ('global','repo','task')),
    repo_hash  TEXT,                            -- nullable; required when scope='repo' or 'task'
    task_type  TEXT,                            -- nullable; required when scope='task'
    tier       TEXT NOT NULL CHECK (tier IN ('working','episodic','semantic','procedural')),
    category   TEXT,                            -- gotcha|pattern|preference|fact|anti_pattern|fix|meta_rule|...
    content    TEXT NOT NULL,
    metadata   TEXT NOT NULL DEFAULT '{}',      -- JSON: tags[], evidence_ids[], supersedes, source
    importance REAL NOT NULL DEFAULT 5.0,       -- 0..10
    confidence REAL NOT NULL DEFAULT 0.7,       -- 0..1
    status     TEXT NOT NULL DEFAULT 'active',  -- active|consolidated|superseded|archived|merged
    use_count  INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL,                -- unix epoch seconds
    last_used  INTEGER NOT NULL,
    consolidated_at INTEGER,
    embedding  BLOB                             -- NULL when embeddings disabled
);

CREATE INDEX IF NOT EXISTS idx_memories_scope ON memories(scope, repo_hash, task_type);
CREATE INDEX IF NOT EXISTS idx_memories_tier ON memories(tier, status);
CREATE INDEX IF NOT EXISTS idx_memories_importance ON memories(importance DESC, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_memories_agent_task ON memories(agent_id, task_id);

-- FTS5 virtual table (BM25 unconditional)
CREATE VIRTUAL TABLE IF NOT EXISTS memories_fts USING fts5(
    content, category, metadata,
    content='memories', content_rowid='rowid',
    tokenize='porter unicode61'
);

-- Triggers to keep FTS in sync
CREATE TRIGGER IF NOT EXISTS memories_ai AFTER INSERT ON memories BEGIN
    INSERT INTO memories_fts(rowid, content, category, metadata) VALUES (new.rowid, new.content, new.category, new.metadata);
END;
CREATE TRIGGER IF NOT EXISTS memories_ad AFTER DELETE ON memories BEGIN
    INSERT INTO memories_fts(memories_fts, rowid, content, category, metadata) VALUES('delete', old.rowid, old.content, old.category, old.metadata);
END;
CREATE TRIGGER IF NOT EXISTS memories_au AFTER UPDATE ON memories BEGIN
    INSERT INTO memories_fts(memories_fts, rowid, content, category, metadata) VALUES('delete', old.rowid, old.content, old.category, old.metadata);
    INSERT INTO memories_fts(rowid, content, category, metadata) VALUES (new.rowid, new.content, new.category, new.metadata);
END;

-- sqlite-vec virtual table (optional — created only if vec0 extension loads)
-- CREATE VIRTUAL TABLE IF NOT EXISTS memories_vec USING vec0(
--     id TEXT PRIMARY KEY,
--     embedding FLOAT[512]
-- );
```

PRAGMAs at open: `journal_mode=WAL`, `synchronous=NORMAL`, `foreign_keys=ON`, `busy_timeout=5000`.

### sqlite-vec setup

```go
db, err := sql.Open("sqlite", path)
if err != nil { return nil, err }
_, err = db.Exec(`SELECT load_extension('vec0')`)
hasVec := err == nil
if hasVec {
    db.Exec(`CREATE VIRTUAL TABLE IF NOT EXISTS memories_vec USING vec0(id TEXT PRIMARY KEY, embedding FLOAT[512])`)
}
```

When `hasVec == false`, all semantic similarity queries silently fall back to FTS5 BM25.

### Query API

```go
type Storage interface {
    Put(ctx context.Context, m Item) error
    Get(ctx context.Context, id string) (Item, error)
    Query(ctx context.Context, q Query) ([]Item, error)
    Delete(ctx context.Context, id string) error
    Vote(ctx context.Context, id string, delta int) error   // wisdom-style upvote
    Consolidate(ctx context.Context, task string) error
}

type Query struct {
    Scope       Scope          // Global|Repo|Task|Auto
    RepoHash    string
    TaskType    string
    Tiers       []Tier
    MinImport   float64
    Text        string         // BM25 query (optional)
    Embedding   []float32      // cosine query (optional, needs sqlite-vec)
    CoreLimit   int            // top-K by importance+recency
    QueryLimit  int            // top-K by similarity
    Include     []string       // category allowlist
    Exclude     []string
    HybridAlpha float64        // 0 = BM25 only, 1 = vector only, default 0.6 (RRF fusion)
}
```

## D.2 Scope hierarchy

New file: `internal/memory/scope.go`.

```
.stoke/memory/
  memory.db                    # SQLite (all scopes in one file, filtered by scope column)
  global/
    operator.json              # flat-JSON mirror for inspection; DB is source of truth
    patterns.json
    agents.json
  repo/<repo-hash>/
    conventions.json
    failures.json
    topology.json
  task/<task-type>/
    code.json
    research.json
    deploy.json
```

`<repo-hash>` = `SHA256(git-root-absolute-path)` first 16 hex chars (stable across workdirs of same repo).
`<task-type>` = one of `code|research|deploy|review|diagnose` (matches Router TaskType).

### Scope resolution order

Query with `Scope: Auto` applies a UNION with specificity preference:

```
SELECT * FROM memories
WHERE status='active' AND (
  (scope='task'   AND task_type = :tt AND repo_hash = :rh) OR
  (scope='repo'   AND repo_hash = :rh) OR
  (scope='global')
)
ORDER BY
  CASE scope WHEN 'task' THEN 3 WHEN 'repo' THEN 2 ELSE 1 END DESC,
  importance DESC, confidence DESC, last_used DESC
LIMIT :k;
```

**Conflict rule**: when two memories contradict (detected by existing `contradiction.DetectContradictions`), the more-specific scope wins. Task beats repo beats global.

## D.3 Embedder 3-way fallback

New file: `internal/memory/embed.go`.

```go
type Embedder interface {
    Embed(ctx context.Context, text string) ([]float32, error)
    Dim() int           // 512 for truncated OpenAI / nomic; 0 for NoopEmbedder
    Backend() string    // "openai" | "local-nomic" | "noop"
}

func AutoDetect(cfg Config) Embedder {
    // 1. Remote API
    if key := os.Getenv("OPENAI_API_KEY"); key != "" && cfg.EmbedProvider != "local" {
        e := &OpenAIEmbedder{Key: key, Model: "text-embedding-3-small", TruncateTo: 512}
        if e.ping(ctx) == nil {
            log.Printf("[memory] embeddings: openai text-embedding-3-small -> 512d (Matryoshka)")
            return e
        }
    }
    // 2. Local llama.cpp daemon
    localURL := firstNonEmpty(os.Getenv("STOKE_EMBED_LOCAL_URL"), "http://127.0.0.1:11434")
    if e := (&LocalEmbedder{BaseURL: localURL, Model: "nomic-embed-text-v2", TruncateTo: 512}); e.ping(ctx) == nil {
        log.Printf("[memory] embeddings: local nomic-embed-text-v2 @ %s -> 512d", localURL)
        return e
    }
    // 3. BM25-only
    log.Printf("[memory] embeddings: disabled (BM25-only)")
    return NoopEmbedder{}
}
```

### Decision tree

```
OPENAI_API_KEY set AND cfg.EmbedProvider != "local"?
  └─ YES → ping OpenAI embed endpoint (1s timeout)
           ├─ 200 OK → use OpenAI 3-small, truncate 1536→512 (slice [:512])
           └─ fail   → fall through

llama.cpp daemon at STOKE_EMBED_LOCAL_URL (or :11434) reachable?
  └─ YES → ping local /embed endpoint
           ├─ 200 OK → use nomic-embed-text-v2 local, truncate to 512 if wider
           └─ fail   → fall through

→ NoopEmbedder: Query hybrid_alpha forced to 0.0; vector queries return []; BM25 only.
```

Startup logs chosen backend exactly once. Rechecked on every `memory.Consolidate` run.

### Write path

On `Storage.Put`, if `Embedder.Dim() > 0`:

```go
vec, err := embedder.Embed(ctx, item.Content)
if err == nil {
    tx.Exec(`INSERT OR REPLACE INTO memories_vec(id, embedding) VALUES (?, ?)`, item.ID, vecToBlob(vec))
}
```

`vecToBlob` = little-endian float32 array (what sqlite-vec expects). Failure is logged + ignored; BM25 remains functional.

## D.4 Consolidation pipeline

New file: `internal/memory/consolidate.go`. Ported from CloudSwarm's `workflows/memory_consolidation.py`.

### Pseudocode

```
fn Consolidate(ctx, scope, repoHash, taskType) error:
    # 1. Fetch candidates
    episodes = SELECT * FROM memories
               WHERE tier='episodic' AND status='active' AND consolidated_at IS NULL
                 AND <scope filter>
               ORDER BY created_at ASC
               LIMIT 500
    if len(episodes) == 0: return nil

    # 2. Fetch recent semantic for LLM context
    context = SELECT * FROM memories
              WHERE tier='semantic' AND status='active' AND <scope filter>
              ORDER BY importance DESC, last_used DESC
              LIMIT 100

    # 3. Chunk into groups of 50
    for chunk in chunks(episodes, 50):
        # 4. LLM call (Sonnet 4.6)
        prompt = buildConsolidationPrompt(chunk, context)
        out = model.Complete(ctx, prompt)             # JSON array
        blocks = parseBlocks(out)                      # []{content, category, confidence, importance, status, evidence_ids, update_target_id?}

        # 5. Classify
        new, updated, superseded = mergeMemoryBlocks(blocks, context)

        # 6. Transactional write
        tx = db.BeginTx()
        for m in new:
            tx.Put(m)                                  # tier='semantic'
        for (target, patch) in updated:
            tx.Update(target.id, patch)                # confidence += 0.1 clamp 1.0; append evidence_ids; last_used=now
        for (old, replacement) in superseded:
            tx.SetStatus(old.id, "superseded")
            tx.Put(replacement)                        # metadata.supersedes = old.id
        for ep in chunk:
            tx.SetConsolidatedAt(ep.id, now())
        tx.Commit()

        # 7. Backfill embeddings for new/updated
        if embedder.Dim() > 0:
            for m in (new ++ updatedNewItems):
                vec = embedder.Embed(ctx, m.Content)
                tx.UpsertVec(m.id, vec)

    # 8. Hygiene pass
    applyConfidenceDecay()      # UPDATE memories SET confidence = MAX(0.2, confidence * 0.95) WHERE status='active'
    cleanupLowConfidence()      # UPDATE memories SET status='archived' WHERE confidence < 0.2 AND status='active'
    deduplicateByEmbedding()    # see below
    applyRetentionTiers()       # see below

fn deduplicateByEmbedding():
    if NOT hasVec: return
    # For each pair (a, b) created in last 7d with cosine >= 0.92, archive newer:
    # sqlite-vec: SELECT a.id, b.id FROM memories_vec a, memories_vec b
    #             WHERE a.id < b.id AND vec_distance_cosine(a.embedding, b.embedding) <= 0.08
    # (cosine distance = 1 - similarity; 0.08 <=> similarity 0.92)
    for (newer_id) in duplicates:
        UPDATE memories SET status='merged' WHERE id=newer_id

fn applyRetentionTiers():
    # 0-7d:   keep all
    # 7-90d:  archive if importance < 5
    # 90d+:   archive all not already 'active' + importance >= 8
    UPDATE memories SET status='archived'
    WHERE status='active'
      AND ((created_at < now - 7d  AND created_at >= now - 90d AND importance < 5)
        OR (created_at < now - 90d AND importance < 8))
```

### Consolidation prompt (Sonnet 4.6)

```
System: You are a memory consolidator. Given raw episodic memories from an agent, produce
semantic memories that capture reusable lessons. For each episodic item decide:
  NEW        (novel fact not in context) — emit a new semantic block
  REINFORCE  (confirms an existing context block) — emit {update_target_id, confidence+=0.1}
  CONTRADICT (flips an existing block) — emit {update_target_id, status=superseded} + a NEW block
  SKIP       (trivia, task-local, non-reusable) — omit

Output strict JSON array. No prose. Schema:
[{content, category, confidence:0..1, importance:0..10, status, evidence_ids:[task_id], update_target_id?}]

Context (existing semantics, top 100 by importance):
{context}

Episodes (decide per-item):
{chunk}
```

### Trigger points

1. End of SOW (existing meta-reasoner hook).
2. Between sessions (live meta-reasoner — Part E).
3. Size threshold: unconsolidated episodic > 500 rows OR `memory.db` > 50 MB.
4. Nightly cron (hygiene only, no LLM): `stoke memory consolidate --hygiene-only`.

## D.5 Auto-retrieval — 4 injection points

New file: `internal/memory/retrieve.go`. Wires into worker prompt pipeline.

```go
// CoreAndQuery returns (core, querySpecific). Core = importance-gated always-on slice.
func (r *Router) CoreAndQuery(ctx, scope Scope, query string, coreLimit, queryLimit int) (core, qs []Item, err error)
```

### Injection point 1 — Before session planning

Called from `cmd/stoke/plan_cmd.go` during plan build.

```go
core, qs, _ := memRouter.CoreAndQuery(ctx, memory.ScopeAuto, sow.Title+" "+sow.Description, 8, 15)
// core:  importance>=7 semantic, repo-scoped, recent-first
// qs:    embedding similarity (falls back to BM25) on SOW excerpt
planBriefing.RelevantLearnings = formatForPrompt(core, qs)   // bullets: [category] content (tags)
```

Inject into plan prompt under `## Prior Learnings` before `## Task Definition`. Hard cap 1200 tokens.

### Injection point 2 — Before worker dispatch

Called from `sow_native.go` at the injection point between canonical names (line 3909) and skills (line 3918).

```go
files := task.FileScope
sessionCtx := session.Title + " " + strings.Join(files, " ")
core, qs, _ := memRouter.CoreAndQuery(ctx, memory.ScopeAuto, sessionCtx, 5, 8)
// core:  conventions (repo scope) + failures (repo scope) for these files
// qs:    similar prior tasks touching same files

sysPrompt.Append("## Relevant learnings\n")
for _, item := range dedupe(core, qs) {
    sysPrompt.Append(fmt.Sprintf("- [%s] %s (%s)\n", item.Category, item.Content, strings.Join(item.Tags, ",")))
}
```

Hard cap 1200 tokens (truncate lowest-confidence first).

### Injection point 3 — Before delegation

Called from spec-5 delegation code when choosing an agent. Not a prompt — surfaces as a score:

```go
core, _, _ := memRouter.CoreAndQuery(ctx, memory.ScopeGlobal, "agent:"+candidate.Role, 3, 0)
reliabilityScore := weightedAvg(core, func(i Item) float64 { return i.Confidence * (i.Importance/10) })
// Scheduler uses reliabilityScore for tie-breaking among candidate agents.
```

Scope = Global because agent reliability is cross-repo knowledge.

### Injection point 4 — Before verification

Called from `internal/verify/` before running AC commands.

```go
q := fmt.Sprintf("false-positive %s %s", ac.ID, diffSummary)
core, _, _ := memRouter.CoreAndQuery(ctx, memory.ScopeRepo, q, 3, 0)
if len(core) > 0 {
    reviewerPrompt.Append("## Known false positives near this change\n")
    for _, i := range core {
        reviewerPrompt.Append("- " + i.Content + "\n")
    }
}
```

One-liner reminders only, 3 max. Keeps reviewer from re-discovering the same false-positive patterns.

## D.6 Adapters — unify, don't replace

Make existing stores implement `memory.Storage`:

- `internal/wisdom/` → `WisdomAdapter` implements `Storage` with `tier=semantic`, preserves vote semantics (`Vote` maps to wisdom upvote).
- `internal/skill/` → `SkillAdapter` implements `Storage` with `tier=procedural`, read-only (Put returns error) unless called from skillmfr pipeline.
- `internal/memory/memory.go` (flat JSON) → `FlatAdapter` — remains the default when `STOKE_MEMORY_BACKEND=flat`.

Registered in `app/` startup:

```go
router := memory.NewRouter()
router.Register("wisdom", wisdom.NewAdapter(wisdomStore))        // semantic
router.Register("skills", skill.NewAdapter(skillRegistry))       // procedural
router.Register("sqlite", memory.NewSQLStore(".stoke/memory/memory.db", embedder))  // all tiers
router.Register("flat",   memory.NewFlatAdapter(".stoke/memory/")) // legacy fallback
```

Router.Query fans out by tier, merges, de-duplicates on content-hash. Writes route to first adapter claiming the (tier, scope) pair.

Preserves existing V2 bridge adapter (`internal/bridge/` wisdom → bus+ledger) because wisdom writes still go through the same code path.

## D.7 `stoke memory` subcommand

New file: `cmd/stoke/memory_cmd.go`.

```
stoke memory list [--scope global|repo|task] [--tier ...] [--limit N]
stoke memory search "query text" [--scope ...] [--limit 10]
stoke memory consolidate [--scope ...] [--hygiene-only] [--dry-run]
stoke memory export --scope repo --output memories.jsonl
stoke memory import --input memories.jsonl
stoke memory info                       # counts, backend, embed status
```

- `list`: tabular output (id, scope, tier, category, importance, confidence, content excerpt).
- `search`: hybrid search (BM25 + embeddings when available), prints top-K.
- `consolidate`: runs full consolidation pipeline. `--hygiene-only` skips LLM, just runs decay/dedupe/retention.
- `export` / `import`: JSONL for portability. One memory per line.
- `info`: prints backend (flat|sqlite), total rows, embed backend, sqlite-vec presence, last consolidation ts.

---

# PART E — Live meta-reasoner

Extends the existing `internal/plan/meta_reasoner.go` (which runs only at end-of-SOW today) to also run between sessions.

## E.1 Package choice

**Decision: extend `internal/plan/meta_reasoner.go`** (do NOT create `internal/metareason/`).

Rationale: the existing package already owns end-of-SOW meta-reasoning, shares utilities (event-log parsing, prompt building, cost tracking). Creating a new `internal/metareason/` package duplicates wiring and forces both call-sites to diverge. Export one new function `RunLiveMetaReasoner(ctx, sessionID) ([]Rule, error)` from the existing package.

## E.2 Trigger

Subscribe to bus `session.completed`:

```go
bus.SubscribeOn("session.completed", func(ev bus.Event) {
    if os.Getenv("STOKE_META_LIVE") != "1" { return }
    sid := ev.Data["session_id"].(string)
    state := taskstate.Load(sid)
    if state.FailureCount == 0 {
        log.Printf("[meta-reasoner] skipping live pass for %s (0 failures)", sid)
        return  // cheap trick: 60% cost savings on clean runs
    }
    if costtrack.Global.OverBudget() {
        log.Printf("[meta-reasoner] budget exhausted, skipping live pass")
        return
    }
    rules, err := plan.RunLiveMetaReasoner(ctx, sid)
    if err != nil { return }
    for _, r := range rules {
        memRouter.Put(ctx, memory.Item{
            Tier:       memory.Tier("semantic"),
            Scope:      memory.ScopeRepo,
            Category:   "meta_rule",
            Content:    r.Rule,
            Metadata:   map[string]any{"tags": []string{"meta-rule", "auto"}, "source_session": sid},
            Importance: 6.0,
            Confidence: 0.6,
        })
    }
})
```

## E.3 Sequence

```
session.completed (S1) fires
    │
    ├─> check STOKE_META_LIVE=1 and state.FailureCount > 0 and !OverBudget
    │
    ├─> collect inputs:
    │     - S1 bus events (filter to errors, failures, resolutions)
    │     - S1 verify results (AC pass/fail/soft-pass)
    │     - S1 failure fingerprints (via failure.Classify)
    │
    ├─> build prompt:  ~8k tokens in, ~1k tokens out, Sonnet 4.6
    │     "Produce at most 5 JSON prevention rules for S2+."
    │
    ├─> LLM call (~$0.04)
    │
    ├─> parse rules [{rule: str, rationale: str, applies_to_files: [glob]}]
    │
    ├─> write to memRouter (tier=semantic, scope=repo, category=meta_rule, confidence=0.6)
    │
    └─> S2 worker dispatch: injection point 2 auto-picks up the new rules
          via CoreAndQuery → `## Relevant learnings` block, capped 400 tokens
```

Cost: ~$0.04/session transition (RT-08 §9). 10-session SOW adds ~$0.40.

Budget guardrails:
- Reuse `costtrack.OverBudget()` — hard stop.
- Cap injected meta-rules to 400 tokens per worker prompt (drop lowest-confidence first).
- Cap absolute meta-rule count per repo to 50 (FIFO eviction by `last_used` when over).

---

# PART F — Progress tracking (`progress.md`)

New file: `internal/plan/progress_renderer.go`. Emitted at session/task/AC boundaries.

## F.1 Hook

```go
scheduler.OnProgress(func(ev ProgressEvent) {
    path := filepath.Join(".stoke", "runs", ev.RunID, "progress.md")
    renderer.RenderToFile(path, ev.Plan, ev.State)
})
```

Called at: `session.started`, `session.completed`, `task.started`, `task.completed`, `ac.checked`, `descent.tier`, `operator.ask`, `session.failed`.

## F.2 Example output

```markdown
# Stoke Run: pln_a1b2c3d4 (SOW: descent-hardening.md)

**Started:** 2026-04-20 15:32 UTC   **Cost so far:** $1.24 / $4.00 budget

## Session S1: Descent hardening foundation

- [x] T1 Add FileRepairCounts to DescentConfig
  - [x] AC1 go build ./... passes
  - [x] AC2 go vet ./... passes
- [~] T2 Enforce cap in T4 before RepairFunc
  - [x] AC1 go build ./... passes
  - [~] AC2 unit test TestFileCap passes (in descent, attempt 2/3)
- [ ] T3 Emit descent.file_cap_exceeded bus event
  - [ ] AC1 event visible on bus
  - [ ] AC2 streamjson mirror present

## Session S2: Truthfulness contract
- [ ] T1 TRUTHFULNESS_CONTRACT prompt block
- [ ] T2 PreEndTurnCheckFn wiring

---
**Legend:** `[x]` done   `[~]` in-flight   `[ ]` pending   `[!]` failed   `[?]` soft-pass
```

Icon map (UTF-8 chars used in-file, renderer emits the shown ASCII glyphs for plain markdown):

| State    | Char | Meaning |
|----------|------|---------|
| done     | `[x]` | Fully passed |
| in-flight| `[~]` | Running now (with optional `(attempt M/N)` suffix) |
| pending  | `[ ]` | Scheduled, not started |
| blocked  | `[b]` | Waiting on operator Ask |
| failed   | `[!]` | Hard failure, no soft-pass |
| soft-pass| `[?]` | T8 soft-pass (annotated with category) |

Operators `cat` or `tail -f` for live visibility; machines parse sow-state.json for structured data.

---

# PART G — Cost dashboard

Renders existing `internal/costtrack/` data as a TUI widget. No new data sources.

## G.1 Mock

```
┌───────────────────────────── stoke run pln_a1b2c3d4 ──────────────────────────┐
│                                                                                │
│  Cost:      $1.24 / $4.00 budget       [████████░░░░░░░░░░░░░░] 31.0%          │
│  Burn:      $0.037/min                                                         │
│  ETA-to-cap: ~75 min                                                           │
│                                                                                │
│  Breakdown                                                                     │
│  ─────────                                                                     │
│   Workers   $0.92   (74%)  ████████████████████░░░░░░░                         │
│   Reviewers $0.18   (15%)  ████░░░░░░░░░░░░░░░░░░░░░░░                         │
│   Reasoning $0.10   ( 8%)  ██░░░░░░░░░░░░░░░░░░░░░░░░░                         │
│   Descent   $0.04   ( 3%)  █░░░░░░░░░░░░░░░░░░░░░░░░░░                         │
│                                                                                │
│  Session S1    $0.81   6 tasks, 4 done                                         │
│  Session S2    $0.43   5 tasks, 1 done (running T1)                            │
│                                                                                │
└────────────────────────────────────────────────────────────────────────────────┘
```

## G.2 Wiring

- New file: `internal/tui/cost_panel.go`. Bubbletea model with 1s refresh via `tea.Tick`.
- Reads `costtrack.Global.Snapshot()` — returns `{Total, Budget, ByCategory, BySession, WindowRate}` (add `Snapshot()` if not present).
- `BurnRate` = rolling 5-minute `$/min`.
- `ETA` = `(Budget - Total) / BurnRate`; when burn==0 → print `—`.
- Added to existing Dashboard view as a second row; focus pane switches with `Tab`.

---

# Business Logic — cross-part orchestration

## Plan → approve → execute

1. `stoke plan --sow X` → build DAG → run pre-flight → write `plan.json` → emit `plan.ready` → if `--interactive` call `Operator.Confirm` → on approve emit `plan.approved` to bus AND eventlog → exit 0.
2. `stoke execute --plan plan.json` → replay eventlog → assert `plan.approved` → dispatch sessions respecting DAG edges → each dispatch runs intent gate (Part C) → worker prompt injects memory learnings (Part D.5 inj 2) → descent applies Part B Ask/Notify on T8 → session complete fires meta-reasoner (Part E) → memory updated → next session dispatches with fresh rules.

## Error Handling

| Failure | Strategy | User Sees |
|---------|----------|-----------|
| `plan.json` sow_hash mismatch on execute | Refuse, exit 1 | "SOW changed since plan; re-run `stoke plan`" |
| Operator.Ask timeout | Return `operator.ErrTimeout`; T8 gate → fail | Notify("operator timeout — aborting session") |
| Intent gate AMBIGUOUS | Proceed as DIAGNOSE (safer default) | Notify("intent unclear — running read-only") |
| Embedder backend changes mid-run | Recompute backend on next Consolidate | log line on backend swap |
| sqlite-vec extension missing | Silent BM25-only fallback | info log at startup |
| Consolidation LLM invalid JSON | Skip chunk, retry once, then discard chunk | log warn |
| Meta-reasoner over budget | Skip, preserve session-completed event | Notify("meta-reasoner skipped: over budget") |
| `progress.md` write fails | Log, continue (non-fatal) | (nothing — silent) |
| Memory `Put` fails on embedding | Write row without embedding | log warn, BM25 still indexes |
| Cost dashboard tick panics | Recover, log, freeze widget at last value | "(cost panel stalled)" |

## Boundaries — What NOT To Do

- Do NOT modify `internal/memory/tiers.go` or `internal/memory/contradiction.go` — extend around them.
- Do NOT create `internal/events/` — extend `internal/streamjson/` (C1).
- Do NOT create `internal/metareason/` — extend `internal/plan/meta_reasoner.go` (§E.1).
- Do NOT make embeddings mandatory — BM25-only path must remain functional (air-gapped deploys).
- Do NOT commit `.stoke/memory/` or `plan.json` to git — add to default `.gitignore` if user config has one.
- Do NOT change `AcceptanceCriterion.VerifyFunc` — spec-3 owns that field (D13).
- Do NOT define the streamjson event taxonomy — spec-2 owns it; NDJSON Operator is a consumer only.
- Do NOT implement HITL stdin reader — spec-2's `internal/hitl/` is consumed.
- Do NOT score agent reliability in this spec — spec-5 does; Part D.5 inj 3 just provides the memory schema for it.
- Do NOT gate `stoke plan` on the descent flag — planning runs regardless of `STOKE_DESCENT`.
- Do NOT break `stoke ship` — existing end-to-end path unchanged.

---

# Testing

## Part A — `stoke plan`

- [ ] Happy: `stoke plan --sow /tmp/good.md --output /tmp/p.json` → exit 0, file exists, valid JSON with `sessions` + `approval: null`
- [ ] `--yes` populates `approval` block with actor + ts
- [ ] SOW hash changes between plan and execute → execute refuses with exit 1
- [ ] `stoke execute --plan` without prior `plan.approved` event → exits with error

## Part B — Operator

- [ ] Terminal Notify goes to stderr with timestamp prefix, does not halt
- [ ] Terminal Confirm returns true on "y"/"yes", false on "n"/"no"/empty
- [ ] NDJSON Ask emits `hitl_required`, blocks until stdin reply, returns `decision` label
- [ ] Ask timeout returns `operator.ErrTimeout`
- [ ] Fake operator maps prompt → canned reply

## Part C — Intent Gate

- [ ] Action-verb "implement X" → IMPLEMENT (deterministic, no LLM call)
- [ ] "explain X" → DIAGNOSE (deterministic)
- [ ] Mixed prose → Haiku called once, result cached per task.ID
- [ ] DIAGNOSE mode: `edit` tool call returns authorization error at harness layer
- [ ] DIAGNOSE output writes to `reports/<task>.md`, not source tree
- [ ] Bus `task.sow.updated` triggers reclassify + requeue

## Part D — Memory

- [ ] Happy: `Put(item)` persists, `Get(id)` returns same content + metadata
- [ ] FTS5: `Query{Text: "react hooks"}` returns matching rows ranked by BM25
- [ ] sqlite-vec present: `Query{Embedding: v}` returns cosine-ranked rows
- [ ] sqlite-vec absent: same call falls back silently to BM25
- [ ] Scope Auto: task scope beats repo beats global on conflict
- [ ] Embedder fallback: no `OPENAI_API_KEY`, no local daemon → `NoopEmbedder`, startup logs "BM25-only"
- [ ] Embedder fallback: `OPENAI_API_KEY` set → `OpenAIEmbedder` chosen, 1536→512 truncation
- [ ] Consolidate: 120 episodic memories → 3 chunks → ≤120 semantic outputs → all episodes marked consolidated
- [ ] Consolidate dedup: two embeddings with cosine 0.95 → newer archived
- [ ] Retention: 91-day-old importance=3 memory → archived
- [ ] Auto-retrieval inj 1: plan prompt contains `## Prior Learnings` with ≤8+15 bullets
- [ ] Auto-retrieval inj 2: worker system prompt capped at 1200 tokens
- [ ] Auto-retrieval inj 4: known-false-positive line appears in reviewer prompt only when matches exist

## Part E — Live meta-reasoner

- [ ] `STOKE_META_LIVE=0`: no-op
- [ ] 0 failures in session: no-op (log line only)
- [ ] OverBudget: skipped
- [ ] Happy: S1 completes with 2 failures → rules written to memory → S2 worker prompt contains them

## Part F — progress.md

- [ ] File created on first `session.started`
- [ ] Icons render per state (`[x]`, `[~]`, `[ ]`, `[!]`, `[?]`)
- [ ] `tail -f` shows live updates

## Part G — Cost dashboard

- [ ] Panel renders $spent / $budget
- [ ] Burn rate updates each tick
- [ ] ETA = `—` when burn rate == 0

---

# Acceptance Criteria

- WHEN `stoke plan --sow X.md` is run THE SYSTEM SHALL produce `plan.json` with a valid DAG and non-null `cost_estimate`.
- WHEN the operator types `y` at the plan approval prompt THE SYSTEM SHALL emit `plan.approved` to both bus and eventlog.
- WHEN intent classifies as DIAGNOSE THE SYSTEM SHALL reject any `edit`/`write`/`git_commit` tool call at the harness auth layer.
- WHEN memory backend starts without sqlite-vec THE SYSTEM SHALL serve BM25 queries without error and log the backend choice once.
- WHEN `Consolidate` runs with 0 unconsolidated episodes THE SYSTEM SHALL return nil without LLM calls.
- WHEN `STOKE_META_LIVE=1` and a session has zero failures THE SYSTEM SHALL skip the LLM call.
- WHEN a task-scoped memory contradicts a repo-scoped memory for the same query THE SYSTEM SHALL return the task-scoped one first.
- WHEN the cost dashboard burn rate is zero THE SYSTEM SHALL display ETA as `—`.

### Bash AC commands

```bash
./stoke plan --help | grep -q 'sow'
./stoke plan --sow /tmp/test.md --output /tmp/plan.json && jq -e '.sessions // .dag.nodes' /tmp/plan.json
./stoke execute --plan /tmp/plan.json --help | grep -q 'plan'
./stoke memory --help | grep -q 'list'
./stoke memory list --scope repo | head -5
./stoke memory search "react" --limit 3
./stoke memory info | grep -Eq 'backend|embed'
go test ./internal/operator/... -run TestAskNotify
go test ./internal/router/... -run TestIntentGate
go test ./internal/memory/... -run TestSQLiteFTS5
go test ./internal/memory/... -run TestEmbedderFallback
go test ./internal/memory/... -run TestConsolidate
go test ./internal/memory/... -run TestAutoRetrieve
go test ./internal/memory/... -run TestScopeHierarchy
go test ./internal/plan/... -run TestLiveMetaReasoner
go test ./internal/plan/... -run TestProgressRenderer
go test ./internal/tui/... -run TestCostPanel
go build ./cmd/stoke
go vet ./...
```

---

# Implementation Checklist

Ordered; each self-contained.

### Part A — `stoke plan`

1. [ ] Create `cmd/stoke/plan_cmd.go`. Flags: `--sow`, `--output` (default `plan.json`), `--yes`, `--estimate-only`, `--interactive`. Reuse existing plan builder + `PreflightACCommands`. Emit `plan.ready` on success. Library: stdlib `encoding/json` (2-space indent).
2. [ ] Define `PlanArtifact` struct mirroring §A.2 JSON. Include `ApprovalBlock` (pointer → null when unapproved). Hash SOW with `crypto/sha256`. Compute `plan_id` = `pln_` + first 8 hex of `SHA256(sow_hash + created_at)`.
3. [ ] Add `stoke execute --plan` subcommand (extend existing ship/execute entry). Before dispatch: load plan.json, recompute sow_hash, assert match, query eventlog for `plan.approved` with matching `plan_id`, exit 1 on mismatch.
4. [ ] On `--yes`, synthesize approval block: `{actor: "ci:$(whoami)", ts: now, mode: "auto"}`, publish `plan.approved` to bus and eventlog before writing plan.json.

### Part B — Operator

5. [ ] Create `internal/operator/operator.go` with interface + types per §B.1.
6. [ ] Create `internal/operator/terminal.go`. Detect TTY via `term.IsTerminal(int(os.Stdout.Fd()))`. TTY path uses existing huh widgets; non-TTY path prints + `bufio.Scanner` reads.
7. [ ] Create `internal/operator/ndjson.go`. Depends on spec-2's `hitl.Reader` + `streamjson.Emitter`. Each `Ask` call generates `ask_id = ulid.Make().String()`, registers a callback in `hitl.Reader`, blocks on the reply channel with timeout.
8. [ ] Create `internal/operator/fake.go` for tests.
9. [ ] Add `Operator operator.Operator` field to `app.Orchestrator`. CLI wires per §B.1 ("Injection").
10. [ ] Patch `internal/plan/verification_descent.go` T8 with policy-aware Gate 7 per §B.4. Add `SoftPassPolicy SoftPassPolicy` + `Operator operator.Operator` fields to `DescentConfig`. Default `SoftPassAuto` preserves current behavior.

### Part C — Intent Gate

11. [ ] Create `internal/router/intent_gate.go`. Export `ClassifyIntent(task, excerpt) (Intent, error)`. Stage 1 verb scan (per §C.2 verb sets). Stage 2 Haiku call via `model.Resolve("intent-gate")` → fallback to Sonnet if Haiku unavailable. Cache by `task.ID`.
12. [ ] Extend `internal/harness/tools/` authorization. Add `ToolMask{Mode Intent}`. Block list per §C.3. Readonly bash allowlist hard-coded in new file `internal/harness/tools/readonly.go`.
13. [ ] Hook `router.ClassifyIntent` into `scheduler.Dispatch` before `harness.Spawn`. Pass Intent into harness as `task.Intent`. DIAGNOSE dispatch writes output to `reports/<task-id>.md`, ledger node kind `diagnostic_report`.
14. [ ] Subscribe bus `task.sow.updated` → re-classify + `scheduler.Requeue(task.ID)`. Emit `task.intent.changed` when intent flips.

### Part D — Memory

15. [ ] Create `internal/memory/sqlite.go`. Apply DDL per §D.1 at open. Implement `Storage` interface. WAL journal, PRAGMAs. Handle `sqlite-vec` load failure gracefully.
16. [ ] Create `internal/memory/scope.go`. Define `Scope` enum (Global|Repo|Task|Auto). Compute `repo_hash` = `SHA256(git rev-parse --show-toplevel)[0:16]`. `task_type` sourced from router classification.
17. [ ] Create `internal/memory/embed.go`. Implement `Embedder` interface + `OpenAIEmbedder`, `LocalEmbedder`, `NoopEmbedder`. `AutoDetect` per §D.3 decision tree. 30s HTTP timeout, 1s ping timeout.
18. [ ] Create `internal/memory/consolidate.go`. Implement `Consolidate(ctx, scope, repoHash, taskType)` per §D.4 pseudocode. Model resolved via `model.Resolve("memory-consolidate")` (Sonnet 4.6 default). Apply hygiene pass at end.
19. [ ] Create `internal/memory/retrieve.go`. Implement `CoreAndQuery(...)` per §D.5. Hybrid BM25+vector fusion via RRF: `score = w_bm25 * rank_bm25 + w_vec * rank_vec`, default `w_bm25=0.4, w_vec=0.6` when embedder available; else `w_vec=0`. Cap by `coreLimit`, `queryLimit`, and 1200 tokens (count via `tokenest`).
20. [ ] Wire injection point 1 in `cmd/stoke/plan_cmd.go` into `PlanBriefing.RelevantLearnings`.
21. [ ] Wire injection point 2 in `cmd/stoke/sow_native.go` between canonical-names (line 3909) and skills (line 3918). Emit as `## Relevant learnings` block.
22. [ ] Add injection point 3 export `ScoreAgent(ctx, role) float64` used by spec-5. Scope = Global, query = `"agent:" + role`.
23. [ ] Wire injection point 4 in `internal/verify/` before running AC commands. Append `## Known false positives near this change` bullets (≤3) to reviewer prompt.
24. [ ] Create adapters: `internal/wisdom/adapter.go` (WisdomAdapter → semantic), `internal/skill/adapter.go` (SkillAdapter → procedural, read-only). Implement `memory.Storage`.
25. [ ] Create `internal/memory/router.go` with `Register/Query/Put` fan-out. Content-hash de-dup in Query merge step.
26. [ ] Wire router construction in `app/` startup per §D.6.
27. [ ] Create `cmd/stoke/memory_cmd.go` per §D.7. Subcommands: `list, search, consolidate, export, import, info`.
28. [ ] Add `.stoke/memory/` and `plan.json` to default ignore recommendations (README note only — do NOT auto-edit user's `.gitignore`).

### Part E — Live meta-reasoner

29. [ ] Extend `internal/plan/meta_reasoner.go`. Export `RunLiveMetaReasoner(ctx, sessionID) ([]Rule, error)`. Build prompt from bus events + verify results + failure fingerprints for that session.
30. [ ] Subscribe to bus `session.completed` in `app/` startup (gated by `STOKE_META_LIVE=1`). Apply cheap-trick skip (0 failures) and budget check (`costtrack.Global.OverBudget()`) before running.
31. [ ] Write output rules as `memory.Item{Tier: semantic, Scope: repo, Category: meta_rule, Importance: 6, Confidence: 0.6}`. Tag `meta-rule` + `auto` for auditability.
32. [ ] Cap absolute meta-rule count per repo to 50 (FIFO by `last_used`). Cap prompt injection to 400 tokens per worker.

### Part F — progress.md

33. [ ] Create `internal/plan/progress_renderer.go`. Implement `RenderToFile(path, plan, state) error`. Icons per §F.2 table.
34. [ ] Subscribe to SessionScheduler.OnProgress (or add hook if absent) at: session.started, session.completed, task.started, task.completed, ac.checked, descent.tier, operator.ask, session.failed.
35. [ ] Output path: `.stoke/runs/<run-id>/progress.md`. Atomic write via `atomicfs/` (write temp + rename). Never panic on render failure — log + continue.

### Part G — Cost dashboard

36. [ ] Add `costtrack.Global.Snapshot()` returning `{Total, Budget, ByCategory, BySession, WindowRate}` if not already present.
37. [ ] Create `internal/tui/cost_panel.go`. Bubbletea model, 1s tick refresh. ASCII art per §G.1 mock. Bar-fill helper `renderBar(pct, width)`.
38. [ ] Add panel to existing Dashboard view as a second row. `Tab` cycles focus.
39. [ ] Handle panic-recover in tick handler; freeze display at last snapshot with footer `(cost panel stalled)`.

### Cross-cutting

40. [ ] Integration test: full `stoke plan` → `--yes` approve → `stoke execute` → session completes → meta-reasoner fires → memory grows → subsequent plan.build picks up new learnings in `## Prior Learnings` block. Assert: 1 new `meta_rule` row in memories for the repo scope.
41. [ ] `go build ./cmd/stoke && go test ./... && go vet ./...` all green before marking spec done.
