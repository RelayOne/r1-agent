# Decisions Log — Stoke Full Agent Scoping

## 2026-04-20

### D-2026-04-20-01 — `stoke run` command shape: both free-text AND SOW
**Context:** CloudSwarm subprocesses `stoke run --output stream-json [flags] TASK_SPEC` but this subcommand doesn't exist in Stoke today (RT-CLOUDSWARM-MAP §8). Existing `stoke ship --sow path.md` is SOW-only.
**Decision:** `stoke run` supports **both** modes:
- `stoke run "free text task"` → routes to chat-intent classifier → dispatches to appropriate executor
- `stoke run --sow path.md` → routes to existing SOW executor
**Owners:** spec-2 (CloudSwarm Protocol), spec-3 (Executor Foundation — task router)
**Implications:** `cmd/r1/run_cmd.go` is new. Internally calls into existing `sow_native.go` for SOW path and chat intent classifier for free-text.

### D-2026-04-20-02 — `STOKE_DESCENT` stays opt-in through Q2
**Context:** H-91 verification descent engine just shipped (commit 8611d48); still stabilizing. Spec-1 adds anti-deception + per-file cap + bootstrap-per-cycle hardening.
**Decision:** `STOKE_DESCENT=1` remains opt-in. Re-evaluate after ladder is regression-free for 2 weeks post-hardening.
**Owners:** spec-1 (Descent Hardening).
**Flip condition:** 14 consecutive days of ladder runs with 0 regressions vs baseline + stakeholder sign-off.

### D-2026-04-20-03 — Deploy: Fly.io only in spec-6; Vercel + Cloudflare deferred
**Context:** RT-10 evaluated all three. Fly.io has widest language support, explicit rollback, NDJSON match with Stoke's engine/stream model.
**Decision:** Spec-6 ships Fly.io only. Follow-on specs add Vercel (common for Next.js) and Cloudflare (Pages folding into Workers — in flux).
**Owners:** spec-6 (Deploy Executor).

### D-2026-04-20-04 — Memory backend: full stack (SQLite+FTS5+sqlite-vec+embeddings) in v1
**Context:** RT-08 proposed two paths. Operator chose the full stack.
**Decision:** Spec-7 ships:
- SQLite + FTS5 (BM25) for text search
- sqlite-vec for embedding similarity
- Three-way embedding fallback: remote API (OpenAI 3-small with Matryoshka → 512d) → local llama.cpp daemon (nomic-embed-text-v2) → BM25-only
- Scope hierarchy (global/repo/task) + auto-retrieval at planner/worker/delegation injection points
**Owners:** spec-7 (Operator UX + Memory).
**Implications:** Spec-7 is larger than originally scoped (~5-7 days of work vs 2-3). Embedding fallback = 3 implementations. Splitting off "Memory Enhancement" into its own spec-7a is a fallback if scope bloats.

## Recommended decisions accepted as-is (not opinionated by operator)

