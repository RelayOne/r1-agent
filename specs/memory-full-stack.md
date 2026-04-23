<!-- STATUS: done -->
<!-- BUILD_COMPLETED: 2026-04-22 -->
<!-- CREATED: 2026-04-21 -->
<!-- DEPENDS_ON: provider-pool (for embedder selection), operator-ux-memory Part D (legacy reference) -->
<!-- BUILD_ORDER: 20 -->

# Memory — Full Stack (post-MVP)

## 1. Overview

Stoke shipped an MVP of S-9 persistent memory by extending `internal/wisdom/sqlite.go` with a `stoke_memories` table, a best-effort FTS5 virtual table, and the three insert/delete/update triggers required to keep the FTS index in sync. That covers roughly 20% of the full intent in `specs/operator-ux-memory.md` Part D and about the same share of the RT-08 recommendations. It handles one flat scope, a single memory-type column, no embeddings, no auto-retrieval, no consolidation, and no operator CLI. It is safe to land as a foundation but cannot yet carry prompt-time retrieval, cross-session learning, or the hygiene passes that make memory *useful* rather than *present*.

This spec lands the remainder of RT-08 in one coherent push. The full stack adds: (a) a **scope hierarchy** (global / repo / task) with typed queries and a task-beats-repo-beats-global conflict rule; (b) a **sqlite-vec** loadable extension for vector search combined with BM25 via Reciprocal Rank Fusion; (c) a **three-way embedder fallback** (remote API → local llama.cpp daemon → BM25-only no-op) so Stoke continues to run air-gapped as a single Go binary; (d) a **consolidation pipeline** that classifies episodic memories in 50-episode chunks via Sonnet-class LLM calls into NEW / REINFORCE / CONTRADICT / AMBIGUOUS outcomes; (e) **auto-retrieval injection** at four well-defined hook points (planner, worker dispatch, delegation scheduler, verification reminder); (f) **hygiene passes** (confidence decay, embedding/Jaccard dedupe, retention tiers) on a cron-style schedule; and (g) a **`stoke memory` CLI** with six verbs so operators can inspect, author, search, consolidate, and garbage-collect memory without writing SQL.

## 2. Backends — three-way embedder fallback (RT-08 §4 verbatim)

The fallback order is evaluated once at `AutoDetect` time and rechecked on every `memory.Consolidate` run:

1. **Remote API.** If `STOKE_EMBED_API ∈ {openai, voyage}` is set AND the corresponding API key env (`OPENAI_API_KEY` / `VOYAGE_API_KEY`) is present, use the remote embedder. Default model on the OpenAI path is `text-embedding-3-small` at 1536d, truncated to 512d via Matryoshka slice (`vec[:512]`) to keep SQLite row sizes sane. On the Voyage path use `voyage-3-lite` at 512d natively.
2. **Local llama.cpp daemon.** If the `stoke embed daemon` (or any llama.cpp HTTP server) is reachable at `STOKE_EMBED_LOCAL_URL` (default `http://127.0.0.1:11434`), HTTP-POST `/embed` with the `nomic-embed-text-v2` model. Truncate wider dims to 512.
3. **BM25-only no-op.** Otherwise instantiate `NoopEmbedder{}`: `Dim()==0`, `Embed()` returns `(nil, nil)` without error. Hybrid queries collapse `alpha=0.0`, and the retriever falls back to FTS5 BM25 + tag-overlap Jaccard.

**Embeddings are never mandatory.** Stoke ships as a CGO-free Go binary and must run air-gapped in CI sandboxes, restricted networks, and offline laptops. The noop path is a first-class execution mode, not an error state. Startup logs the chosen backend exactly once; log line format: `[memory] embeddings: <backend> <model> -> <dim>d` or `[memory] embeddings: disabled (BM25-only)`.

## 3. Schema upgrade (extends the MVP table)

