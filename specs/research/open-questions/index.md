# Open Questions — Operator Decisions Needed

Per-spec open questions that cannot be resolved from research alone. Check with operator before final spec write.

## High-impact (answer before proceeding)

### Q1. `stoke run` command shape
Currently missing entirely. Options:
- **(A)** Full new command that takes free-text TASK_SPEC (matches CloudSwarm contract exactly). Chat-like entry.
- **(B)** Alias `stoke run --sow path/to.md` as a lightweight wrapper over `stoke ship`.
- **(C)** Both modes: `stoke run "free text"` routes to chat-intent, `stoke run --sow path.md` routes to SOW executor.
**Recommendation:** (C). Matches CloudSwarm's free-text contract AND preserves SOW path.

### Q2. Default for `STOKE_DESCENT`
Currently opt-in via env flag. Hardening in spec-1 adds anti-deception + per-file cap + bootstrap-per-cycle.
- **(A)** Keep opt-in through 2026-Q2; flip to default after ladder regression-free for 2 weeks.
- **(B)** Flip default-on with this scoping work and use a kill-switch env var `STOKE_DESCENT=0`.
**Recommendation:** (A). Stability first.

### Q3. First deploy provider
- **(A)** Fly.io only in spec-6; Vercel + Cloudflare in a follow-on spec.
- **(B)** Fly.io + Vercel together (2x scope).
- **(C)** All three.
**Recommendation:** (A). Fly.io has widest language support; ship iteratively.

### Q4. Memory backend scope in spec-7
- **(A)** SQLite + FTS5 only (BM25). Defer sqlite-vec + embeddings to v2.
- **(B)** SQLite + FTS5 + sqlite-vec + optional embedding API in v1.
**Recommendation:** (A). Ship the auto-retrieval + scope hierarchy first; add vectors when real retrieval quality gap appears.

## Medium-impact (defaults acceptable, confirm if opinion)

### Q5. Anti-deception opt-out
Currently proposed: no opt-out. Confirm?

### Q6. Per-file repair cap
3 (Cursor match) vs 5 (more lenient). Default 3.

### Q7. HITL standalone timeout
CloudSwarm auto-rejects at 15 min. Standalone default:
- (A) 1 hour (blocking read times out)
- (B) Infinite (block until operator responds)
**Recommendation:** (A). Prevents indefinite hangs in CI.

### Q8. A2A card signing
Signed agent cards landed in A2A v1.0. Use:
- (A) Same HMAC key as delegation tokens
- (B) Separate Ed25519 keypair (`STOKE_A2A_CARD_KEY`)
**Recommendation:** (B). Different threat model.

### Q9. Research executor: default parallelism cap
5 subagents. Raise? Lower? Budget-tied?
**Recommendation:** Cap at 5; automatic 1/2-4/10+ effort scaling gated by `--effort` flag.

### Q10. Intent Gate classifier model
Haiku (cheap, fast) vs on-device regex only.
**Recommendation:** Verb-scan first (deterministic), Haiku only on ambiguity.

## Low-impact (defer to implementation)

### Q11. Exact streamjson event subtype naming
Under `_stoke.dev/descent/tier`, `_stoke.dev/verify/start`, etc. Naming bikeshed-resolvable at build time.

### Q12. HMAC key rotation mechanism
Out of v1 scope. Single `STOKE_DELEGATION_SECRET` works for solo deployment.

### Q13. Memory consolidation schedule
Nightly vs on-demand vs size-threshold. Default: nightly with `--on-demand` override.
