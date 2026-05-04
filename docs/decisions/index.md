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

## 2026-05-04 — Spec 8 Agentic Test Harness

These five decisions accept the design choices in `specs/agentic-test-harness.md` as binding for the harness, lint, and docs surface.

### D-A1 — Single `r1.*` MCP namespace; legacy `stoke_*` dual-aliased until v2.0.0
**Decision:** All new tools land under `r1.*`. The 5 legacy `stoke_*` SOW tools are preserved verbatim per `canonicalStokeServerToolName` and dispatch to the same handlers. Removal scheduled for v2.0.0.
**Owners:** spec 8 (agentic-test-harness).
**Implications:** The Slack-style envelope's `links.deprecations[]` carries a one-time warning when a session calls a `stoke_*` name. CHANGELOG records the removal at the v2.0.0 cut.
**Source:** `specs/agentic-test-harness.md` §10a "Stoke alias removal".

### D-A2 — Slack-style envelope at the `r1_server.go` boundary; stokerr/ taxonomy for every error
**Decision:** Every `r1.*` tool response wraps in `{ok, data?, error_code?, error_message?, links?}`. Every error maps to one of the 10 `internal/stokerr/` codes via `MapErrorToTaxonomy`. Raw Go error strings are forbidden at the wire.
**Owners:** spec 8.
**Implications:** Handlers that return `fmt.Errorf("...")` are silently re-mapped (with string heuristics) and a future spec will tighten this so direct `*stokerr.Error` is the only legal form.
**Source:** §3 "Existing Patterns to Follow", §6.

### D-A3 — Synthetic accessibility tree, NOT pixel snapshots
**Decision:** Both TUI and web surfaces emit a structured `A11yNode` tree (`role`, `name`, `state`, `children`). Snapshot assertions fire against the tree; the rendered string is debug-only. `lipgloss.SetColorProfile(termenv.Ascii)` is mandatory in `NewShim` for byte-determinism.
**Owners:** spec 8.
**Implications:** Computer Use as a primary driver is deferred to Q3 2026; the harness exercises a11y trees, not pixels. The §10a "Snapshot drift" mitigation depends on this.
**Source:** §5, §10a.

### D-A4 — UI without API is a build break
**Decision:** Every interactive UI component (React onClick, Bubble Tea KeyMsg consumer, Tauri command) MUST reference an MCP tool from the live r1.* catalog. The `tools/lint-view-without-api/` scanner enforces this in CI and via `r1.verify.lint`.
**Owners:** spec 8.
**Implications:** Adding a UI button without a corresponding MCP tool fails the build. Adding a tool without a UI is a WARN (allowlist for `headless_only` cases).
**Source:** §8.

### D-A5 — Gherkin-flavored markdown DSL (`*.agent.feature.md`); no bespoke language
**Decision:** Test fixtures use Markdown-shaped Gherkin (Given/When/Then in `- ` list items under `## Scenario:` headers). The runner at `tools/agent-feature-runner/` parses them and dispatches each step via heuristics + per-file `## Tool mapping` blocks. No custom DSL.
**Owners:** spec 8.
**Implications:** Authoring fixtures is a 0-cognitive-load task for any human who has read a Cucumber file. The Markdown lint and table-of-contents generators in `docs/` work across the harness corpus without special handling.
**Source:** §6, §10, §11.

Cross-link: `specs/agentic-test-harness.md` (BUILD_ORDER 8). Implementation tracked in `build/agentic-test-harness` per the 43-item §12 checklist.