The MVP `stoke_memories` table stays; the new columns are added via `ALTER TABLE` in an idempotent migration that is safe to run on a DB already populated by the MVP path. The migration detects existing columns via `PRAGMA table_info(stoke_memories)` and skips any `ALTER` whose target column is already present, so reopening a DB that already shipped with MVP rows is a no-op.

```sql
ALTER TABLE stoke_memories ADD COLUMN scope TEXT;           -- "global" | "repo" | "task"
ALTER TABLE stoke_memories ADD COLUMN scope_id TEXT;        -- repo path / task ID / empty for global
ALTER TABLE stoke_memories ADD COLUMN tier TEXT;            -- episodic | semantic | procedural
ALTER TABLE stoke_memories ADD COLUMN importance REAL DEFAULT 5;
ALTER TABLE stoke_memories ADD COLUMN confidence REAL DEFAULT 0.7;
ALTER TABLE stoke_memories ADD COLUMN last_used TEXT;
ALTER TABLE stoke_memories ADD COLUMN use_count INTEGER DEFAULT 0;
ALTER TABLE stoke_memories ADD COLUMN status TEXT DEFAULT 'active';  -- active | superseded | merged | archived
ALTER TABLE stoke_memories ADD COLUMN evidence_ids TEXT;    -- JSON array of source IDs
ALTER TABLE stoke_memories ADD COLUMN embedding BLOB;

CREATE INDEX IF NOT EXISTS idx_mem_scope  ON stoke_memories(scope, scope_id);
CREATE INDEX IF NOT EXISTS idx_mem_tier   ON stoke_memories(tier);
CREATE INDEX IF NOT EXISTS idx_mem_status ON stoke_memories(status);
-- Optional virtual table, only when sqlite-vec loads. Silent fallback otherwise.
-- CREATE VIRTUAL TABLE IF NOT EXISTS stoke_memories_vec USING vec0(
--     id INTEGER PRIMARY KEY, embedding FLOAT[512]
-- );
```

**Backfill defaults** for rows that predate this migration: `scope = 'repo'` when `repo IS NOT NULL`, else `scope = 'global'`; `scope_id = repo`; `tier` is inherited from the existing `type` column (`episodic`/`semantic`/`procedural` map 1:1); `importance = 5`, `confidence = 0.7`, `last_used = created_at`, `use_count = 0`, `status = 'active'`, `evidence_ids = '[]'`, `embedding = NULL`. All MVP FTS5 triggers (`stoke_memories_ai/ad/au`) stay in place unchanged; the ALTERs do not affect them because FTS triggers reference only `content` and `key`. The migration is wrapped in a single transaction and bumps `PRAGMA user_version` from `1` (MVP) to `2` (full stack) so subsequent opens detect the state in O(1).

## 4. Package layout

Memory moves out of `internal/wisdom/` into a dedicated package. `wisdom` keeps a thin adapter (see Part D.6 of the legacy spec) that re-exports the MVP functions against the new backend so existing callers don't break.

| File (new) | Responsibility |
|---|---|
| `internal/memory/sqlite.go` | Storage layer: open DB, run migration, FTS5 + optional sqlite-vec wiring, Put/Get/Query/Delete/Vote/Consolidate |
| `internal/memory/embed.go` | `Embedder` interface, `AutoDetect`, ping logic |
| `internal/memory/embed_openai.go` | Remote OpenAI/Voyage embedder (HTTP client, 30s timeout, 1s ping) |
| `internal/memory/embed_local.go` | Local llama.cpp embedder (HTTP to `/embed`) |
| `internal/memory/scope.go` | `Scope` enum (Global/Repo/Task/Auto), `RepoHash()`, scope resolver + SQL predicate builder |
| `internal/memory/retrieve.go` | `CoreAndQuery(...)`, 4 hook points, RRF fusion, 1200-token cap |
| `internal/memory/consolidate.go` | Chunked episodic → semantic classification pipeline, 4 outcomes |
| `internal/memory/hygiene.go` | `ApplyDecay`, `Dedupe`, `ApplyRetentionTiers` |
| `internal/memory/metareason.go` | Live inter-session meta-reasoner, gated by `STOKE_META_LIVE=1` |
| `cmd/stoke/memory_cmd.go` | `stoke memory {list,show,put,search,consolidate,gc}` |

