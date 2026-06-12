# Deep Audit — r1 Eval Agent Runtime

**Date:** 2026-06-02
**Auditor:** Claude (Opus 4.8), 6 parallel subagents + direct inspection
**Scope:** Verify how much of the advertised system ("complete agnostic agent runtime using memory, transparency, mapping, and deterministic + double-checked flows to enforce completion and prevent models from getting lazy/lying; plus a Claude-vs-Codex adversarial mode with automatic review and completion enforcement") is actually built vs missing/incomplete/weak.
**Method:** Read + grep + trace call graphs from `cmd/r1/main.go`; `go build ./...` (PASS, exit 0); targeted `go test` per subsystem (all green). No files modified — read-only audit.

---

## Headline verdict

This is a **real agent runtime, not a facade.** ~312K lines of first-party Go across 184 internal packages, almost no stubs (the only "not implemented" strings in first-party code are inside *anti-stub detection tooling*). The binary genuinely execs the `claude` and `codex` CLIs in isolated git worktrees, runs a native Anthropic-API agent loop, and enforces completion through hard merge gates.

**The central structural finding is a "two-worlds" gap:** the most impressively-documented subsystems — the V2 governance layer (`supervisor` deterministic rules engine, `ledger` append-only graph, `bus` durable WAL) and the `cortex` parallel-cognition engine — are **real, well-tested code that is largely or entirely unwired from the live execution path.** The completion-enforcement that *actually runs* lives in `internal/workflow`, not in the governance packages the docs foreground.

So the accurate one-line summary is: **the live dual-engine + workflow-gate path is genuinely strong and honest; the "V2 governance / cognition" superstructure is built but not plugged in.**

---

## Scorecard

| Pillar (claim) | Built? | Wired/live? | Rating |
|---|---|---|---|
| **Dual-engine adversarial (Claude⟷Codex)** | Yes | Yes (legacy/`sow` path) | **REAL** |
| **Completion enforcement (merge gates, anti-fakery)** | Yes | Yes — `workflow.go` | **REAL** |
| **Anti-deception / anti-laziness checks** | Yes | Partial (some advisory-only) | **REAL→PARTIAL** |
| **Cost tracking + budget halt** | Yes | Yes — aborts runs | **REAL** |
| **Memory — wisdom (capture→inject→recall)** | Yes | Yes | **REAL** |
| **Memory — agent-tool memory (store/recall)** | Yes | Yes (native loop) | **REAL** |
| **Memory — cross-session recall bridge (`app.Memory`)** | Yes | **No — never assigned** | **DEAD** |
| **Mapping — repomap PageRank, injected to prompts** | Yes | Yes | **PARTIAL (Go-only)** |
| **Mapping — semantic/vector search** | Yes | Yes | **PARTIAL (bag-of-words, not embeddings)** |
| **Transparency — logging / cost** | Yes | Yes | **REAL** |
| **Transparency — telemetry / metrics** | Yes | **Write-only (never read back)** | **PARTIAL** |
| **Research FTS5 full-text search** | Yes | **Compiled out (no `fts5` build tag)** | **DORMANT** |
| **V2 governance — supervisor rules engine** | Yes (34 rules) | **Zero importers — fully dead** | **ORPHANED** |
| **V2 governance — ledger (content-addressed graph)** | Yes | Only CLI/audit cmds, not mission path | **REAL but not governing** |
| **V2 governance — bus (durable WAL)** | Yes | MCP + bench only, not mission executor | **REAL but peripheral** |
| **V2 governance — consensus loops (7-state)** | Yes | **Zero runtime callers** | **ORPHANED** |
| **Cortex — Workspace/Round/Router/Lobes** | Yes (~9k lines) | **Never `Start()`ed; hook never assigned** | **REAL but runtime-dead** |
| **specexec (speculative parallel exec)** | Yes | Yes (`--specexec`) | **REAL (plan-only caveat)** |

---

## 1. Dual-engine adversarial mode — REAL (the headline works)

This is the strongest validation of the project's pitch. Evidence:

- **Both CLIs are really exec'd** with process-group isolation:
  - Claude: `internal/engine/claude.go:81` (arg construction: `-p`, `--output-format stream-json`, `--tools`, `--max-turns`, `--settings`, `--strict-mcp-config`…), exec at `:131`, `killProcessGroup` SIGTERM→SIGKILL at `:201`.
  - Codex: `internal/engine/codex.go:51` (`exec --cd … --sandbox … --json --output-last-message … --profile …`), exec `:72`, 429/usage-limit detection `:164`.
  - A third real runner shells the native Anthropic Messages API (`internal/engine/native_runner.go` → `internal/agentloop/loop.go`). Gemini runner also present (`engine/gemini.go`).
- **Cross-model review is wired into the live verify phase**, not orphaned: `model.CrossModelReviewer` (`internal/model/router.go:164`) maps Claude→Codex / Codex→Claude. `internal/app/app.go:404` builds *both* runners; `workflow.Engine.pickRunner` selects the *opposite* engine as reviewer in the verify phase; invoked at `workflow.go:1311`, gated by `Policy.Verification.CrossModelReview` (default `true`, `config/policy.go:319`).
- **Merge is hard-blocked on failure** (this is the real completion enforcement — see §2).
- **Caveat — one new entrypoint is a stub:** `r1 run --output stream-json` ("CloudSwarm" mode) is *announce-only* — `dispatchCloudSwarmSOW` "intentionally does NOT fork a worker" (`run_cmd.go:271`); `dispatchCloudSwarmFreeText` emits `"status":"announce_only"`. The real loops are the legacy `run`/`build`/`mission` and the `sow`-native path.

## 2. Completion enforcement — REAL, and the gates are honest

The enforced chokepoint is `internal/workflow/workflow.go`. The gates *return errors, clean the worktree, and refuse merge* — they are not cosmetic:

- **Verify gate** (build/test/lint): `workflow.go:737` runs `verify.NewPipeline(...).Run`; failure routes to retry/escalate (`:1246`) and never reaches merge.
- **Review verdict gate:** `workflow.go:1474` — `if !verdict.Pass { Cleanup; advanceState(Failed); return error }`.
- **Anti-rubber-stamp quality gate:** reviewer must have actually read ≥half the changed files (Claude) or referenced ≥1/3 (Codex), else Failed (`workflow.go:1437-1470`).
- **Anti-fakery verdict parser:** rejects `pass=true` with zero findings and `pass=false` with zero findings — defeats a trivial `{"pass":true}` bypass (`parseReviewVerdict`).
- **Final merge gate:** `workflow.go:1115-1122` — `if !evidence.AllGatesPass() { return "merge blocked: gates failed" }` and `if !State.CanCommit() { return "merge blocked" }`. Only then does `CommitVerifiedTree` + `Merge` run.
- **taskstate** provides the evidence-gated phase machine behind `CanCommit()` / `advanceState`.
- **convergence** (adversarial self-audit) is an additional pre-merge gate (`workflow.go:974-1112`).
- **Gaps:** the PostToolUse hook is **advisory only** (doesn't block), and a batch of shell hooks referenced by the SessionStart machinery (`detect-self-skip.sh`, `detect-stubs.sh`, `detect-overmocked.sh`, ~40 `enforce-*.sh`) are **missing/retired** — the SessionStart hook reported `FAILED open or read` for all of them this session. The Go-level gates are the real enforcement; the shell-hook layer is partly bit-rotted.

## 3. Anti-deception machinery — REAL primitives, mixed wiring

`critic`, `convergence`, and `cortex/lobes/antitrunc` contain real logic to detect truncation/laziness phrases and scope underdelivery. The hard enforcement that's actually on the critical path is the workflow gates (§2). Some checks exist as libraries that aren't on every path. Honest assessment: **coherent and enforced for the `workflow` path; advisory or unwired in places.**

## 4. Memory + transparency — wisdom & cost are the genuine pillars

- **wisdom — REAL full loop:** captured during execute/retry (`workflow.go:1238,1283`) and at session end via LLM extraction (`sow_wisdom.go:117`); persisted to `.stoke/wisdom/*.json`; **injected back** into execute/system prompts via `Store.ForPrompt()` (`workflow.go:611`, `sow_native.go:4170`); `FindByPattern` is real retrieval (in-memory scan + indexed SQLite). This is a real capture→persist→inject→recall cycle.
- **agent-tool memory — REAL:** `memory_store`/`memory_recall`/`memory_forget` exposed to the native loop (`internal/tools/memory_tools.go`), persisted to `.r1/agent-memory.json`, `Recall` uses real TF-relevance scoring. The model can write and recall within/across tasks.
- **cost tracking — REAL and enforced:** real per-model pricing → USD (`costtrack/tracker.go:30`), and **budget genuinely halts work** — `workflow.go:552-565` aborts each attempt with "budget exceeded" + worktree cleanup.
- **Dead/dormant transparency:**
  - `app.RunConfig.Memory` cross-session recall (`app.go:230-240`) is **never assigned in the CLI** — orphaned.
  - `telemetry.Collector` and `flowtrack` phase inference are **write-only** — recorded, never read back in the live runtime.
  - **FTS5 is compiled out:** `research`, `wisdom/sqlite`, and `membus` advertise FTS5 full-text search, but no build path sets the `sqlite_fts5`/`fts5` tag (Makefile, install.sh, CI all omit it), so `tryFTS5()` always fails and search silently degrades to a `LIKE` scan. Tests don't gate on `hasFTS`, so they pass against the fallback and never prove FTS5 works in a shipped binary.
  - **Two parallel memory systems** (`memory.Store` JSON via agent tools vs `membus` SQLite bus) that don't share data.

## 5. Mapping — REAL algorithms, but Go-centric and "embeddings" are lexical

- **repomap — REAL but Go-only.** PageRank is genuine iterative rank propagation (`repomap.go:362-392`: damping 0.15/0.85 over importers, + symbol-count and call-graph bonuses) — not a filesize heuristic. It **is** injected into execute prompts (`workflow.go:2049`, 2000-token budget, via `ctxpack`). **But** `Build` runs entirely on `goast.AnalyzeDir` (`repomap.go:62`) = `go/parser`, **Go only.** For a non-Go repo the ranked map is empty. This directly undercuts the "complete agnostic" claim *at the mapping layer*.
- **symindex — multi-language but regex-based:** Python/TS/JS/Go via regex patterns (`symindex/index.go:97+`), not AST. So lighter symbol indexing is language-agnostic; deep ranked mapping is not.
- **semdiff — REAL Go AST** (`go/parser`, `semdiff.go:311+`); line-based for other languages.
- **vecindex — real cosine math over fake embeddings:** `EmbedFunc` interface is real, but every production caller uses `BagOfWordsEmbed` (research store, MCP codebase server) — i.e. lexical TF vectors, **not neural embeddings.** Code comments are honest about this ("not embeddings… a follow-up can swap in embedding-backed similarity"). "Vector/embedding semantic search" is therefore aspirational at the model layer.
- **tfidf — real** TF-IDF, used by skill index and the memoryrecall lobe.

## 6. V2 governance — built, tested, and NOT plugged in (biggest gap vs docs)

The `CLAUDE.md` package map foregrounds a "V2 governance" layer. It is real, high-quality, well-tested code — and almost none of it governs a real run:

- **supervisor (deterministic rules engine):** 34 real rules (claimed 30) across 12 categories (claimed 10), with genuine deterministic conditions/actions (e.g. `rules/drift/budget_threshold.go:50` fires at 50/80/100/120%; `rules/trust/completion_requires_second_opinion.go:52` queries the ledger for an independent reviewer). The engine (`core.go:226`) is real. **But `supervisor.New(` has zero non-test callers anywhere in the repo** — it is entirely dead. (Note: the ~20 "supervisor" mentions in `main.go` refer to the V1 `boulder` idle-detector, and `serve_cmd.go`'s "rules API" wires a *different* `internal/rules` package — naming is conflated.)
- **ledger (append-only content-addressed graph):** REAL — salted SHA256 content commitments, type-prefixed node IDs, Merkle parent-linkage, dual filesystem + SQLite persistence, immutability enforced (no Update/Delete methods; a test AST-scans to prove it), 52 real node types (claimed 22). **But the mission/workflow run path never constructs or writes it** — only the `honesty`/`artifact`/`ledger verify` CLI subcommands do. The governance graph is not populated by actual runs.
- **bus (durable WAL):** REAL — append-only NDJSON, `fsync` per append, replay-on-open, causality enforcement, delayed events. **But its only live consumers are the MCP cortex backend and the bench harness** — not the mission executor.
- **consensus loops (7-state):** real append-only state machine, **zero runtime callers.**
- **bridge (the claimed V1→V2 connector):** its only non-test referent is a doc-comment, not a code import.

**Net:** "deterministic governance flows" exist as a coherent, tested *library*, but they do **not govern an actual mission run today.** This is the largest gap between documentation and runtime reality, and the most likely thing to mislead a reader.

## 7. Cortex — the most engineered subsystem, runtime-dead

`internal/cortex` (~8,936 lines) is genuinely the best-engineered code in the repo: real Workspace/Round/Router/Lobe GWT-style loop (`cortex.go` lifecycle with atomic-CAS start, `MidturnNote` superstep, `PreEndTurnGate` blocking `end_turn` on unresolved critical Notes), real lobes (`rulecheck`, `planupdate` makes real Haiku calls, etc.). **But:**

- The `agentloop.CortexHook` field (`cfg.Cortex`) is **never assigned in any production path** — the live native loop uses `BuildNativeSupervisor`, not cortex.
- The only live entry, `r1 mcp serve`, wires lobes but **never calls `cortex.Start()`** (comment admits "Lobe runners are inert until Start"), and the LLM-lobe mode is explicitly RESERVED behind a `stubProvider` that errors on every call.

So the sophisticated parallel-cognition engine has no live driver. A user can only round-trip Workspace Notes via MCP tools with inert lobes.

---

## What's genuinely strong (don't rebuild these)

1. Dual-CLI execution with worktree + process-group isolation (`engine/`).
2. Cross-model review wired into a real, blocking verify phase (`workflow.go`).
3. Honest, anti-fakery merge gates — verdict parser, reviewer-read-evidence quality gate, scope/protected-file re-scan.
4. wisdom capture→inject→recall loop.
5. Real budget enforcement that aborts runs.
6. ledger / bus / supervisor / cortex are well-built and well-tested *as libraries*.
7. specexec parallel strategy harness with real winner-scoring.

## What's missing / incomplete / weak (priority order)

1. **Governance not wired (highest impact):** `supervisor`, `ledger`, `bus`, `loops` don't participate in a real mission. The "deterministic double-checked flows" that the pitch emphasizes are enforced by `workflow.go` gates, *not* by the governance engine the docs showcase. Either wire supervisor/ledger into the mission path or stop documenting them as active governance.
2. **Cortex runtime-dead:** assign `cfg.Cortex` in the native loop (or call `Start()` in MCP) — otherwise ~9k lines of cognition code never executes.
3. **Agnosticism is overstated at the mapping layer:** the flagship ranked repomap is Go-only; non-Go repos get regex symbol indexing at best. Pitch says "complete agnostic"; mapping is not.
4. **"Embeddings" are bag-of-words:** vecindex never uses a neural embedding provider in production. Either wire a real `EmbedFunc` or relabel as lexical search.
5. **FTS5 compiled out everywhere:** add the `sqlite_fts5` build tag to Makefile/install/CI, or remove the FTS5 code and its misleading "full-text search" claims. Tests should assert `hasFTS`.
6. **Write-only observability:** telemetry Collector, metrics counters, flowtrack phase inference are recorded but never surfaced. Either render them or drop them.
7. **Dead CLI bridges:** `app.RunConfig.Memory` cross-session recall is never assigned; `promptguard_boot.FingerprintBootPrompt` is defined but never called.
8. **Bit-rotted shell-hook layer:** ~40 `enforce-*.sh`/`detect-*.sh` hooks referenced by SessionStart are missing (`FAILED open or read`). The Go gates still work; the shell anti-deception layer is broken.
9. **CloudSwarm `--output stream-json` entrypoint is announce-only** — no worker dispatch.
10. **Doc drift:** package map says 132 internal packages (actual 184), 30 supervisor rules (34), 22 ledger node types (52), 10 rule categories (12). Counts are *understated* — the code exceeds the docs — but the docs imply the governance layer is active when it isn't.

## Verification status
- `go build ./...` → exit 0 (clean).
- Targeted `go test` per audited subsystem → all green (engine, model, workflow, specexec, ledger, bus, supervisor rules, wisdom, costtrack, memory, research, replay, cortex).
- `go vet ./internal/... ./cmd/...` → exit 0 (clean).
- Full `go test ./...` to completion was **not** run (large suite); not claimed as evidence.