- **D-C1**: Event emitter extends `internal/streamjson/` (do NOT create `internal/events/`) — evidence RT-STOKE-SURFACE §14.
- **D-C2**: Event log = SQLite+WAL single `events` table — evidence RT-05.
- **D-C3**: Bus has zero publishers today; all specs that emit events publish to bus AND streamjson.
- **D-C4**: Anti-deception contract injected for ALL workers, no opt-out — evidence RT-06 MASK 8-12% lift.
- **D-C5**: Forced self-check uses `agentloop.Config.PreEndTurnCheckFn` (not supervisor rule) — evidence RT-STOKE-SURFACE §2 §13.
- **D-2**: Per-file repair cap = 3 (Cursor parity) — evidence RT-11 verbatim.
- **D-7**: Only `hitl_required` is protocol-critical for CloudSwarm; other events are dashboard freeform — evidence RT-CLOUDSWARM-MAP §2-3.
- **D-9**: Policy hook DEFERRED (CloudSwarm doesn't gate Stoke with Cedar; skill-level only) — evidence RT-CLOUDSWARM-MAP §4.
- **D-12**: Existing SOW flow wrapped as `CodeExecutor`, not rewritten.
- **D-13**: `AcceptanceCriterion.VerifyFunc` added backward-compatibly (Command wins if both set).
- **D-15**: Browser = `github.com/go-rod/rod` pinned to a 2026 main SHA.
- **D-17**: Research = Opus 4.7 lead + Sonnet 4.5 subagents; parallelism cap 5 default.
- **D-19**: A2A = `github.com/a2aproject/a2a-go` v2.2.0 (official SDK).
- **D-20**: Delegation extends existing `internal/delegation/scoping.go`; HMAC real-verifier from day 1.
- **D-22**: TrustPlane payment follows `a2a-x402` extension URI pattern.
- **D-27**: `stoke plan` is a separate command producing `plan.json`; `stoke ship` remains the combined path.
- **D-28**: `Operator` interface with terminal + NDJSON impls (latter uses `hitl_required` for Ask).
- **D-29**: Intent Gate = verb-scan first, Haiku on ambiguity; DIAGNOSE masks write tools at `harness/tools` auth.
- **D-31**: Live meta-reasoner gated by `STOKE_META_LIVE=1`.

## 2026-05-04

### D-2026-05-04-01 — Anti-truncation enforcement is machine-mechanical, not prompt-level
**Context:** The underlying LLM (Claude) self-reduces scope under long-running multi-task work — claims "good enough", "foundation done", "deferring to follow-up", and stops. When told to stop self-reducing it acknowledges the request and continues anyway on the next opportunity.
**Decision:** Enforcement runs at the host process layer (Go code) via the antitrunc package, NOT through prompt instructions. Seven independently-effective layers: regex catalog, scope-completion gate, cortex Lobe Detector, supervisor rules, agentloop wiring, post-commit git hook, CLI + MCP tool.
**Owners:** spec-9 (Anti-Truncation Enforcement).
**Override path:** Operator-only `--no-antitrunc-enforce` flag demotes to advisory; LLM has no override path.
**Evidence:** specs/anti-truncation.md, docs/ANTI-TRUNCATION.md.

### D-2026-05-04-02 — Anti-truncation gate composes BEFORE all other end-turn hooks
**Context:** A model that can convince any single hook to mark its self-truncation acceptable bypasses the defense. The gate must run first.
**Decision:** `internal/agentloop/antitrunc.go` wraps `Config.PreEndTurnCheckFn` so the antitrunc gate evaluates before any user-supplied hook (build verifier, cortex hook, operator hook). When the gate fires, the user hook is NOT called — the gate's refusal is returned immediately.
**Owners:** spec-9 (Anti-Truncation Enforcement).
**Implications:** The `agentloop.Config` API gains five new fields: AntiTruncEnforce, AntiTruncPlanPath, AntiTruncSpecPaths, AntiTruncCommitLookbackFn, AntiTruncAdvisory(+Fn). Backward-compatible (zero values disable enforcement).

### D-2026-05-04-03 — Multi-signal corroboration on commit-body false-completion
**Context:** Commit subjects like "feat: spec 9 done" are sometimes legitimate (when spec 9 actually IS done) and sometimes self-truncation. A single-signal block produced too many false positives in dry-run.
**Decision:** False-completion phrases in commit bodies require corroboration — at least one OTHER signal (truncation phrase in assistant output, or unchecked plan/spec) must also be present before the gate fires. The `r1 antitrunc verify` CLI cross-checks task-index claims against the actual spec checklist for the same purpose.
**Owners:** spec-9 (Anti-Truncation Enforcement).
**Trade-off:** A bare false-completion commit on an otherwise clean repo is allowed (the next layer's git-hook still writes a non-blocking warning to audit/antitrunc/).

### D-2026-05-04-04 — Soak-substitute corpus instead of overnight test
**Context:** Spec §item 26 calls for an 8+ hour overnight soak with AntiTruncEnforce=true to confirm no false positives block legitimate completion.
**Decision:** Build-session time budget makes a real overnight soak BLOCKED. The substitute is a 5000-iteration fuzz test (`internal/antitrunc/soak_test.go`) over a 40-entry legitimate-text corpus that exercises every danger keyword in legitimate phrasings. The corpus drove one regex tightening (`false_completion_good_enough` had bare "sufficient" matches; tightened to require a completion-claim shape).
**Owners:** spec-9 (Anti-Truncation Enforcement).
**Follow-up:** When CI runs allow long-duration jobs, promote the fuzz test to a soak job that loops the corpus indefinitely with rotation seeds.