`internal/wisdom/sqlite.go` keeps the existing `wisdom_learnings` table untouched. The `SQLiteStore.StoreMemory / SearchMemories / ListMemories / DeleteMemory` functions become thin wrappers over `internal/memory` so call sites compile as-is during the migration window.

## 5. Auto-retrieval — 4 injection points (RT-08 §7 verbatim)

| Hook | Tier | Top-K | Format |
|---|---|---|---|
| Before session planning | Semantic + Episodic | 8 core + 10 query | H2 block `## Prior Learnings`, bullets `[category] content (tags)` |
| Before worker dispatch | Semantic + Procedural | 5 core + 8 query | Under `## Relevant learnings` in system prompt, hard cap 1200 tokens |
| Before delegation | Semantic `tag=agent:*` | top-3 by confidence | `float64` reliability score returned to scheduler (not prompt) |
| Before verification | `tag=false-positive` + `tag=verifier-error` | 3 only | One-liner `## Known false positives near this change` in reviewer prompt |

Core/query split follows CloudSwarm: **core** = importance-gated always-on slice (`importance >= 7`, recency-sorted); **query** = embedding similarity against task text (falls through to BM25 when `Dim()==0`). Scope resolution preference `task > repo > global` is applied **before** rank; repo memories always win ties against global, and task memories win ties against repo.

## 6. Consolidation pipeline (RT-08 §2 reference port)

**Trigger points:**
1. **End of SOW** — always; existing meta-reasoner hook.
2. **Between sessions** — gated by `STOKE_META_LIVE=1`; runs mini-pass on just the previous session's episodes (see `metareason.go`).
3. **Size threshold** — `unconsolidated_episodic > 500` rows OR `memory.db > 50 MB`.
4. **Nightly hygiene** — cron-style; runs `ApplyDecay` + `Dedupe` + `ApplyRetentionTiers` without the LLM step.

**Algorithm per run:**
1. Fetch up to 500 unconsolidated episodic rows (ORDER BY `created_at ASC`).
2. Fetch top-100 semantic rows (ORDER BY `importance DESC, last_used DESC`) for LLM context.
3. Chunk episodes into groups of 50.
4. For each chunk, call Sonnet-class model with a structured prompt. Output is a strict JSON array. Each block: `{content, category, confidence 0..1, importance 0..10, status, evidence_ids, update_target_id?}`.
5. Classify each block into exactly one of four **outcomes**:
   - **NEW** — insert semantic row, `evidence_ids` = source task IDs.
   - **REINFORCE** — `confidence += 0.1` (clamp 1.0), append source task ID to `evidence_ids`, update `last_used = now`.
   - **CONTRADICT** — set old row `status='superseded'`, create new row with metadata `supersedes: <old-id>`, then run `contradiction.DetectContradictions` to surface the disagreement kind for audit trail.
   - **AMBIGUOUS** — store with low confidence (≤0.4) and `status='active'` so it surfaces for human review but does not dominate retrieval.
6. Mark all source episodes `consolidated_at = now`.
7. Prefer **merge over deletion**. Always carry `evidence_ids` forward. Never drop audit pointers.

## 7. `stoke memory` CLI — 6 verbs

```
stoke memory list       [--scope global|repo|task] [--tier episodic|semantic|procedural] [--limit N]
stoke memory show       <id>
stoke memory put        --scope X --tier Y --content "..." [--tags foo,bar] [--importance N]
stoke memory search     "query" [--top-k 10] [--tier X] [--scope X]
stoke memory consolidate [--dry-run] [--chunk 50]
stoke memory gc         [--decay] [--dedupe] [--retention]
```

Output: tabular (`id, scope, tier, category, importance, confidence, content-excerpt`) by default; `--json` for machine consumption. `consolidate --dry-run` prints the classification counts (NEW / REINFORCE / CONTRADICT / AMBIGUOUS / SKIP) without mutating rows. `gc` with no flags runs all three hygiene passes in order.

