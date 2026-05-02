# RT-08 — Persistent Agent Memory: Taxonomy, Retrieval, Consolidation

**Scope:** Enhance `internal/memory/` (tiers + contradiction already shipped) with hierarchical storage, auto-retrieval, embeddings, consolidation, and a live meta-reasoner.

## 1. Cognitive origin of the taxonomy

Endel Tulving (1972) split long-term memory into **episodic** (time-stamped events) and **semantic** (decontextualised facts); his 1985 extension added **procedural** (skills run without conscious recall). The 2023 CoALA paper ("Cognitive Architectures for Language Agents") translated the trio into an agent skeleton — working memory for immediate context; episodic/semantic/procedural as long-term stores — and most 2025-2026 agent-memory systems inherit that mapping ([Medium — Semantic vs Episodic vs Procedural Memory in AI Agents](https://medium.com/womenintechnology/semantic-vs-episodic-vs-procedural-memory-in-ai-agents-and-why-you-need-all-three-8479cd1c7ba6); [arXiv — Memory for Autonomous LLM Agents](https://arxiv.org/html/2603.07670v1); [Agent Memory Paper List](https://github.com/Shichun-Liu/Agent-Memory-Paper-List)). Why it matters for Stoke: each tier has a different write/read/decay profile, and conflating them (as a single "memory" blob does) is the main reason agents re-learn gotchas every session.

Stoke's `tiers.go` already enumerates the CoALA four (Working/Episodic/Semantic/Procedural) and wires distinct scoring per tier — we are ahead of most OSS harnesses here. The gaps are: (a) no hierarchy across global/repo/task scope, (b) no auto-retrieval hook, (c) no embeddings, (d) no consolidation workflow, (e) no live meta-reasoner between sessions.

## 2. CloudSwarm's architecture (reference implementation)

Read from `/home/eric/repos/CloudSwarm/platform/temporal/activities/{store_memory,retrieve_memories,memory_consolidation}.py` and `workflows/memory_consolidation.py`.

**Write path (`store_memory`)** — `MemoryInput{agent_id, task_id, content, memory_type, metadata, account_id}` → bound content at 100 kB (V-115 DoS guard) → `generate_embedding()` via OpenRouter → `INSERT INTO episodic_memory (agent_id, task_id, content, embedding, metadata) ... ON CONFLICT (task_id) DO UPDATE`. Single table, `memory_type` is a metadata key ("episodic"|"semantic"), not a separate table. pgvector 1536-dim embeddings (`text-embedding-3-small`) with `ivfflat` index ([ADR-004](file:///home/eric/repos/CloudSwarm/docs/adr/004-pgvector-for-memory.md)).

**Read path (`retrieve_agent_memories`)** — input `{agent_id, query, core_limit=8, query_limit=15}`, returns `{core, query_specific}`:
- **core**: top-K semantic memories with `importance >= 7`, ORDER BY `created_at DESC` — always injected.
- **query_specific**: embed the query; `ORDER BY embedding <=> $query::vector LIMIT $query_limit` (cosine via `<=>`); falls back to recency if embedding unavailable.

**Consolidation workflow** (`MemoryConsolidationWorkflow`): fetch unconsolidated episodes (LIMIT 500) → fetch top-100 existing semantic for context → **chunk into 50-episode batches** → LLM call per chunk with a structured prompt that emits JSON blocks `{content, category, confidence 0-1, importance 0-10, status, evidence_ids, update_target_id?}` → `merge_memory_blocks` sorts into `new / updated / superseded` → transactional write → embedding backfill → mark episodes `consolidated=true`.

**Hygiene workflow**: `apply_confidence_decay` (multiply by 0.95 per run, clamp at 0.2), `cleanup_low_confidence` (status→archived below threshold), `deduplicate_memories` (`1 - (a.embedding <=> b.embedding) > 0.92` ⇒ archive newer), `apply_retention_tiers` (0-7d keep all; 7-90d keep if importance≥5; 90d+ archive).

The LLM consolidation prompt decides NEW / REINFORCE (+0.1 confidence) / CONTRADICT (supersede + new) / AMBIGUOUS (conditional) per episode — Stoke's `contradiction.go` already does deterministic negation-flip + factual-delta detection, which is a strict superset of what the LLM prompt guarantees.

## 3. Cursor / Windsurf

Windsurf's "Cascade Memories" auto-captures preferences/corrections during chat, stores them at `~/.codeium/windsurf/memories/` **scoped to workspace**, and retrieves them automatically when Cascade thinks they're relevant; memory creation doesn't consume credits ([Cascade Memories docs](https://docs.windsurf.com/windsurf/cascade/memories); [arsturn breakdown](https://www.arsturn.com/blog/understanding-windsurf-memories-system-persistent-context)). Cursor's Rules/Memories work similarly but with explicit user-defined rules + AI-proposed candidate memories that require approval. Neither publishes retrieval internals, but the observable pattern is: **workspace-scoped, injected automatically into context, not committed to git, free to create**. Stoke should mirror workspace-scoping (our `repo` tier) and auto-injection but go further with procedural skills and cross-session meta-reasoning, which neither ships.

## 4. Embedding model recommendation (2026)

Benchmarks ([PE Collective](https://pecollective.com/blog/best-embedding-models-2026/); [BentoML open-source guide](https://www.bentoml.com/blog/a-guide-to-open-source-embedding-models); [Milvus 2026 guide](https://milvus.io/blog/choose-embedding-model-rag-2026.md)):

| Model | MTEB | $/1M tok | Deploy |
|---|---|---|---|
| Voyage-3 | ~68 | $0.02 (lite) | API only |
| OpenAI text-embedding-3-small | ~62 | $0.02 | API |
| OpenAI text-embedding-3-large | 64.6 | $0.13 | API (Matryoshka — truncate to 256d) |
| BGE-M3 | ~64 | free | Local (≈2 GB) |
| Nomic-embed-text-v2 | ~62 | free | Local (MoE, llama.cpp-friendly) |

**Stoke recommendation — three-way fallback:**
1. If `STOKE_EMBED_API=openai|voyage` and key present → remote API (preferred: OpenAI 3-small @ 1536d, truncate to 512d via Matryoshka to keep SQLite row size sane).
2. Else if `stoke embed daemon` running (local llama.cpp with nomic-embed-text-v2) → HTTP localhost.
3. Else → **skip embeddings, fall back to BM25 + tag overlap** (already works for small per-repo corpora < 10k memories — [ML Journey sparse-vs-dense](https://mljourney.com/sparse-vs-dense-retrieval-for-rag-bm25-embeddings-and-hybrid-search/)).

Hybrid (BM25 + dense with RRF fusion) is the documented best practice for mixed query types, which Stoke has (error codes + natural language). Implement BM25 unconditionally; layer embeddings when available. Do **not** make embeddings mandatory — Stoke ships as a Go binary and must run air-gapped.

## 5. Storage backend for Go

Options on the table:

- **SQLite + `sqlite-vec`** — single file, no server, runs everywhere (incl. WASM), `sqlite-vec` benches 1 ms build / 17 ms query on SIFT1M vs DuckDB's 741 ms / 46 ms ([alexgarcia.xyz sqlite-vec v0.1](https://alexgarcia.xyz/blog/2024/sqlite-vec-stable-release/index.html)). FTS5 ships in-tree for BM25. Stoke already uses SQLite in `internal/session/sqlite.go` and `internal/wisdom/sqlite.go`. **Winner.**
- **DuckDB** — better for analytics, weak for vector search, larger binary, no reason to add a second embedded DB.
- **Badger** — KV only, no vectors, no FTS — we'd reinvent indexing.
- **Flat JSON + in-memory BM25** — simplest, ~what `memory.go` does today; tips over at ~5k entries and has no embedding story.

**Recommended**: keep `Store` flat-JSON path as the zero-config default, add a `SQLStore` in `internal/memory/sqlite.go` (mirroring `wisdom/sqlite.go`) with schema `memory(id TEXT PK, tier, scope, content, tags JSON, importance REAL, confidence REAL, created_at, last_used, use_count, embedding BLOB)` + `FTS5(content, tags)` virtual table + optional `sqlite-vec` virtual table when the extension loads. Extension load is tried at startup; missing ⇒ silent BM25-only fallback.

## 6. Consolidation schedule & algorithm

**Trigger points** (pick all three):
1. **End of SOW** — always; Stoke already has a meta-reasoner hook here.
2. **Between sessions** (the new live meta-reasoner — §9) — mini-pass on just the previous session's episodes.
3. **Size threshold** — when unconsolidated episodic > 500 rows OR disk > 50 MB.
4. **Nightly hygiene** — decay + dedupe + retention tiers, cron-style.

**Dedupe algorithm** — follow CloudSwarm: cosine ≥ 0.92 on embeddings ⇒ archive the newer duplicate. Without embeddings: Jaccard on content tokens ≥ 0.85 AND overlapping tags. Always prefer merging over deletion — set `status=merged` and carry an `evidence_ids` list so the source episodes remain auditable (critical for Stoke's provenance story; aligns with `internal/ledger/` append-only model).

**Merge heuristic** when the LLM says "REINFORCE": bump `confidence += 0.1` (clamp 1.0), append the new episode's `task_id` to `evidence_ids`, update `last_used`. On "CONTRADICT": old → `status=superseded`, new created with `supersedes` back-pointer, run `contradiction.DetectContradictions` to surface the disagreement kind for audit.

## 7. Retrieval integration points — recommended top-K + format

| Hook | Tier focus | Top-K | Format |
|---|---|---|---|
| Before session planning | Semantic (repo conventions) + Episodic (recent failures) | 8 core + 10 query | H2 block, bullets `[category] content (tag1, tag2)` — matches `ForPrompt` today |
| Before worker dispatch | Semantic + Procedural (skills) | 5 core + 8 query | Inject into system prompt under `## Relevant learnings`, hard-cap 1200 tokens |
| Before delegation (choose agent) | Semantic (agent reliability scores keyed by `tag=agent:<role>`) | top-3 by confidence | Not prompt — surfaced to scheduler as `float64 score` |
| Before verification | Semantic `tag=false-positive` + Episodic `tag=verifier-error` | 3 only | One-liner reminder: "Known false positives near this change: X, Y" |

Core/query split (from CloudSwarm) is the right pattern: **core** = importance-gated slice you always include; **query** = embedding-similar for the current task. Stoke should add a **scope filter** (global > repo > task) applied before ranking — repo memories always win ties against global.

## 8. Unify `wisdom/` and `memory/`?

No — **keep separate, unify the interface**. Rationale:
- `wisdom/` is prevention rules: LLM-produced, upvote/downvote curated, pattern-matched by `FindByPattern`. Maps to **Semantic tier**.
- `memory/` has 6 categories (gotcha/pattern/preference/fact/anti_pattern/fix) + decay + tags. Maps to **Episodic + Semantic**.
- `skill/` maps to **Procedural**.
- `checkpoint/` maps to **Working**.

Stoke's `tiers.go` already declares this mapping in the package docs. The right move is to make `wisdom.Store` and `skill.Store` implement `memory.Storage`, register them through `memory.Router`, and route writes by tier. Callers get one API (`router.Put/Query/Vote`) and the backends keep their specialisations (wisdom votes, skill name/description, episodic composite scoring). This preserves the V2 bridge (`internal/bridge/`) that already emits bus events from wisdom writes.

## 9. Live meta-reasoner between sessions

**Design**: after S1 merges, spawn `meta-reasoner` on the last session's event log (bus events + verification results + failure fingerprints). Prompt: "Produce at most 5 JSON prevention rules for S2+." Write rules to `wisdom` at `tier=semantic, scope=repo, tag=meta-rule, confidence=0.6`. Next dispatch's `ForPrompt` picks them up automatically.

**Cost estimate** — Sonnet-class model, ~8k in / ~1k out per session transition. At current list prices (~$3/$15 per MTok) that's ≈ $0.04/transition. A 10-session SOW adds ≈ $0.40 — negligible vs. a single worker's cost. Gate behind `STOKE_META_LIVE=1` so ops can disable on cost-capped runs; reuse `costtrack.OverBudget` as hard stop.

**Cheap trick** — don't re-reason if no failures happened in the prior session (check `taskstate` + verify results). This drops average cost by ≈ 60% on clean SOWs.

**Budget guardrails**: reuse the existing `context` three-tier budget; cap injected meta-rules to 400 tokens per worker prompt; if over, drop lowest-confidence first.

## 10. Concrete Stoke changes

1. `internal/memory/sqlite.go` — SQLite backend with FTS5 + optional `sqlite-vec`, implementing `Storage`.
2. `internal/memory/scope.go` — add `Scope` enum (Global/Repo/Task) on `Item`; filter in `Router.Query` before rank.
3. `internal/memory/embed.go` — pluggable `Embedder` interface (API / local / noop), wired via `config.yaml`.
4. `internal/memory/consolidate.go` — CloudSwarm-style chunk→LLM→merge, triggered by end-of-SOW + size-threshold + nightly cron hooks.
5. `internal/memory/hygiene.go` — decay / dedupe / retention-tier ports of the CloudSwarm activities.
6. `internal/memory/retrieve.go` — `CoreAndQuery(ctx, scope, query, coreLimit, queryLimit)` returning the two-slice result; wire into `prompts.BuildPlanPrompt / BuildExecutePrompt / BuildReviewPrompt`.
7. `internal/wisdom` and `internal/skill` — add thin `memory.Storage` adapters; register them in `app/` startup.
8. `cmd/r1 memory {list,consolidate,dedupe,export}` — operator CLI, following `cmd/r1 wisdom` pattern.
9. Live meta-reasoner — new `internal/metareason/` package; subscribe to `bus` session-complete events; gated by `STOKE_META_LIVE`.

---

**Sources:**
- [Agent Memory Paper List (survey index)](https://github.com/Shichun-Liu/Agent-Memory-Paper-List)
- [Memory for Autonomous LLM Agents survey](https://arxiv.org/html/2603.07670v1)
- [Position paper — Episodic Memory is the Missing Piece](https://arxiv.org/pdf/2502.06975)
- [AgentCore long-term memory deep dive (AWS)](https://aws.amazon.com/blogs/machine-learning/building-smarter-ai-agents-agentcore-long-term-memory-deep-dive/)
- [Semantic vs Episodic vs Procedural Memory in AI Agents (Medium)](https://medium.com/womenintechnology/semantic-vs-episodic-vs-procedural-memory-in-ai-agents-and-why-you-need-all-three-8479cd1c7ba6)
- [Cascade Memories docs (Windsurf)](https://docs.windsurf.com/windsurf/cascade/memories)
- [Understanding Windsurf's Memories System (arsturn)](https://www.arsturn.com/blog/understanding-windsurf-memories-system-persistent-context)
- [Best Embedding Models 2026 — PE Collective](https://pecollective.com/blog/best-embedding-models-2026/)
- [Open-source Embedding Models 2026 — BentoML](https://www.bentoml.com/blog/a-guide-to-open-source-embedding-models)
- [Choose an Embedding Model for RAG 2026 — Milvus](https://milvus.io/blog/choose-embedding-model-rag-2026.md)
- [sqlite-vec v0.1 stable release](https://alexgarcia.xyz/blog/2024/sqlite-vec-stable-release/index.html)
- [DuckDB vs SQLite — Analytics Vidhya](https://www.analyticsvidhya.com/blog/2026/01/duckdb-vs-sqlite/)
- [Sparse vs Dense Retrieval — ML Journey](https://mljourney.com/sparse-vs-dense-retrieval-for-rag-bm25-embeddings-and-hybrid-search/)
- [Hybrid Search RRF — Premai.io](https://blog.premai.io/hybrid-search-for-rag-bm25-splade-and-vector-search-combined/)
- CloudSwarm code: `platform/temporal/activities/{store_memory.py,retrieve_memories.py,memory_consolidation.py}`, `workflows/memory_consolidation.py`, `migrations/{012_memory.sql,018_semantic_memory.sql}`, `docs/adr/004-pgvector-for-memory.md`.