## 8. Implementation checklist

Ordered, self-contained. Each item names the file, the function, the pattern file to read first, and the unit test to add.

### 8.1 Schema migration
1. [ ] `internal/memory/sqlite.go` — add `const schemaV2` with the 10 `ALTER TABLE` + 3 `CREATE INDEX` statements from §3. Pattern: `internal/wisdom/sqlite.go:43-77` (memory schema). Test: `TestMigrationIdempotent` reopens a seeded MVP DB, runs migration, asserts no row loss + `user_version=2`.
2. [ ] `internal/memory/sqlite.go:runMigration()` — read `PRAGMA table_info`, skip `ALTER` for existing columns, wrap in `BEGIN/COMMIT`, bump `PRAGMA user_version`. Pattern: `internal/session/sqlite.go` migration block. Test: `TestMigrationPartial` seeds a DB with half the columns, verifies idempotent apply.
3. [ ] `internal/memory/sqlite.go:backfillDefaults()` — run once post-migration: `UPDATE stoke_memories SET scope='repo', scope_id=repo WHERE scope IS NULL AND repo IS NOT NULL`; analogous for `tier` from `type`. Test: `TestBackfillDefaults`.
4. [ ] `internal/memory/sqlite.go:Open(path) (*Store, error)` — opens with WAL, runs migration, attempts `SELECT load_extension('vec0')`, sets `hasVec` bool. Pattern: `internal/wisdom/sqlite.go:NewSQLiteStore`. Test: `TestOpenWithoutVec` (extension absent), `TestOpenWithVec` (extension present, `build tag sqlite_vec`).
5. [ ] `internal/memory/sqlite.go:ensureVecTable()` — `CREATE VIRTUAL TABLE IF NOT EXISTS stoke_memories_vec USING vec0(id INTEGER PK, embedding FLOAT[512])` only when `hasVec`. Test: `TestEnsureVecTable`.

### 8.2 Embedder fallback
6. [ ] `internal/memory/embed.go:Embedder` interface: `Embed(ctx, text) ([]float32, error)`, `Dim() int`, `Backend() string`. Pattern: `internal/provider/pool.go` provider interface shape. Test: N/A (type def).
7. [ ] `internal/memory/embed.go:AutoDetect(cfg Config) Embedder` — tries OpenAI → local → noop per §2. 1s ping timeout per backend. Logs choice once via `logging.Infof`. Test: `TestAutoDetect` with env toggles + fake HTTP servers.
8. [ ] `internal/memory/embed_openai.go:OpenAIEmbedder` — POST `https://api.openai.com/v1/embeddings`, body `{model, input}`, 30s timeout, Matryoshka truncate. Test: `TestOpenAIEmbed` against httptest.Server.
9. [ ] `internal/memory/embed_openai.go:VoyageEmbedder` — POST `https://api.voyageai.com/v1/embeddings`, 512d native. Test: `TestVoyageEmbed`.
10. [ ] `internal/memory/embed_local.go:LocalEmbedder` — POST `{url}/embed`, parse `{embedding: []float32}`. Test: `TestLocalEmbed`.
11. [ ] `internal/memory/embed.go:NoopEmbedder` — `Embed` returns `(nil, nil)`, `Dim()==0`. Test: `TestNoopEmbed`.
12. [ ] `internal/memory/sqlite.go:vecToBlob([]float32) []byte` — little-endian float32 serializer (sqlite-vec wire format). Test: `TestVecToBlob` roundtrip.
13. [ ] `internal/memory/sqlite.go:Put` embed hook — when `embedder.Dim() > 0`, call `embedder.Embed(ctx, item.Content)`, upsert into `stoke_memories_vec`. Failure is logged and ignored (BM25 still indexes). Test: `TestPutWithEmbed`, `TestPutEmbedFailureTolerant`.

### 8.3 Scope hierarchy
14. [ ] `internal/memory/scope.go:Scope` enum: `ScopeGlobal`, `ScopeRepo`, `ScopeTask`, `ScopeAuto`. Test: N/A.
15. [ ] `internal/memory/scope.go:RepoHash() string` — `SHA256(git rev-parse --show-toplevel)[0:16]`. Falls back to `cwd` when not a git repo. Pattern: `internal/worktree/` git shell usage. Test: `TestRepoHashStable`.
16. [ ] `internal/memory/scope.go:PredicateFor(scope, repoHash, taskType) (where string, args []any)` — returns SQL `WHERE` fragment + args per §5 UNION rules. Test: `TestScopePredicateAutoUnion`.
17. [ ] `internal/memory/scope.go:Specificity(row)` — returns 3/2/1 for task/repo/global. Used in ORDER BY tie-break. Test: `TestSpecificityOrder`.
18. [ ] `internal/memory/sqlite.go:Query(ctx, q Query)` — build SQL from `Query.Scope` via `PredicateFor`, apply tier filter, min-importance filter, include/exclude categories. Test: `TestQueryScopeFilter`.
19. [ ] `internal/memory/sqlite.go:Query` conflict rule — when `contradiction.DetectContradictions` flags two rows in result, keep the more-specific scope. Test: `TestTaskBeatsRepoOnContradiction`.

### 8.4 Auto-retrieval (4 hooks × injection-site wiring)
20. [ ] `internal/memory/retrieve.go:Router` type holding `*Store` + `Embedder`. Test: N/A.
21. [ ] `internal/memory/retrieve.go:CoreAndQuery(ctx, scope, query, coreLimit, queryLimit) (core, qs []Item, err error)` — runs two queries: core by importance+recency, qs by embedding-or-BM25 similarity. Pattern: CloudSwarm `retrieve_agent_memories`. Test: `TestCoreAndQueryShape`.
22. [ ] `internal/memory/retrieve.go:fuseRRF(bm25, vec []Item, alpha float64) []Item` — Reciprocal Rank Fusion: `score = alpha/(k+rank_vec) + (1-alpha)/(k+rank_bm25)`, default `k=60`, `alpha=0.6` when embedder present else `0.0`. Test: `TestRRFFusion`.
23. [ ] `internal/memory/retrieve.go:formatForPrompt(items) string` — renders `[category] content (tag1, tag2)\n` bullets. Test: `TestFormatForPrompt`.
24. [ ] `internal/memory/retrieve.go:CapTokens(text string, max int) string` — uses `internal/tokenest/` to truncate lowest-confidence items first. Test: `TestCapTokens1200`.
25. [ ] **Hook 1 (planner)** — wire into `cmd/stoke/plan_cmd.go` (or equivalent plan builder). Call `CoreAndQuery(ScopeAuto, sow.Title+" "+sow.Description, 8, 10)`, inject under `## Prior Learnings` before `## Task Definition`. Test: `TestPlannerInjection` (plan output contains H2 block when memories exist).
26. [ ] **Hook 2 (worker dispatch)** — wire into `cmd/stoke/sow_native.go` between canonical-names and skills (see legacy spec lines 3909-3918). Call `CoreAndQuery(ScopeAuto, sessionTitle+" "+fileScope, 5, 8)`. Append under `## Relevant learnings` in system prompt. Cap 1200 tokens. Test: `TestWorkerInjectionCap`.
27. [ ] **Hook 3 (delegation)** — new function `ScoreAgent(ctx, role string) float64` in `retrieve.go`. Queries `ScopeGlobal, tag="agent:"+role, top-3`, returns `weightedAvg(confidence * importance/10)`. Called by spec-5 delegation code. Test: `TestScoreAgent`.
28. [ ] **Hook 4 (verification)** — wire into `internal/verify/` reviewer prompt builder. Query `ScopeRepo, tags={false-positive, verifier-error}, top-3`. Append `## Known false positives near this change` one-liners only when matches exist. Test: `TestVerifierInjectionNoMatch` (absent block), `TestVerifierInjectionPresent`.

### 8.5 Consolidation pipeline
29. [ ] `internal/memory/consolidate.go:Consolidate(ctx, scope, repoHash, taskType) (Report, error)` — top-level function per §6 algorithm. Test: `TestConsolidateNoop` (0 episodes → no LLM call).
30. [ ] `internal/memory/consolidate.go:fetchCandidates(ctx, scope, 500)` — SELECT with scope predicate + tier='episodic' + consolidated_at IS NULL. Test: `TestFetchCandidatesScope`.
31. [ ] `internal/memory/consolidate.go:fetchContext(ctx, scope, 100)` — top-100 semantic for LLM context. Test: `TestFetchContextOrder`.
32. [ ] `internal/memory/consolidate.go:chunkEpisodes(episodes, 50)` — split into 50-row chunks. Test: `TestChunk50`.
33. [ ] `internal/memory/consolidate.go:buildPrompt(chunk, context)` — returns the RT-08 §6 structured prompt string. Test: `TestBuildPromptStable` (golden file).
34. [ ] `internal/memory/consolidate.go:callLLM(ctx, prompt)` — resolves via `model.Resolve("memory-consolidate")` (Sonnet 4.6 default). Returns raw JSON string. Pattern: `internal/model/`. Test: `TestCallLLMWithStub`.
35. [ ] `internal/memory/consolidate.go:parseBlocks(raw string) ([]Block, error)` — strict JSON decode, validate schema. Test: `TestParseBlocks`, `TestParseBlocksMalformedSkipChunk`.
36. [ ] `internal/memory/consolidate.go:classify(block, context) Outcome` — deterministic NEW/REINFORCE/CONTRADICT/AMBIGUOUS dispatch from block fields + existing `contradiction.DetectContradictions`. Test: `TestClassifyAllFour`.
37. [ ] `internal/memory/consolidate.go:applyOutcome(tx, outcome, block)` — four branches: `New`, `Reinforce` (`confidence += 0.1 clamp 1.0`, append `evidence_ids`, `last_used=now`), `Contradict` (old→`status=superseded`, new with `supersedes` back-pointer), `Ambiguous` (insert with `confidence<=0.4`). Test: `TestApplyOutcomeEachBranch`.
38. [ ] `internal/memory/consolidate.go:markConsolidated(tx, ids)` — `UPDATE stoke_memories SET consolidated_at=? WHERE id IN (...)`. Test: `TestMarkConsolidated`.
39. [ ] `internal/memory/consolidate.go:backfillEmbeddings(ctx, newItems)` — for each new/updated item, compute embedding and upsert into vec table. Best-effort. Test: `TestBackfillEmbeddingsIgnoresError`.
40. [ ] `internal/memory/consolidate.go:Report` struct — counts per outcome + disk/row stats + LLM cost. Test: `TestReportShape`.

### 8.6 Hygiene passes
41. [ ] `internal/memory/hygiene.go:ApplyDecay(ctx)` — `UPDATE stoke_memories SET confidence = MAX(0.2, confidence * 0.95) WHERE status='active'`. Test: `TestApplyDecayClamps`.
42. [ ] `internal/memory/hygiene.go:Dedupe(ctx)` — when `hasVec`, pairwise cosine `<= 0.08` (i.e. similarity `>= 0.92`) within last 7d → archive newer as `status='merged'` and carry its `evidence_ids` into the survivor. Test: `TestDedupeCosine92`.
43. [ ] `internal/memory/hygiene.go:DedupeJaccard(ctx)` — noop-embedder fallback: Jaccard on tokenized `content` `>= 0.85` AND overlapping tags → same archive behavior. Test: `TestDedupeJaccard85`.
44. [ ] `internal/memory/hygiene.go:ApplyRetentionTiers(ctx)` — 0-7d keep all; 7-90d archive when `importance < 5`; >90d archive when `importance < 8`. Test: `TestRetentionTiers`.
45. [ ] `internal/memory/hygiene.go:RunAll(ctx)` — runs Decay + Dedupe + Retention in order. Returns counts. Test: `TestRunAllOrder`.

### 8.7 Live meta-reasoner
46. [ ] `internal/memory/metareason.go:RunLive(ctx, sessionID) ([]Rule, error)` — collect bus events + verify results + failure fingerprints for the session, build prompt per RT-08 §9 (leave the exact prompt text as an inline TODO with the quoted source), call Sonnet-class model, parse rules. Test: `TestRunLiveBasicPath`.
47. [ ] `internal/memory/metareason.go:Subscribe(bus)` — subscribes to `session.completed`, checks `STOKE_META_LIVE=1`, skips on 0 failures (cheap trick, 60% savings), skips on `costtrack.OverBudget()`, else runs `RunLive` and writes rules with `tier=semantic, scope=repo, category=meta_rule, importance=6, confidence=0.6`. Test: `TestSubscribeSkipsClean`.
48. [ ] `internal/memory/metareason.go:capMetaRules(repoHash, max=50)` — FIFO evict by `last_used` when over cap. Test: `TestCapMetaRules50`.

### 8.8 CLI verbs
49. [ ] `cmd/stoke/memory_cmd.go:cmdMemoryList(flags)` — SELECT with scope + tier filters, tabular output. Pattern: existing `cmd/stoke/` subcommands. Test: `TestCLIList`.
50. [ ] `cmd/stoke/memory_cmd.go:cmdMemoryShow(id)` — SELECT single row, pretty-print all columns including metadata JSON. Test: `TestCLIShow`.
51. [ ] `cmd/stoke/memory_cmd.go:cmdMemoryPut(flags)` — inserts with provided scope/tier/content/tags/importance. Validates enums. Test: `TestCLIPutValidation`.
52. [ ] `cmd/stoke/memory_cmd.go:cmdMemorySearch(query, flags)` — hybrid BM25+vec via `Router.CoreAndQuery`, prints top-K. Test: `TestCLISearch`.
53. [ ] `cmd/stoke/memory_cmd.go:cmdMemoryConsolidate(flags)` — calls `Consolidate`. `--dry-run` prints counts only. `--chunk N` overrides 50. Test: `TestCLIConsolidateDryRun`.
54. [ ] `cmd/stoke/memory_cmd.go:cmdMemoryGC(flags)` — calls hygiene passes. Flags `--decay`, `--dedupe`, `--retention` select subsets; no flag runs all. Test: `TestCLIGCAll`.
55. [ ] `cmd/stoke/main.go` — register `memory` command group and six verbs. Test: `TestMemoryCLIRegistered`.

### 8.9 Adapters + wiring
56. [ ] `internal/wisdom/adapter.go:WisdomAdapter` — implements `memory.Storage` mapping wisdom upvotes to `Vote`. Pattern: existing `Store.Vote`. Test: `TestWisdomAdapterPassthrough`.
57. [ ] `internal/wisdom/sqlite.go` — rewire `StoreMemory/SearchMemories/ListMemories/DeleteMemory` as thin delegators to `internal/memory`. Keep function signatures so callers compile unchanged. Test: `TestWisdomDelegation`.
58. [ ] `app/` startup — open `internal/memory.Store`, construct `Router`, inject into orchestrator. Pattern: existing `session.SQLStore` construction. Test: `TestAppBootsMemory`.
59. [ ] `cmd/stoke/main.go` — add `--memory-backend=sqlite|flat` flag (default `sqlite` when existing `.stoke/memory.db`, else `flat`). Test: `TestMemoryBackendFlag`.

### 8.10 Tests (cross-cutting)
60. [ ] `internal/memory/sqlite_integration_test.go` — seed 100 episodes, call `Consolidate`, assert exact counts of NEW/REINFORCE/CONTRADICT/AMBIGUOUS on a known corpus. Stub the LLM with a deterministic response file.
61. [ ] `internal/memory/race_test.go` — `go test -race` with 4 goroutines writing memories while a 5th runs `Consolidate`; assert no deadlocks, no lost writes, wisdom writes still succeed.
62. [ ] `internal/memory/mvp_compat_test.go` — open a DB populated only by MVP (single `type` column, no scope/tier), run migration, verify all rows readable via new `Query` API with `scope='repo'` defaulted.
63. [ ] `internal/memory/retrieval_1200_test.go` — inject 200 fake memories, call CoreAndQuery hook 2, assert resulting prompt block is `<=1200` tokens by `tokenest.Count`.
64. [ ] `internal/memory/e2e_test.go` — end-to-end: put, search, consolidate, gc, list. Verify counts and status transitions.

## 9. Acceptance criteria

- `go build ./cmd/stoke && go test ./... && go vet ./...` all green.
- `sqlite-vec` extension load is **optional**: when the extension is missing, all queries succeed via BM25+tag-overlap with no user-visible error.
- Migration from the MVP table is **idempotent**: seeding a DB with MVP-shaped rows, running migration twice, and querying returns the same rows with no data loss and `user_version=2`.
- Auto-retrieval at each of the 4 hooks injects at most 1200 tokens under the documented H2 block.
- `stoke memory consolidate --dry-run` against a seeded 100-episode corpus produces the expected NEW/REINFORCE/CONTRADICT/AMBIGUOUS counts without mutating any row (`consolidated_at` stays NULL).
- `stoke memory search "<query>"` returns ranked results in both sqlite-vec and noop-embedder modes; rankings differ but shape is identical.
- Startup logs the embedder backend exactly once per process.

## 10. Testing

- **Per-file unit tests** — one `_test.go` alongside every new file (items 1-48).
- **End-to-end consolidation integration test** — seed 100 episodes across scopes, stub the LLM with a deterministic JSON response, run `Consolidate`, verify: (a) all 100 source rows have `consolidated_at` set, (b) new semantic rows carry `evidence_ids`, (c) at least one CONTRADICT path flipped an old row to `status='superseded'`, (d) embedding backfill ran for all new rows when `hasVec`.
- **Concurrency / race test** — `go test -race ./internal/memory -run TestRace`. 4 writers on `stoke_memories` + 1 concurrent `Consolidate` + 1 concurrent wisdom write on `wisdom_learnings`. No deadlock, no lost rows, wisdom writes must not block memory writes (separate transactions, same DB file, WAL handles it).
- **MVP compatibility test** — `TestMVPCompat` loads a byte-for-byte golden MVP DB committed under `testdata/`, runs migration, asserts every MVP row is visible via the new `Query` API with defaulted scope/tier/importance/confidence.
- **Air-gapped smoke test** — `TestAirgapped` forces `AutoDetect` to noop by unsetting all embedder env vars and pointing `STOKE_EMBED_LOCAL_URL` at `127.0.0.1:1` (closed port). Asserts `NoopEmbedder` chosen, hybrid alpha forced to 0, BM25 queries succeed.

## 11. Rollout

The full stack lands behind `STOKE_MEMORY_V2=1` for the first two weeks. With the flag unset, Stoke continues to use the MVP code path (flat `stoke_memories` table, MVP triggers, LIKE fallback); the new migration runs but new columns stay NULL and new callers short-circuit on the flag check. During the rollout window:

- Default for `stoke run` and `stoke ship`: MVP behavior (flag unset).
- Default for `stoke memory` CLI: full stack (flag implicitly on — the CLI is new).
- CI runs the full rung suite with `STOKE_MEMORY_V2=1` to verify no regression in worker prompt quality (measured via existing honesty-gate metrics and rung pass-rate).
- After two clean weeks, the flag flips to default-on; removal of the MVP short-circuit lands in a follow-up commit.
- Escape hatch: `STOKE_MEMORY_V2=0` forces MVP even after default flip, so operators hitting a regression can roll back without a binary swap.

Compatibility promise: MVP-seeded databases upgrade in place. Rows written under the MVP remain queryable after the flag flip; the migration only adds columns and defaults, never drops.
